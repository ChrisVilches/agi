// Package hooks runs programs the user wrote at three fixed moments in a run:
// before a tool call is made, after one has produced a result, and as agi is
// about to print something the model wrote.
//
// A hook is the user's policy and not agi's. It is told which tool is about
// to run and with what, and it answers by its exit status and, optionally, a
// JSON object on standard output: let the call through, change its arguments,
// refuse it, change what the model reads afterwards, add context, or stop the
// agent that made the call. agi has no opinion about any of it, and a run
// given no hooks behaves exactly as it did before they existed.
//
// The third moment is not like the other two. A MessageDisplay hook is handed
// text agi is about to show and may hand back different text to show instead,
// and that is the whole of what it can do: the conversation, the snapshot and
// the log keep the model's own words, because the text reaches a hook of this
// event only after all three have stored it. It is what a markdown renderer
// is written as.
//
// The protocol is Claude Code's, deliberately, so that a hook script written
// for that program usually works here. It is a flavor of it and not a copy —
// agi has no interactive permission prompt, no plugins and no transcript file
// — and every place the two differ is listed in docs/hooks-specification.md,
// which is the whole of the feature and the document to read before changing
// anything here.
//
// This package knows nothing about the agent loop, the CLI, or the event log.
// It is handed a tool's name and the raw JSON arguments the model produced,
// and it hands back either the data as the hooks left it or an error saying
// what a hook decided.
package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/ChrisVilches/agi/tools"
)

// Event names a moment in a run at which hooks run.
//
// There are three, and the strings are the format: they are what a manifest
// writes and what a hook reads back in hook_event_name, so renaming one makes
// every manifest written before the rename read differently.
type Event string

const (
	// PreToolUse runs before a tool call is made. It can refuse the call or
	// change the arguments it is made with.
	PreToolUse Event = "PreToolUse"
	// PostToolUse runs after a call has produced a result, and can change
	// what the model reads. It does not run for a call that failed: Claude
	// Code has a separate event for that one and agi does not implement it.
	PostToolUse Event = "PostToolUse"
	// MessageDisplay runs when agi is about to show the user something the
	// model wrote, and can hand back different text to show instead. It
	// changes what is printed and nothing else: the conversation, the
	// snapshot and the log all keep the model's own words, which is true by
	// the shape of the thing rather than by care — the text has already been
	// stored by the time a hook of this event sees it.
	//
	// There are two things the model writes and agi displays, and a hook of
	// this event renders one of them: the prose it wrote for the reader, or
	// the deliberation behind it, which a hook asks for with "thinking".
	MessageDisplay Event = "MessageDisplay"
)

// events is every event a manifest may name, which is what an unknown name in
// one is checked against. It is in declaration order rather than sorted,
// because that is the order they happen in and the order a message about them
// reads best in.
var events = []Event{PreToolUse, PostToolUse, MessageDisplay}

// AboutTool reports whether the event is one that happens around a tool call.
//
// Two of the three are, and what separates them is not a detail: a tool event
// has a tool's name to match on, named parameters to test conditions against,
// and a call that can still be refused. MessageDisplay has none of those — it
// is handed text that has already been produced and answered — so everything
// written for the first kind has to ask before assuming the second.
//
// It is a method on the event rather than a list kept by each caller, for the
// reason Watches is a method: a caller comparing names would have to be
// corrected every time an event was added, and the one that was missed would
// be wrong quietly.
func (e Event) AboutTool() bool { return e == PreToolUse || e == PostToolUse }

