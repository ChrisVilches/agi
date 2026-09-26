package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// FileName is the file a folder's hooks are declared in. It is a fixed name at
// the root of the folder, because what is named on the command line is a
// folder of resources and this is the half of it that holds hooks.
//
// It used to be manifest.json, shared with a folder's tools under one
// top-level key each, and the price of sharing was that this package named
// "tools" in a field it read and threw away while the tools package named
// "hooks" back at it. A name that says what the file holds is what removed
// both: neither package has any reason to open the other's file.
const FileName = "hooks.json"

// shellMeta are the characters that only mean something to a shell.
//
// They are refused in the exec form's program name, where they cannot work:
// a manifest writing {"command": "jq .x | grep y", "args": [...]} has mixed
// the two forms up, and without this it would be asking the kernel to run a
// file whose name contains a pipe. That failure would arrive in the middle of
// a run, and a hook that cannot be started stops the agent — so a mistake
// that costs a message here would otherwise cost the work in flight.
const shellMeta = "|&;<>()$`\n"

// manifest is the file's shape: an event name to the hooks filed under it.
//
// It is a map rather than an object with two named fields so that an unknown
// event name is caught here, with the names there are, instead of arriving as
// the JSON decoder's own words about an unknown field.
type manifest map[string][]entry

// entry is one hook as the file writes it.
type entry struct {
	Matcher string            `json:"matcher"`
	If      map[string]string `json:"if"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
}

// Load reads every folder given and returns the hooks they declare, in the
// order the folders were named and the order each manifest lists them in.
//
// The order is kept and never rearranged, and unlike the order of a folder of
// tools it decides something: hooks run in sequence and one hook's output is
// the next one's input, so rearranging them would change what the tool is
// finally called with.
//
// Everything about a folder is checked here, before a gateway is contacted or
// a session is created, the way a tool manifest and a folder of skills are. A
// manifest that is not valid JSON, an event nobody has heard of, a regular
// expression that does not compile, an entry naming no program: each is a
// mistake in how the run was invoked, and it costs a message at the start
// rather than a failed call in the middle of a conversation the user is
// paying for.
//
// Two identical hooks are not an error, wherever they came from. The rule
// that refuses two tools of one name exists because the model addresses a
// tool by its name and could not tell them apart; nothing addresses a hook,
// so two that do the same thing are merely two hooks, and both run.
func Load(dirs []string, cfg Config) (*Set, error) {
	s := New(cfg)
	for _, dir := range dirs {
		found, err := load(dir)
		if err != nil {
			return nil, err
		}
		s.hooks = append(s.hooks, found...)
	}
	return s, nil
}

// load reads one folder's hooks.json and returns the hooks it declares.
//
// A folder with no hooks.json declares no hooks, which is nothing to report:
// the file is one of three a folder of resources may hold, and a folder of
// tools and skills alone is an ordinary folder to be given to a run.
//
// A hooks.json that is there and declares nothing says the same thing, and is
// equally fine. It used to be an error, on the grounds that somebody wrote
// the file and meant something by it, and that was wrong twice over. A file
// holding {} is not ambiguous — it says no hooks as plainly as no file does,
// and nothing about the run then misbehaves quietly. And emptying the file is
// how a person turns their guards off for an afternoon; refusing it makes
// them delete a file they intend to write again, and refuses the whole run
// over a folder whose tools and skills were perfectly good.
//
// What is still refused is a file that cannot be read as hooks at all: a name
// that is not an event, an entry missing its program, a pattern that will not
// compile. Those are mistakes. Declaring nothing is a decision.
func load(dir string) ([]*Hook, error) {
	where := filepath.Join(dir, FileName)
	raw, err := os.ReadFile(where)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", where, err)
	}

	m, err := decode(raw, where)
	if err != nil {
		return nil, err
	}

	// The folder has to be absolute, because it is where a hook's program
	// runs and because a run started from somewhere else must reach the same
	// scripts. A relative --resources is an ordinary way to name a folder.
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", dir, err)
	}

	var found []*Hook
	for _, event := range eventsIn(m) {
		if err := known(event); err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}
		for i, e := range m[event] {
			h, err := e.compile(Event(event), root)
			if err != nil {
				return nil, fmt.Errorf("%s: %s hook %d: %w", where, event, i+1, err)
			}
			found = append(found, h)
		}
	}

	return found, nil
}

// eventsIn returns the event names a hooks.json used, in the order events
// happen rather than the order a map hands them over.
//
// Two runs naming the same folders must produce the same list of hooks, and a
// map's iteration order is deliberately not stable, so reading one straight
// out of it would make the order the hooks run in differ from run to run.
// Names the file used that are not events come last and in a fixed order, so
// that the message about the first of them is the same every time.
func eventsIn(m manifest) []string {
	used := make([]string, 0, len(m))
	for _, e := range events {
		if _, ok := m[string(e)]; ok {
			used = append(used, string(e))
		}
	}

	var unknown []string
	for name := range m {
		if !isEvent(name) {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	return append(used, unknown...)
}

func isEvent(name string) bool {
	for _, e := range events {
		if string(e) == name {
			return true
		}
	}
	return false
}

// known refuses an event agi does not have, naming the ones it does.
//
// agi implements three of Claude Code's events and a manifest may well have
// been written against the rest of them, so a name that is real over there
// and absent here is the likeliest mistake. Refusing it is the rule that
// keeps a promise off the page: a hook filed under an event that never fires
// would simply never run, and nothing would ever say so.
func known(name string) error {
	if isEvent(name) {
		return nil
	}
	return fmt.Errorf("%q is not an event agi runs hooks at; it has %s", name, eventList())
}

func eventList() string {
	names := make([]string, 0, len(events))
	for _, e := range events {
		names = append(names, string(e))
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// decode reads the file, refusing a field it does not know.
//
// An unknown field is refused rather than ignored because every one of them
// is a mistake with a silent version: "match" instead of "matcher" would be a
// hook that runs for every tool, and "commands" instead of "command" a hook
// with no program, which is a guard that quietly guards nothing. A top-level
// name is not a field but an event, and known below refuses one of those with
// the names there are.
func decode(raw []byte, where string) (manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var m manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("reading %s: %w", where, err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("reading %s: more than one JSON value", where)
	}
	return m, nil
}

// compile checks one entry and returns the hook it declares.
func (e entry) compile(event Event, dir string) (*Hook, error) {
	if err := e.matchable(event); err != nil {
		return nil, err
	}

	matcher, err := compileMatcher(strings.TrimSpace(e.Matcher))
	if err != nil {
		return nil, fmt.Errorf("has a matcher that is not a regular expression: %w", err)
	}

	conditions, err := compileConditions(e.If)
	if err != nil {
		return nil, err
	}

	command, args, err := e.program(dir)
	if err != nil {
		return nil, err
	}

	return &Hook{
		event:      event,
		matcher:    matcher,
		conditions: conditions,
		command:    command,
		args:       args,
		dir:        dir,
	}, nil
}

// matchable refuses a matcher or a condition written on an event that has
// nothing to match.
//
// Both are defined over a tool: one is a pattern for its name and the other
// a pattern per named parameter, and a MessageDisplay hook has neither. The
// entry is refused rather than read with the field ignored, which is what
// Claude Code does with a matcher here. Ignoring it produces exactly the
// dead-hook silence that refusing an unfirable matcher was written to stop —
// a person who wrote "matcher": "^bash$" on a display hook believes it only
// renders a bash result, and it renders everything.
//
// It names the field and says why there is nothing for it to do, because the
// correction is to delete the line and the user has to be told which line.
func (e entry) matchable(event Event) error {
	if event.AboutTool() {
		return nil
	}
	if strings.TrimSpace(e.Matcher) != "" {
		return fmt.Errorf("has a matcher, which a %s hook takes none of: it is not about a "+
			"tool call, so there is no tool name to match; remove it, since every %s hook "+
			"runs for every message", event, event)
	}
	if len(e.If) > 0 {
		return fmt.Errorf("has conditions, which a %s hook takes none of: it is not about a "+
			"tool call, so there are no parameters to test; branch on the message inside the "+
			"program instead", event)
	}
	return nil
}

// compileConditions turns the if object into the patterns a call's arguments
// are tested against.
//
// A condition whose pattern does not compile names the parameter it was
// written for, since a manifest may hold several and the regular expression
// alone would leave the user to find out which.
func compileConditions(raw map[string]string) (map[string]*regexp.Regexp, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	conditions := make(map[string]*regexp.Regexp, len(raw))
	for name, pattern := range raw {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("has a condition on %q that is not a regular expression: %w", name, err)
		}
		conditions[name] = compiled
	}
	return conditions, nil
}

// program settles what the hook runs and how, which is the one place the two
// forms of a command are told apart.
//
// Without args the command is a shell command line, handed to a shell as the
// user wrote it. Nothing is resolved: the text is theirs, a path inside it
// may be anywhere in it, and what makes a relative one mean what its author
// meant is that the program runs in the folder its manifest is in.
//
// With args it is a program, run directly with no shell between. A relative
// path is resolved against that same folder here rather than left to the
// working directory, because Go looks a bare name up on PATH in the parent
// before the child ever changes directory, and a resolution that depended on
// which of those happened first would be a rule nobody could hold in mind.
// A name with no separator in it is left alone, so "node" still means
// whatever PATH says it means.
func (e entry) program(dir string) (string, []string, error) {
	command := strings.TrimSpace(e.Command)
	if command == "" {
		return "", nil, errors.New("names no program to run")
	}
	if e.Args == nil {
		return command, nil, nil
	}

	if i := strings.IndexAny(command, shellMeta); i >= 0 {
		return "", nil, fmt.Errorf("gives arguments and a command %q containing %q, which only a "+
			"shell would understand; write the command as a program and put the rest in args, "+
			"or leave args out and let a shell run the whole line",
			command, string(command[i]))
	}

	// A bare name is PATH's to resolve. Anything with a separator is a path,
	// and a relative one is relative to the folder, which is what makes a
	// folder of hooks something you can move.
	if strings.ContainsRune(command, filepath.Separator) && !filepath.IsAbs(command) {
		command = filepath.Join(dir, command)
	}
	// args is kept non-nil, since that is what says this is the exec form.
	args := e.Args
	if args == nil {
		args = []string{}
	}
	return command, args, nil
}