// Hook is one entry of a manifest: which calls it runs for, and what to run.
//
// Everything in it was settled when the manifest was read. The regular
// expressions are compiled, the program is resolved, and what is left for a
// call is deciding whether it matches and then running it.
//
// What is deliberately not settled is whether the program can be run. The
// executable bit can change while a run is going, so checking it once at the
// start would be a promise agi cannot keep; the only honest check is the
// attempt itself.
type Hook struct {
	// event is which of the three moments this hook runs at.
	event Event
	// matcher accepts the tool's name. A nil matcher accepts every tool,
	// which is what a manifest says by leaving it out, writing it empty, or
	// writing "*". A hook of an event that is not about a tool has none, and
	// is refused for writing one.
	matcher *regexp.Regexp
	// conditions are one regular expression per named tool parameter,
	// combined with OR: the hook runs if at least one of them matches. An
	// empty map imposes no condition.
	conditions map[string]*regexp.Regexp
	// enabled is which kinds of run the hook is for: one where agi's output
	// is a terminal, one where it is a pipe or a file, both, or neither. A
	// hook whose file said nothing is for both, which is what every hook was
	// before the field existed.
	//
	// It is on every event and not only on MessageDisplay, where the need
	// for it was first felt. A guard is as entitled to the distinction as a
	// renderer is: one that opens an editor, asks a question, or posts to a
	// chat room has an attended run and an unattended one to tell apart, and
	// a field that answered the question for one event and not the others
	// would be a rule the user has to remember the shape of.
	enabled enablement
	// thinking is which half of what agi prints a MessageDisplay hook is
	// for: the model's deliberation, or the prose it wrote for the reader.
	// It is one or the other and never both, so a run that wants each
	// rendered writes two hooks — which is the honest shape, since the two
	// are not the same text and are rarely laid out the same way.
	//
	// It is false on every hook of every other event, and those are refused
	// for writing the field at all: a tool hook prints nothing, so there is
	// no half of the output for it to choose between.
	thinking bool

	// command is the program to run, or a shell command line when args is
	// nil. In the first case it is already absolute; in the second it is the
	// user's own text and is handed to a shell as it was written.
	command string
	// args are the arguments the program is run with, and their presence is
	// what says this is the exec form rather than the shell form. A hook with
	// no arguments and no shell is spelled with an empty, non-nil args.
	args []string
	// dir is the folder the manifest is in, which is where the program runs.
	// It is what makes a relative path in a shell command line mean what its
	// author meant, and what makes a folder of hooks something you can move.
	dir string
}

// Event returns the moment the hook runs at.
func (h *Hook) Event() Event { return h.event }

// Command returns the program the hook runs, or the shell command line.
func (h *Hook) Command() string { return h.command }

// Thinking reports whether the hook renders the model's deliberation rather
// than the prose it wrote for the reader.
//
// It is for showing the user what the run was armed with, the way Matcher is.
// Two display hooks are otherwise indistinguishable in a listing, and they
// fire on different text, so a line that did not say which would be a listing
// that claims a renderer is armed over the message it is not armed over.
func (h *Hook) Thinking() bool { return h.thinking }

// Matcher returns the tool-name pattern as the file wrote it, or "*" for a
// hook that runs for every tool. It is for showing the user what the run was
// armed with, which is why it renders the absent case rather than reporting
// it.
func (h *Hook) Matcher() string {
	if h.matcher == nil {
		return "*"
	}
	return h.matcher.String()
}

// Watches reports whether the hook's matcher accepts a tool of this name.
//
// It is a question about a tool event and is asked of nothing else: a hook
// that has no matcher because its event has no tool would answer true here
// and mean nothing by it.
//
// It is the first half of what decides whether a hook runs, asked on its own
// so that a caller which knows the tools a run has can find a hook that can
// never fire. Whether the hook's conditions would then accept the call is a
// question about a call, and there is none here.
//
// It is a method rather than something a caller works out from Matcher,
// because what an absent or wildcard matcher means is this package's to know:
// a caller comparing strings would have to reimplement the three spellings of
// "every tool" and would get one of them wrong eventually.
func (h *Hook) Watches(name string) bool {
	return h.matcher == nil || h.matcher.MatchString(name)
}

// Call is one tool call, as the hooks around it are told about it.
//
// It is what varies from call to call and nothing else: which run this is and
// how long a hook may take belong to the Set, because neither changes while a
// run is going.
type Call struct {
	// Tool is the name the model addressed, which is what a matcher is
	// tested against.
	Tool string
	// Arguments are the arguments the call will be made with — not
	// necessarily the ones the model wrote, since a hook earlier in the
	// sequence may already have changed them.
	Arguments json.RawMessage
	// RequestID is what joins a hook's own record of a call to agi's, and is
	// sent as tool_use_id.
	RequestID string
	// Agent names the agent making the call: fork-1, subagent-2. A guard may
	// hold a subagent to a stricter rule than the agent the user is talking
	// to, and that agent is the empty string — the call is then a
	// main-thread one and agent_id is left out, which is how Claude Code
	// says the same thing.
	Agent string
	// Elapsed is how long the call took, for the PostToolUse hooks. It is
	// zero before the tool has run, which is every PreToolUse hook.
	Elapsed time.Duration
	// Response is the shape this tool's result is shown to a hook in, and
	// read back from. Nil is a tool whose result is a plain string both
	// ways, which is every tool in agi that does not run a program.
	Response tools.Responder
	// Ran is what the program this call ran did, for the PostToolUse hooks.
	// It is nil before the tool has run, which is every PreToolUse hook, and
	// nil for every tool that runs no program — the same caveat Elapsed
	// carries, and for the same reason.
	//
	// Nothing in this package reads a field of it. It is carried from the
	// tool that made it to the same tool again, through Response, so that a
	// guard can be shown the two streams apart without this half of the
	// program learning which tool it is holding.
	Ran *tools.Execution
}

// Message is one piece of text agi is about to show the user, as the
// MessageDisplay hooks around it are told about it.
//
// It is a type of its own rather than a field on Call, because it is not a
// call: nothing is about to happen to the machine, nothing can be refused,
// and there is no tool, no arguments and no request to correlate with.
// Widening Call to cover both would be naming the whole after the part, which
// is the mistake Set was renamed to avoid.
type Message struct {
	// Content is the text as it stands: what the model wrote, or what an
	// earlier hook in the sequence left.
	Content string
	// Agent names the agent whose message this is, the same way Call does.
	// A fork's prose is displayed and a subagent's is not, so in practice a
	// hook sees main and fork-1 here.
	Agent string
	// Thinking says this is the model's deliberation rather than the prose
	// it wrote for the reader, which is what decides the hooks it is shown
	// to: a hook declares one of the two and sees that one only.
	//
	// It is a field of the message rather than an entry point of its own
	// because nothing else about the two differs. They are the same text
	// going to the same place through the same sequence, and the only
	// question is which hooks were written for this one.
	Thinking bool
}

// Set is every hook a run was given.
//
// It is a Set and not a Guard, which is what an earlier draft called it.
// Guarding is one of the three things a hook does, and the name would have
// been wrong for the other two: a hook that rewrites a call's arguments,
// redacts a secret out of a result, or adds a line of context guards nothing.
// Naming the whole after the part is the widening this repository refuses
// elsewhere, and it is why the native tools are called native rather than
// control flow.
//
// Registry was the near miss, echoing tools.Registry one level over. It loses
// because a registry here is registered into and looked up by name, and
// nothing ever looks a hook up: the model never addresses one, which is also
// why two hooks of one name are not an error.
type Set struct {
	// hooks are in the order they will run: the order the folders were given
	// on the command line, and within a folder the order the manifest lists
	// them. Order is the user's and is never rearranged, because one hook's
	// output is the next one's input.
	hooks []*Hook

	// timeout bounds one hook execution, from the moment the process is
	// spawned to the moment it finishes. It is one number for every hook of
	// a run, since --hook-timeout is one flag.
	timeout time.Duration
	// session is what a hook is told this run is called. It is here rather
	// than on a Call because it does not vary within a run, and a field
	// repeated at every call site is one that will eventually be filled in
	// wrongly at one of them.
	session string
	// cwd is agi's own working directory, which a hook is told because its
	// own is the folder its manifest is in. It is read once: agi never
	// changes directory, so asking again would be asking a settled question.
	cwd string
	// tty says agi's own standard output is a terminal, which is what an
	// enabled field is read against.
	//
	// It is the run's fact and not an agent's. A subagent's streams are all
	// discarded and a fork's are marked, and neither changes what agi is
	// attached to: a guard that may only run in an attended run is in one
	// whenever the user is watching, wherever in the run it fires. So it is
	// here beside session, for the reason session is here — it does not vary
	// within a run, and a field repeated at every call site is one that will
	// eventually be filled in wrongly at one of them.
	//
	// False is the safe way round for a set nobody told. A hook written for
	// a terminal is the one that paints escape sequences, and not running it
	// leaves a message plain rather than leaving a pipe full of codes.
	tty bool
}

// Config is what a Set needs beyond the hooks themselves.
type Config struct {
	// Timeout bounds one hook execution. A zero or negative timeout means
	// DefaultTimeout, so a caller with no opinion — a test, mostly — does
	// not have to have one.
	Timeout time.Duration
	// Session identifies the run to a hook. See Identify, which is how the
	// caller turns a session's directory into one.
	Session string
}

// DefaultTimeout bounds one hook execution when a Config says nothing.
//
// It is short because a hook stands between the agent and its next tool call
// and the agent can do nothing until it answers. Ten seconds is long enough
// for a guard that reads a file, greps a command line, or asks a local
// service, and short enough that a hook which has hung is noticed rather than
// waited on. A hook with more to do than that is what --hook-timeout is for.
const DefaultTimeout = 10 * time.Second

// MinTimeout is the smallest --hook-timeout may be.
//
// Below a second a hook that was going to answer is killed for being started
// on a busy machine, which is the same failure as a broken hook and stops the
// agent just as hard. It is exported because the flag is refused in the
// command layer, where the user can act on it, and a second spelling of the
// number over there would be a second floor the first time either changed.
const MinTimeout = time.Second

// identifierLength is how much of the hash a session is identified by.
//
// Sixteen hexadecimal characters is sixty-four bits, which no plausible
// number of sessions comes near colliding in, and it is short enough to sit
// inside a filename beside whatever else a hook script puts there. The whole
// digest would be four times as long for no reachable gain.
const identifierLength = 16

// Identify turns a session's directory into the identifier a hook is told the
// run by, which is the first identifierLength characters of the SHA-256 of
// that directory's absolute, cleaned path.
//
// A hook is a fresh process on every call and remembers nothing, so a guard
// that wants to warn once per run, or count what the agent has touched, needs
// a key. The three things such a key has to be are what decided the shape of
// this one. It has to be the same on every call of a run and on every later
// run of that session, which a plain function of the path is. It has to be
// safe inside a filename, which a path is not, since every state-keeping hook
// script ever written does something like /tmp/notified_$SESSION_ID.flag. And
// it has to be unique, which is why the whole path is hashed and not the
// directory's last segment: a user keeping ~/work/notes and ~/play/notes has
// two conversations, and an identifier built from the last segment would have
// given them one and mixed their state without anything ever saying so.
//
// The path is made absolute first because --session takes any directory the
// user likes and they may type it relatively. Two runs started as
// --session ./chat from different folders are two conversations and must not
// share an identifier; one session reached by two spellings is one
// conversation and should not have two. A path that cannot be resolved — a
// working directory agi cannot read — is hashed as it was given, which is the
// best answer left and no worse than not having one.
//
// Symbolic links are deliberately not resolved. See the known limitations in
// docs/hooks-specification.md: it leaves one directory reachable under two
// identifiers, which is the harmless direction, and resolving would make the
// identifier depend on the state of the filesystem rather than on how agi was
// invoked.
func Identify(dir string) string {
	path := filepath.Clean(dir)
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])[:identifierLength]
}

// New returns a set holding no hooks but configured like a real one. It is
// what None is for a run that was given none, and what a test builds from.
func New(cfg Config) *Set {
	s := &Set{timeout: cfg.Timeout, session: cfg.Session}
	if s.timeout <= 0 {
		s.timeout = DefaultTimeout
	}
	// A working directory agi cannot read is left empty rather than failing
	// the run: it is one field of a payload, and a hook that wanted it can
	// see that it is not there. Nothing agi does depends on it.
	if cwd, err := os.Getwd(); err == nil {
		s.cwd = cwd
	}
	return s
}

// None is a run that was given no hooks. Every call passes through untouched,
// so no caller has to ask whether there are any — the same bargain a
// discarded event log makes with a run that asked not to be recorded.
func None() *Set { return New(Config{}) }

// A nil *Set is a run with no hooks, and every method below reads one as
// such rather than panicking.
//
// It is the bargain eventlog.Log already makes, and for the same reason: the
// hooks reach everything that builds an agent, through wiring, and a caller
// with nothing to say about them — a test, or the throwaway registry that
// collects the reserved tool names — should not have to say None() to avoid
// a crash in the middle of a run. A guard that is absent lets everything
// through, which is what a run given no hooks means.

// WithSession returns a copy of the set that tells a hook which run this is,
// identified from the directory the session is stored in.
//
// It is a second step rather than part of Load because the two are settled at
// different moments. The folders are read while the command line is being
// read, so a mistake in a manifest costs a message before anything else
// exists; the session is not created until after that, and for a run that
// named none its directory does not exist until agi makes one. A set that had
// to wait for the session would move every mistake in a manifest past the
// point where it is cheap to find.
//
// It returns a copy because the hooks are the run's and the identifier is one
// fact about it: nothing here is meant to change once a run is going, and a
// setter would be an invitation for it to.
func (s *Set) WithSession(dir string) *Set {
	if s == nil {
		return nil
	}
	copied := *s
	copied.session = Identify(dir)
	return &copied
}

// WithTTY returns a copy of the set told whether agi's own output is a
// terminal, which is what decides an enabled field naming one kind of run.
//
// It is a second step for the reason WithSession is one, and not the same
// reason. The folders are read while the command line is being read, so that
// a mistake in a manifest costs a message before a gateway is contacted; where
// the output is going is not the folder's business at all, and a Load that
// asked for it would have every caller of it — the tests among them — answer
// a question about terminals to read a file.
//
// It returns a copy because the hooks are the run's and this is one fact
// about it, settled once and never again while the run is going.
func (s *Set) WithTTY(yes bool) *Set {
	if s == nil {
		return nil
	}
	copied := *s
	copied.tty = yes
	return &copied
}

// Skips reports whether the hook is one this run will never reach, because
// its enabled names the other kind of run.
//
// It is for the listing a verbose run opens with. A hook that is loaded,
// listed, and cannot fire is the shape cli.refuseDeadMatchers refuses, and
// the only thing that makes this one acceptable is that the user asked for
// it — so the listing says so, and the user is never left reading a line
// that claims a guard is armed when it is not.
func (s *Set) Skips(h *Hook) bool {
	if s == nil {
		return false
	}
	return !h.enabled.allows(s.tty)
}

// Displays reports whether this run has a MessageDisplay hook for one of the
// two kinds of message, the one Message.Thinking names.
//
// It is asked because agi formats what it prints when nobody else will. The
// model's deliberation is dimmed on its way to the screen, and that dimming
// is a default and not chrome: a run that armed a hook over the deliberation
// armed something to decide how it looks, and a default laid underneath would
// leave every such hook renderering into text agi had already styled. So the
// caller asks first and keeps its own formatting only for the half of the
// output nobody claimed.
//
// It answers for this run and not for the folder, since forEvent drops a hook
// whose enabled names the other kind of run: a renderer written for a
// terminal has not claimed the deliberation of a piped run.
func (s *Set) Displays(thinking bool) bool {
	for _, h := range s.forEvent(MessageDisplay) {
		if h.thinking == thinking {
			return true
		}
	}
	return false
}

// Len returns how many hooks the run was given.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.hooks)
}

// All returns every hook, in the order they run. It is for saying what a run
// was armed with, which is one of the things that has to be known to
// reproduce it.
func (s *Set) All() []*Hook {
	if s == nil {
		return nil
	}
	return s.hooks
}

// forEvent returns the hooks declared for one event, in the order they run.
//
// Whether each of them matches the call is asked separately, and asked again
// for every hook rather than once for the sequence. A PreToolUse hook can
// rewrite the arguments, so the call a later hook is judging is not
// necessarily the one the model wrote: deciding the whole list up front would
// let one hook change `ls` into `rm -rf /` and walk it straight past the
// guard whose condition is `^rm `. A hook matches against the call as it
// stands when its turn comes.
//
// Whether the hook is for this kind of run is asked here rather than beside
// the matching, because it is the one question that is settled for the whole
// run: nothing a hook does can turn a pipe into a terminal, so a hook the
// run cannot reach is not in the list at all. Asking it here is also what
// gives the three events one answer — a sequence that filtered its own hooks
// would be three places to add the fourth event to.
func (s *Set) forEvent(event Event) []*Hook {
	if s == nil {
		return nil
	}
	var found []*Hook
	for _, h := range s.hooks {
		if h.event == event && h.enabled.allows(s.tty) {
			found = append(found, h)
		}
	}
	return found
}

// matches reports whether the hook runs for a call on tool with args.
//
// A hook matches when its matcher accepts the tool's name and its conditions
// accept the arguments. Both halves are the file's, and both default to
// accepting, so a hook that says neither runs for every call of its event.
func (h *Hook) matches(tool string, args json.RawMessage) bool {
	return h.Watches(tool) && h.satisfied(args)
}
