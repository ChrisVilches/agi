package cli

// TODO: this package imports too much. The agent loop, the tool registry, the
// session, the backend, and the prompts all converge here, so cli is where the
// whole of agi has to be known at once — and it is the one package that cannot
// be reused, bound as it is to a command line.
//
// Most of what is here is not about a command line at all. It belongs in a
// file of its own — presumably under model, which already owns a run's state —
// holding the logic and knowing nothing of the CLI: no flags, no terminal, no
// output routing. What would stay behind is reading arguments and flags and
// deciding where output goes.
//
// Do not start from that sketch. Read the whole source and understand it
// properly first, then write down several ways it could be restructured, and
// only then choose the best of them. A rearrangement made without the full
// picture would only move the same tangle somewhere less visible.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ChrisVilches/agi/eventlog"
	"github.com/ChrisVilches/agi/hooks"
	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/prompt"
	"github.com/ChrisVilches/agi/session"
	"github.com/ChrisVilches/agi/skills"
	"github.com/ChrisVilches/agi/tools"
	"github.com/ChrisVilches/agi/tools/native"
	"github.com/ChrisVilches/agi/tools/workspace"
	"github.com/ChrisVilches/agi/transport"
)

const (
	// defaultMaxTurns bounds one run when --max-turns says nothing, so a loop
	// of tool calls cannot run forever.
	//
	// It is set high rather than tight because the cap stops a run that has
	// gone wrong and is not a budget for one going right. A run that reaches
	// it produced no answer at all, which is the worst thing a run can do,
	// while the turns a long one spends before answering are work the user
	// asked for. A cap low enough to cut an honest run short would trade
	// every answer of that kind for the minutes a runaway loop costs.
	defaultMaxTurns = 200
	// minMaxTurns is the smallest cap --max-turns may be given.
	//
	// A turn is one exchange with the model, and the first of them is rarely
	// the answer: an agent asks for a tool, reads what came back, and often
	// corrects a call that went wrong. A handful of turns is the least a run
	// that touches a tool at all can finish in, so a smaller cap could only
	// end in giving up and is refused rather than obeyed.
	minMaxTurns = 5
	// defaultMaxSubagents bounds how many subagents one call may spawn when
	// --max-subagents says nothing.
	//
	// Eight is wide enough for the fan-outs that are worth making — a handful
	// of independent pieces of reading, each with its own agent — and narrow
	// enough that a model which decides to split the work has not opened more
	// conversations with the gateway at once than the user expected. It is a
	// default and not a ceiling: the flag has no upper bound, because how many
	// agents are worth paying for at once is the user's to judge and nothing
	// here knows the work.
	//
	// The smallest it may be set to is native.MinTasks, which is the tool's
	// own floor rather than a second number: a cap below the fewest tasks a
	// call may carry would refuse every call the tool could be made.
	defaultMaxSubagents = 8
	// maxSchemaAttempts bounds how many times the model is asked to restate
	// its answer as JSON before the run gives up.
	//
	// TODO: parameterize this instead of hardcoding it.
	maxSchemaAttempts = 10
)

// ErrInterrupted is what a run reports when the user stopped it — Ctrl-C at a
// terminal, which reaches agi as a cancelled context.
//
// It is a sentinel rather than an ordinary error because two callers have to
// recognize it. A run that was interrupted still stores its conversation, so
// the work done up to that point is not thrown away and the session can be
// continued; and main maps it to the exit status a shell expects from a
// program that was signalled, rather than to the one an ordinary failure gets.
var ErrInterrupted = errors.New("interrupted")

// interrupted reports whether the run has been stopped. Nothing else cancels
// the context a run is given: it comes from main and is cancelled by the
// signal handler there, so a cancelled context means the user asked for this.
func interrupted(ctx context.Context) bool { return ctx.Err() != nil }

// abandon ends a run that was interrupted, leaving the conversation in a state
// a later run can continue from.
//
// What needs closing out is a batch of tool calls stopped partway through. The
// model asked for them and something has to answer, or the stored conversation
// moves on from a request that is still outstanding — which a provider is
// entitled to reject, and which would make the snapshot unresumable. The calls
// that were never made are answered as calls that could not be made, and each
// one is recorded like any other failure.
//
// The interruption itself is recorded at the turn the run had reached, or at
// no turn at all when it was stopped after the agent loop had finished, during
// the schema phase. A subagent records its own, under its own name, because a
// subagent is an ordinary run and was interrupted like one.
//
// Nothing is printed here, for the reason a termination prints nothing: the
// interruption is reported as the run's error, and main prints that on standard
// error like any other. Saying it here as well would say it twice at a
// terminal, where the progress stream and standard error are the same screen.
func abandon(lg *eventlog.Log, conv *model.Conversation, turn int) error {
	for _, f := range conv.AbandonToolRequests(ErrInterrupted) {
		lg.Record(eventlog.Event{
			Kind:      eventlog.ToolFailure,
			Turn:      turn,
			RequestID: f.RequestID,
			Reason:    f.Reason,
		})
	}
	lg.Record(eventlog.Event{Kind: eventlog.RunInterrupt, Turn: turn, Messages: conv.Len()})
	return ErrInterrupted
}

// contain stops a nested agent's ending from becoming the run's, and is what
// every spawned agent's answer passes through on its way back.
//
// A nested agent's error travels the same path as any tool's: its own converse,
// to whatever ran it, to the tool, to Registry.Answer, to the parent's
// converse. A sentinel the parent's loop reads as a verdict on the run would
// therefore not end that agent — it would end the whole run, on the strength of
// something an agent the user never spoke to decided about a task they did not
// set. So every such sentinel is turned into ordinary text here, where the
// answer crosses back.
//
// The one that does not need catching is *incomplete, and for a reason rather
// than by luck: the --exit-code phase belongs to runAgent, which only the run's
// own agent goes through — a fork and a subagent run converse directly, so
// nothing below the run can produce one.
//
// ErrInterrupted is deliberately not caught. It is the same shape — a sentinel
// the parent inspects — but the parent is meant to see this one: a nested agent
// stopped by Ctrl-C was stopped because the whole run was, and the parent
// abandons its own outstanding calls on the strength of it. The two are told
// apart by type, which is what having two types is for.
func contain(answer string, err error) (string, error) {
	// A hook stopping a nested agent stops that agent and nothing else. The
	// reasoning is the same one this function was written for: the agent that
	// asked for the work reads why its copy or its subagent could not finish,
	// and decides what to do — try the work another way, do it itself, or
	// give up and say so. Nothing is lost by that, because the guarded tool
	// never ran; a parent that goes on to attempt the same work itself fires
	// the same hook and is stopped by it, that time as the agent the user is
	// talking to, which does end the run.
	//
	// The sentinel is not wrapped: errors.As would still find it one layer
	// down, and the guard would read as though it worked while doing nothing
	// at all.
	if stop, is := stopped(err); is {
		return "", fmt.Errorf("a hook stopped this agent: %s", stop.Reason)
	}
	return answer, err
}

// ended reports whether err left a conversation worth storing.
//
// A run that answered is the ordinary case. The others are a run the user
// stopped, a run a guard stopped, and a run that answered and then reported the
// task incomplete: each got as far as it got, everything said was said, and
// nothing about them is in doubt. An ordinary failure is — a gateway that did
// not answer, a reply agi could not make sense of — and storing a conversation
// it cannot vouch for would be storing a guess.
func ended(err error) bool {
	if errors.Is(err, ErrInterrupted) {
		return true
	}
	// A task the model reported incomplete is the whole of what --exit-code
	// adds, and it is not a failure of the run: the loop answered, the answer
	// was printed, and the verdict is the model's reading of its own work. The
	// conversation is worth as much as any other — more, since the next run is
	// likely to pick it up and finish what this one could not.
	var short *incomplete
	if errors.As(err, &short) {
		return true
	}
	// A hook stopping the run is the second of them, and it belongs with the
	// first rather than with a failure: everything said was said, the
	// outstanding calls have been answered as abandoned, and what happened is
	// not in doubt — a guard said no. Throwing the conversation away would
	// discard work the user watched happen, and a session they cannot resume
	// is a poor way to tell them their guard fired.
	_, is := stopped(err)
	return is
}

// wiring is what a run's tools are built from: where its progress goes, the
// backend they talk to, the log they record under, where they keep copies of
// the files they change, the tools the user brought, the skills the run was
// given, how many turns an agent may take, how many subagents one call may
// spawn, and the counter that names the agents it spawns.
//
// One value passes through everything that builds a registry or starts an
// agent, so the run, a fork, and a subagent cannot be given different things by
// accident. It is the reason there is one workspaceRegistry rather than a list
// per agent, applied to the arguments as well as to the list: four functions
// each taking the same nine values is a claim held together by four argument
// lists happening to match, and the next value added is the one passed to three
// of them and forgotten in the fourth.
//
// What is deliberately not in it is the registry. A runner is handed the one
// its agent's verbose listing should compare against, and which that is
// differs by caller: the run's own for a subagent the run spawned, the fork's
// for one a fork spawned. Folding it in here would give one answer for both,
// and the wrong one for the second.
type wiring struct {
	out    output
	client transport.Backend
	log    *eventlog.Log
	// base is the instruction the run starts from — prompt.Base(), or the
	// files --sys named. It is here because a fork reads it too: a fork is the
	// agent the user is talking to, copied, and an answer written under a
	// different instruction would be an answer from somebody else. A subagent
	// does not read it, which is why it is passed to the agents rather than
	// settled once for all of them.
	base    string
	backups string
	user    []tools.Tool
	found   []skills.Skill
	// hooks are the programs that run around every tool call, and every
	// agent of a run is held to the same ones. A guard exists to stop
	// something happening to the machine, and a subagent's bash call does
	// the same damage as the run's, so this is here rather than settled per
	// agent — the same argument the fan-out cap makes one field down.
	hooks    *hooks.Set
	maxTurns int
	// maxSubagents is the most tasks one subagents call may carry. It is here
	// rather than settled in the tool for the reason the turn cap is passed to
	// the loop: how wide a fan-out may be is decided by the command line, and
	// every agent that can spawn one — the run and each fork — is held to the
	// same number.
	maxSubagents int
	// agents names the subagents a run spawns, wherever they were spawned
	// from. See native.Counter for why it is the run's and not a tool's.
	agents *native.Counter
	// maxTerminals is how many terminals one agent may have open at once, and
	// zero for a run that was not given terminals at all — --enable-terminals
	// is off by default, so zero is the usual case. Every agent of a run is
	// held to the same number, for the reason every agent is held to the same
	// hooks.
	maxTerminals int
	// terminals are the terminals of the agent this wiring is building for,
	// and nil when the run has none. They are an agent's own and never
	// shared, so a fork rewires its copy before it builds anything, the way it
	// rewires the log: given its parent's, it would be typing into terminals
	// its parent opened, and leaving behind ones its parent never heard of.
	terminals *workspace.Terminals
}

// newTerminals returns a fresh set of terminals for one agent, or nil when the
// run was not given any. Whoever calls it owns what it returns and closes it
// when that agent ends.
func (w wiring) newTerminals() *workspace.Terminals {
	if w.maxTerminals == 0 {
		return nil
	}
	return workspace.NewTerminals(w.maxTerminals)
}

// workspaceRegistry returns the tools that do ordinary work on the machine:
// the ones agi ships, and the ones the user brought with --resources. This is
// the one list of them: an ordinary run and a subagent both start from it, so
// a tool added here reaches both and the two cannot drift apart.
//
// The user's own tools belong in that list rather than beside it. They are
// work tools in every way that matters — described to the model by their
// definition alone, saying nothing in the system prompt, and absent from a run
// that was given none — so a subagent gets them for the same reason it gets
// bash, which is that there is only one list.
//
// A tool that keeps a copy of what it changes is told where to put it and
// which agent is asking. Both come from the caller rather than from anything
// here, because the name has to be the one the run records under: a copy and
// the log line about it are read together, and a second spelling of an agent's
// name would be two names for one agent. An empty backups directory is a run
// with nowhere to keep copies, and the tools then neither keep nor promise
// them.
//
// The terminal tools are here when terminals is not nil, which is only when
// --enable-terminals asked for them. They are work tools like bash — and they
// are what lets an agent get past every hook written for bash, which is why
// nobody gets them without asking. They arrive as the agent's own terminals
// rather than being built here because they hold state the agent's owner has
// to close when the agent ends, and this function does not know when that is.
func workspaceRegistry(backups, agent string, user []tools.Tool, terminals *workspace.Terminals) (*tools.Registry, error) {
	r := tools.NewRegistry()
	if err := r.Register(
		workspace.NewBash(),
		workspace.NewMkTemp(),
		workspace.NewPatchFile(backups, agent),
		workspace.NewWriteFile(),
	); err != nil {
		return nil, err
	}
	if terminals != nil {
		if err := r.Register(terminals.Tools()...); err != nil {
			return nil, err
		}
	}
	if err := r.Register(user...); err != nil {
		return nil, err
	}
	return r, nil
}

// providedNames are the tools agi itself provides, which is what a tool the
// user defined may not be called.
//
// They are asked of the registry that provides them rather than written out,
// so that a tool added to either list is reserved by that alone. The registry
// built here is thrown away: a backend and a log reach it only through the
// subagents tool, which does not touch them until a subagent is spawned, and
// nothing here spawns one.
//
// It is built with a skill, which this run may not have, because the name a
// tool may not take is agi's whether or not this particular run registers the
// tool. A user tool called loadskill would otherwise be accepted by every run
// that was given no skills and refused by every run that was, which is a
// tools.json that works until the day somebody names a folder of skills too.
//
// The terminal tools are built for the same reason, whether or not this run
// passed --enable-terminals. A name is agi's because agi can register it and
// not because this invocation did, and a list that shrank with the flag would
// be read by both callers as though the flag were about them: a tools.json
// that loads until the day somebody enables terminals, and a hooks.json that
// fails until they do. Nothing is started by building them — a terminal opens
// only when an agent asks for one — so a registry thrown away holds nothing
// that would have to be closed.
func providedNames() (map[string]bool, error) {
	w := wiring{
		out:          quiet(),
		found:        []skills.Skill{{ID: "reserved"}},
		maxTurns:     defaultMaxTurns,
		maxSubagents: defaultMaxSubagents,
		terminals:    workspace.NewTerminals(1),
	}
	r, err := defaultRegistry(w)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, r.Len())
	for _, name := range r.Names() {
		names[name] = true
	}
	return names, nil
}

// defaultRegistry returns the tools available to a run: the work tools, and
// the native tools that are part of the agent itself.
//
// The log is passed in because a subagent records its own work under its own
// name in it, and the subagents tool is where subagents come from. The output
// is passed in for the same reason: a --verbose run says what each subagent
// was given as it starts, and this is the only place that knows how to spawn
// one.
//
// The registry being built is handed to the runner as well, and that is not
// the circle it looks like. A subagent's verbose block names a tool rather
// than explaining it again when the run's own listing already said the same
// thing, so the runner has to be able to ask what that listing said; it asks
// only when a subagent starts, by which time this registry is long finished.
//
// A run given no skills gets no tool for loading one, which is the same rule
// that keeps an unregistered tool out of the system prompt: a tool that could
// only ever answer "there are none" is worth less than no tool at all, and the
// model would have to be told about skills to be told there are none.
func defaultRegistry(w wiring) (*tools.Registry, error) {
	r, err := agentRegistry(w, eventlog.MainAgent, nil)
	if err != nil {
		return nil, err
	}
	if err := r.Register(
		native.NewFork(forkRunner(w, r)),
	); err != nil {
		return nil, err
	}
	return r, nil
}

// forkRegistry returns the tools a fork is given, under the agent name its
// tools file their copies of changed files under, and with loaded already
// marked as handed over in its loadskill.
//
// It holds every tool the run holds, under the same names and with the same
// definitions. One of them will not run: fork is the copy's nested one, which
// refuses every call and says why. That is a change from leaving it out, and
// the reason is what a copy is. A fork carries the whole conversation forward
// and re-sends it on every turn, so it is the one agent whose cost is dominated
// by what was said before it existed — and a provider charges less for a prefix
// it has already seen. A prompt that differed by a section, or a tool list that
// differed by a name, would be a different prefix from the first byte of the
// conversation onward, and the copy would pay full price for every turn it
// inherited. Registering a tool that refuses keeps the two prefixes identical.
// What it costs is a page of guidance a copy will not act on, read once against
// a conversation re-read every turn.
//
// What a copy may not do is therefore said twice and in two voices that agree:
// the refusal when it calls, and the instruction it opens with, which names the
// tool so the first turn is not spent discovering it. That is a departure from
// the rule that an agent's limits are settled by registration alone, and it is
// confined to this one tool, where the saving is real.
//
// A copy may not copy itself because the tree would then be as deep as a model
// cared to make it, where today one copy runs at a time and three levels is the
// most there can be. The refusal does not end the copy — it reads why, carries
// on, and says in its answer whatever it could not settle.
func forkRegistry(w wiring, agent string, loaded []string) (*tools.Registry, error) {
	r, err := agentRegistry(w, agent, loaded)
	if err != nil {
		return nil, err
	}
	if err := r.Register(
		native.NewNestedFork(),
	); err != nil {
		return nil, err
	}
	return r, nil
}

// agentRegistry returns everything a run and a copy of one both hold outright,
// under the agent name its tools file their copies of changed files under, and
// with loaded already marked as handed over in its loadskill.
//
// It is the one list the two build on, which is the whole point of its being
// one function. A tool added here reaches both, so "a copy holds what the run
// holds" is true by construction rather than by two hand-written lists staying
// in step — the argument workspaceRegistry already makes one level down. What
// each adds on top is a fork, and the two forks differ only in whether the tool
// does anything.
func agentRegistry(w wiring, agent string, loaded []string) (*tools.Registry, error) {
	r, err := workspaceRegistry(w.backups, agent, w.user, w.terminals)
	if err != nil {
		return nil, err
	}
	if err := r.Register(
		native.NewSubagents(subagentRunner(w, r), w.found, w.agents, w.maxSubagents),
	); err != nil {
		return nil, err
	}
	if len(w.found) > 0 {
		if err := r.Register(native.NewLoadSkill(w.found, loaded)); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// subagentRunner returns the function the subagents tool spawns one with.
//
// A subagent is an ordinary run: the same backend, the same loop, a
// conversation that starts from a prompt. Two things differ. It runs under the
// system prompt written for it rather than the ordinary base, and it is given
// the work tools without the native ones — which is the whole of what
// stops a subagent spawning subagents. Nothing in its prompt forbids it; it
// simply has no such tool, the same way it has no tool nobody registered.
//
// Skills are the one native tool a subagent can have, and only the ones
// its task named: the agent that wrote the brief chose them, and choosing them
// is part of the same decision as splitting the work up. A subagent given none
// is told nothing about skills at all, so it reads the prompt a subagent has
// always read. The list and the tool arrive together here, as they do for the
// run itself — the section is built from the same skills the tool is.
//
// Its conversation ends with it: only the final answer goes back to the agent
// that asked, and nothing is stored. Its progress is discarded too, since
// several subagents run at once and their steps would interleave into
// nonsense; what the user sees is the list of titles the tool announces before
// they start.
//
// A --verbose run sees one thing more, once per subagent and never again: what
// this agent was given, printed as it starts. That is the prompt it reads —
// the skills its task named among it, since the prompt lists them — and the
// tools it may call, which is the whole of what makes one spawned agent differ
// from another. It is said here because here is where it is settled: which
// skills a subagent gets is decided task by task, so a run cannot say it in
// advance without describing an agent it may never spawn.
//
// The log is the exception to that silence, and it is why the two channels are
// separate. Interleaving is a problem for a display, which a person reads top
// to bottom; it is not a problem for a log, where every line says which agent
// wrote it. So a subagent records everything an ordinary run records, under a
// name of its own, and a run that fanned out can still be read back in full.
func subagentRunner(w wiring, run *tools.Registry) native.Runner {
	// Subagents start at the same moment as one another and each block is
	// several screens of prompt, so they are written out one at a time. Two
	// halves of two blocks on the screen at once would be a page belonging to
	// neither agent, which is the problem their progress is dropped to avoid.
	var screen sync.Mutex

	return func(ctx context.Context, brief native.Brief) (string, error) {
		// The name is settled first because two things are filed under it:
		// the subagent's lines in the log, and the copies its tools keep of
		// files they change. It comes from the brief rather than from the log,
		// which forgets it when there is nothing to record.
		name := subagentName(brief)

		// A subagent's terminals are its own, numbered from 1, and end with
		// it: its answer is all that returns, so a terminal left open would be
		// one nothing could ever use or close.
		terminals := w.newTerminals()
		defer terminals.Close()

		registry, err := workspaceRegistry(w.backups, name, w.user, terminals)
		if err != nil {
			return "", err
		}
		if len(brief.Skills) > 0 {
			// Nothing is marked loaded: a subagent starts from an empty
			// conversation, so there is nothing in it to have loaded.
			if err := registry.Register(native.NewLoadSkill(brief.Skills, nil)); err != nil {
				return "", err
			}
		}

		sub := w.log.Agent(name)
		started := time.Now()
		sub.Record(eventlog.Event{
			Kind:    eventlog.SubagentStart,
			Title:   brief.Title,
			Content: brief.Body,
			Tools:   registry.Names(),
		})

		system := subagentSystemPrompt(registry, brief.Skills)
		if w.out.verbose {
			intro := subagentIntro(name, brief.Title,
				agentSetup{system: system, registry: registry}, explainedBy(run))
			screen.Lock()
			w.out.step("%s", intro)
			screen.Unlock()
		}

		conv := model.NewConversation(system, brief.Body)
		sub.Record(eventlog.Event{Kind: eventlog.SystemPrompt, Content: conv.System})

		answer, err := contain(converse(ctx, quiet(), sub, w.client, registry, conv, w.maxTurns, w.hooks, name))
		if err != nil {
			sub.Record(eventlog.Event{
				Kind:    eventlog.SubagentFinish,
				Title:   brief.Title,
				Reason:  err.Error(),
				Elapsed: eventlog.Since(started),
			})
			return "", err
		}

		sub.Record(eventlog.Event{
			Kind:    eventlog.SubagentFinish,
			Title:   brief.Title,
			Content: answer,
			Elapsed: eventlog.Since(started),
		})
		return answer, nil
	}
}

// forkRunner returns the function the fork tool runs a copy of the run with.
//
// A fork is an ordinary run, as a subagent is: the same backend, the same loop.
// What differs is where its conversation comes from. A subagent starts from a
// prompt somebody wrote for it; a fork starts from the conversation of the
// agent that asked for it, which arrives with the call as brief.Fork.
//
// Its system prompt is assembled and never copied, and that matters more than
// it looks. The parent's prompt explains fork, and a fork's own does not run;
// carrying text across rather than assembling it is how a prompt comes to
// describe tools its reader does not have, which is the standing lie that keeps
// workspace tools out of the system prompt in the first place. Assembling it
// from the fork's own registry says exactly what the fork has.
//
// The base is the parent's, --sys included, and here a fork parts company with
// a subagent. What --sys replaces is the character of the agent the user is
// talking to, and a subagent is not talked to — but a fork is that agent,
// copied, so an answer written under a different instruction would be an answer
// from somebody else.
//
// The skills come across whole: the same list the run was given, and the same
// ones already loaded, because a loaded skill's text arrived as a turn and is
// sitting in the copied conversation. A fork that did not know would spend a
// call fetching a document already above it.
//
// A fork is watched, and that is the one thing it does not share with a
// subagent. Progress is dropped for subagents because several run at once and
// their steps would interleave into nonsense; only one fork runs at a time and
// the agent that asked is blocked while it does, so there is nothing to
// interleave with. Its steps stream out marked, so the user can see whose they
// are.
func forkRunner(w wiring, run *tools.Registry) native.ForkRunner {
	return func(ctx context.Context, brief native.ForkBrief) (string, error) {
		// The name is settled first because two things are filed under it: the
		// fork's lines in the log, and the copies its tools keep of files they
		// change. It is not main: a fork's changes are ones whose reasoning the
		// snapshot will not contain, and telling those copies apart from the
		// run's own is the whole of what the copies are for.
		name := forkName(brief)

		// Everything inside the fork is built from a wiring of its own, and it
		// has to be rewired before the registry is, not after. A fork's
		// subagents are built by forkRegistry from whatever wiring it is
		// handed: given the run's, they record under the run's log handle and
		// their lines come out unmarked, which is the one thing a reader could
		// not work out for themselves, since a fork's subagents are numbered in
		// the run's own sequence. Rewiring here is what makes Log.Agent
		// carrying the mark across do any good.
		inside := w
		inside.log = w.log.Fork(name)
		inside.out = w.out.nested()
		// A fork is a new agent as far as terminals go, though it is a copy
		// in every other way. Its terminals are numbered from 1 and closed
		// when it ends, because only its answer returns: the agent that asked
		// would not know a terminal had been opened, and nothing would ever
		// close it. The parent's own stay where they are, out of its reach.
		inside.terminals = w.newTerminals()
		defer inside.terminals.Close()

		registry, err := forkRegistry(inside, name, loadedSkills(run))
		if err != nil {
			return "", err
		}

		started := time.Now()
		inside.log.Record(eventlog.Event{
			Kind:    eventlog.ForkStart,
			Content: brief.Task,
			Tools:   registry.Names(),
		})

		system := systemPromptFor(inside.base, registry, skillsSection(inside.found))
		if inside.out.verbose {
			// The parent's own setup is rebuilt rather than carried here,
			// because the same arguments always assemble the same prompt —
			// that is the rule a snapshot storing none already rests on. It is
			// rebuilt to be compared against, so a copy given something of its
			// own would show up as a difference rather than be hidden by one.
			parent := agentSetup{
				system:   systemPromptFor(inside.base, run, skillsSection(inside.found)),
				registry: run,
			}
			inside.out.step("%s", forkIntro(name, agentSetup{system: system, registry: registry}, parent))
		}

		instruction := forkInstruction(brief.Task)
		conv := brief.Fork(system, brief.RequestID, instruction)
		inside.log.Record(eventlog.Event{Kind: eventlog.SystemPrompt, Content: conv.System})
		inside.log.Record(eventlog.Event{Kind: eventlog.HarnessInstruction, Content: instruction})

		answer, err := contain(converse(ctx, inside.out, inside.log, inside.client, registry, conv, inside.maxTurns, inside.hooks, name))
		if err != nil {
			inside.log.Record(eventlog.Event{
				Kind:    eventlog.ForkFinish,
				Reason:  err.Error(),
				Elapsed: eventlog.Since(started),
			})
			return "", err
		}

		inside.log.Record(eventlog.Event{
			Kind:    eventlog.ForkFinish,
			Content: answer,
			Elapsed: eventlog.Since(started),
		})
		return answer, nil
	}
}

// loadedSkills asks an agent's registry which skills it has already loaded, so
// that a copy of it starts where it left off rather than back at the beginning.
//
// A registry with no loadskill has loaded none, which is the answer for a run
// that was given no skills at all.
func loadedSkills(r *tools.Registry) []string {
	t, ok := r.Lookup(native.LoadSkillTool)
	if !ok {
		return nil
	}
	loader, ok := t.(*native.LoadSkill)
	if !ok {
		return nil
	}
	return loader.Loaded()
}

// forkName is what a fork's lines in the log are filed under. The number counts
// the forks a run has taken, so a run that forks twice does not reuse the first
// one's name — the rule subagent numbering already keeps, for the same reason:
// the name is also part of a filename.
func forkName(brief native.ForkBrief) string {
	return fmt.Sprintf("fork-%d", brief.Number)
}

// forkInstruction is what a copy of the run is told to do, wrapped around the
// task the model wrote.
//
// It is a harness instruction and not a user turn. The user did not write this;
// the model did, and agi is relaying it, which is exactly what a harness
// instruction is for. Storing it as a user turn would be a false record of who
// spoke, and the record is the one thing agi never rewrites.
//
// It does not open by saying the reader is a copy, because the tool result
// immediately above it has just said so. Two announcements of one fact, one
// from a tool and one from the harness, would reach the copy as two voices with
// nothing to say which of them to believe.
//
// What it does say is which tool will not run here, and that is the one place
// the shape of the tree is stated in words rather than settled by registration.
// A copy is offered fork so that its prompt is the prefix the run's was, and it
// refuses; without this line the copy would learn that by calling it, which
// costs a turn of the work it was copied to do. The wording is a flat statement
// of what is so, in the harness's own voice, and not a rule being laid down —
// the refusal is the rule.
func forkInstruction(task string) string {
	return fmt.Sprintf(`Everything above is the conversation you were copied from. It is your context and not your assignment.

One of the tools offered to you does nothing here. You cannot fork, because a copy may not copy itself, and calling it only wastes a turn. Everything else is yours, subagents included.

Carry out the task below and then answer in prose. That answer is the whole of what returns to the run you were copied from: nothing else you do here is kept, and there is nobody to ask, so settle what you can and say plainly in the answer what you could not.

The task:

%s`, task)
}

// subagentName is what a subagent's lines in the log are filed under. The
// number counts the subagents a run has spawned and was settled before any of
// them started, so the second task of a call is subagent-2 however long it
// took to get going.
func subagentName(brief native.Brief) string {
	return fmt.Sprintf("subagent-%d", brief.Number)
}

// subagentSystemPrompt assembles the system prompt for a spawned agent, which
// runs under an instruction of its own: its answer is final and read by a
// machine, not the start of a conversation with a person.
//
// --sys does not reach it, and that is the point of the distinction. What the
// user swaps is the character of the agent they are talking to; a
// subagent is not talked to at all, and its prompt exists to make its reply
// the kind of thing the subagents tool can hand back. Replacing that with a
// persona would break the one contract a spawned run has.
// The skills are the ones its task named and no others, so a subagent that was
// given none reads no skills section — the rule that keeps an unregistered tool
// out of a prompt, applied to an agent that was given nothing to load.
func subagentSystemPrompt(r *tools.Registry, found []skills.Skill) string {
	return systemPromptFor(prompt.Subagent(), r, skillsSection(found))
}

// skillsSection is the last part of the system prompt: what a skill is, and
// which ones this run was given.
//
// The two are one section because neither is any use alone. The explanation
// tells the model to load a skill when the task calls for one, and the list is
// what it can load; an explanation with no list would describe a tool with
// nothing to call it on, and a list with no explanation would be a set of
// names the model has no reason to think it may ask for.
//
// It is assembled here rather than contributed by the loadskill tool the way
// the other native tools' guidance is, for two reasons. It belongs at the end,
// after everything the tools explain, because it is the one part of the prompt
// that is a catalogue rather than an instruction, and the guidance sections go
// in tool-name order, which would file it in the middle. And the list is not
// the tool's to write: it is what this run was given, and it is rendered by
// the package that found it.
//
// What keeps it honest is that it comes from the same list defaultRegistry
// registers the tool from, so a run with skills has both and a run without has
// neither. The heading is === skills === for the reason the guidance sections
// have one: the prompt is Markdown throughout, and a Markdown heading would
// dissolve into the prose it is meant to separate.
func skillsSection(found []skills.Skill) string {
	if len(found) == 0 {
		return ""
	}
	return "=== skills ===\n\n" + prompt.Skills() + "\n\n" + skills.Describe(found)
}

// systemPromptFor puts a base instruction together with whatever the
// registered tools insist on explaining, and then with the skills the run was
// given. The base is the swappable half — prompt.Base(), the files --sys
// names, or prompt.Subagent() for a spawned run, and nothing here knows which
// it was handed. Native tools append to it whatever base is in use,
// because a model that has not been told what they are for will never reach
// for them. Workspace and user-defined tools contribute nothing here by
// design.
//
// The skills come last, after every explanation, because they are the one part
// that is a catalogue rather than an instruction: what to do with them has
// been said by the time they are listed. A run given none passes nothing and
// the prompt ends where it used to.
//
// It is assembled on every run and never stored, so how agi was invoked is the
// only thing that decides it. The rule that makes that work: the same
// arguments and the same files always produce the same prompt. Nothing here
// may depend on the time, the run, or anything else that varies on its own.
func systemPromptFor(base string, r *tools.Registry, skills string) string {
	parts := []string{base}
	if guidance := r.SystemPrompt(); guidance != "" {
		parts = append(parts, guidance)
	}
	if skills != "" {
		parts = append(parts, skills)
	}
	return strings.Join(parts, "\n\n")
}

// runPrompt wires up a backend, somewhere to store the run, a log, and tools,
// then runs the agent on prompt under the base instruction it was given. A nil
// schema means the answer is whatever prose the model ends on, and an empty
// chosenModel means --model was not given and the run takes the machine's.
//
// The base arrives already settled — read from a file or taken from the one
// agi ships — because whether it could be read at all is a question about the
// command line and is answered there, next to the empty prompt and the schema
// that would not parse, before any of the below exists.
//
// The order matters twice. The backend is settled first, so a run that cannot
// happen at all — no backend chosen, or the chosen one unconfigured — does not
// leave a directory behind. The session comes before the log because the log
// lives in the session's directory, and both come before the tools because a
// subagent records under the same log as the run that spawned it.
func runPrompt(ctx context.Context, out output, chosenModel, base, prompt string, schema *model.Schema, exitCode bool, store sessionFlags, user []tools.Tool, found []skills.Skill, armed *hooks.Set, maxTurns, maxSubagents, maxTerminals int) error {
	backend, err := openBackend(chosenModel)
	if err != nil {
		return err
	}

	sess, err := store.open()
	if err != nil {
		return err
	}
	// Removing the session is what --rm asks for, and it happens however the
	// run ends: an answer, a failure, the user stopping it, the model ending
	// it. Every session other than that one ignores this.
	//
	// It is deferred here, before the log is opened, because deferred calls
	// run in reverse: the log is closed first and the directory goes second,
	// which is the only order that works. Closing the log is what waits for
	// the writer goroutine to finish with the file, and a directory removed
	// before that is a goroutine writing into nothing. Moving either line
	// past the other breaks it silently.
	defer func() {
		if err := sess.Remove(); err != nil {
			out.step("%s\n", tools.Detail(err.Error()))
		}
	}()

	// The session is taken for this run before anything writes into it. Two
	// agi processes sharing one directory would overwrite each other's
	// snapshots and shuffle their lines into one another's log, so the second
	// of them is refused here rather than allowed to produce a session that
	// is neither run's. The lock is given back however the run ends, which is
	// what the deferred release is for; a run killed outright has it taken
	// back by the kernel.
	//
	// It sits between the two defers above and below on purpose. Releasing
	// has to happen after the log is closed, since a log still being written
	// is a run still using the session, and before the directory is removed,
	// since there is nothing to release once it is gone. Deferred calls run
	// in reverse, so this line's position is the order it gets.
	lock, err := sess.Lock()
	if err != nil {
		return err
	}
	defer func() {
		if err := lock.Release(); err != nil {
			out.step("%s\n", tools.Detail(err.Error()))
		}
	}()

	lg, err := store.openLog(sess)
	if err != nil {
		return err
	}
	// Closing the log is what waits for the last events to reach the file, so
	// it happens however the run ends. A log that could not be written is
	// mentioned and nothing more: the answer was the point of the run.
	defer func() {
		if err := lg.Close(); err != nil {
			out.step("%s\n", tools.Detail(err.Error()))
		}
	}()

	// One value carries what every agent in this run is built from, so the run,
	// a fork, and a subagent cannot be handed different things by accident.
	// The counter is made here because it is the run's: it names the subagents
	// this run spawns wherever they were spawned from, and a fork building one
	// of its own would start numbering at one again.
	//
	// The hooks were read while the command line was, so a mistake in a
	// manifest cost a message before any of this existed. What they could not
	// be told then is which run this is, because the session did not exist
	// yet, and they are told here — once, for every agent of the run.
	w := wiring{
		out:          out,
		client:       backend,
		log:          lg,
		base:         base,
		backups:      sess.BackupDir(),
		user:         user,
		found:        found,
		hooks:        armed.WithSession(sess.Dir()),
		maxTurns:     maxTurns,
		maxSubagents: maxSubagents,
		agents:       &native.Counter{},
		maxTerminals: maxTerminals,
	}
	// The run's own agent's terminals are closed however the run ends, which
	// ends whatever was left running in them. It is deferred after the log is
	// opened, so it runs before the log is closed: closing a terminal is part
	// of the run, and a run still ending is one still using its session.
	//
	// The deferral is the guarantee rather than the ordinary path. A run that
	// gets as far as finishing closes them a few lines below, where what was
	// closed can still be said; this one catches every path that returns
	// before reaching that line, and finds nothing left on the paths that do.
	w.terminals = w.newTerminals()
	defer w.terminals.Close()

	registry, err := defaultRegistry(w)
	if err != nil {
		return err
	}

	// Where the run is being kept is how the user resumes it, so it is worth
	// saying, quietly, before anything else scrolls past — when agi is the one
	// that decided where. A run given --session was told the path by whoever
	// started it and is told nothing by being told it again; see
	// sessionFlags.chose.
	if store.chose() {
		out.step("%s\n\n", sessionLine(sess.Dir()))
	}

	started := time.Now()
	lg.Record(eventlog.Event{
		Kind:    eventlog.RunStart,
		Backend: backend.Name(),
		Model:   backend.Model(),
		Gateway: backend.BaseURL(),
		Tools:   registry.Names(),
		Session: sess.Dir(),
		Schema:  describeSchema(schema),
		Content: prompt,
	})

	err = runSession(ctx, out, lg, backend, registry, sess, base, prompt, schema, found, exitCode, maxTurns, w.hooks)

	finish := eventlog.Event{Kind: eventlog.RunFinish, Elapsed: eventlog.Since(started)}
	if err != nil {
		finish.Reason = err.Error()
	}
	lg.Record(finish)

	// A terminal the agent never closed is closed here, and said, because
	// closing it ends whatever was still running inside — a tail, a server
	// started to try something against, an ssh session — and nothing else in
	// the run would tell the user that happened. The agent is asked to close
	// its terminals and usually does; this is the line that makes the other
	// case visible rather than silent.
	//
	// It is said here rather than by the deferral above because this is still
	// inside the run, where there is a screen to say it on and a line left to
	// say it above. Closing twice is nothing: the deferral then finds an empty
	// set.
	//
	// Only the run's own agent is reported. A fork or a subagent closes its
	// terminals when it ends, and numbers them from 1 of its own, so "terminal
	// 1" said for one of those would name a terminal the user watched the run
	// itself open.
	if left := w.terminals.Close(); len(left) > 0 {
		out.step("\n%s\n", tools.Detail(leftOpenLine(left)))
	}

	// The same line again, now that whatever the run printed has pushed the
	// first one off the screen. A run that said where it was being kept only
	// at the top said it to somebody who had not read the answer yet, and the
	// moment the directory is worth having is the moment after — the next
	// question is asked by giving agi the same directory again.
	//
	// A --rm run is the one that says it once. Its directory is about to be
	// removed, and is removed by the deferred call that runs the moment this
	// one returns, so a line here would name a path that no longer exists by
	// the time it is read. The opening line stands for both: while the run is
	// going the directory is there like any other, which is what somebody
	// watching one and wanting to look inside it needs.
	//
	// A run given --session says it at neither end, which is the same rule as
	// the opening line's and not a second one: the two are one line said
	// twice, and somebody who already holds the directory has no more use for
	// the second than for the first.
	if store.chose() && !store.discard {
		out.step("\n%s\n", sessionLine(sess.Dir()))
	}
	return err
}

// sessionLine is how a run says where it is being kept, at both ends of the
// run and nowhere else — and only when agi chose the directory, which is the
// rule in sessionFlags.chose rather than anything this function knows.
//
// It is written as the flag that names the directory rather than as a sentence
// about it, because what the line is for is being pasted. A conversation is
// continued by giving agi the same directory again, so `--session /tmp/agi-1b2`
// is the whole of that command but the prompt, and a user who copies it has
// only to type the next question after it. Saying it in prose would be the
// same number of characters on the screen and two words of typing afterwards.
//
// It is dim for the reason the rest of the progress stream is: a person
// watching a run is reading the answer, and this is here to be found rather
// than read.
func sessionLine(dir string) string {
	return tools.Detail("--session " + dir)
}

// leftOpenLine says which terminals the run closed on the agent's behalf.
//
// It names them rather than counting them, because the numbers are what the
// user watched the run open and what every line about them said; a count would
// be a number they would then have to go back up the screen to spend. It is
// one line and dim, for the reason the session line is: a run that ends tidily
// says nothing here at all, and a run that does not has already said whatever
// mattered higher up.
//
// The wording carries no verb that has to agree with the count, which is what
// lets one sentence serve one terminal and five.
func leftOpenLine(ids []int) string {
	what := "terminal"
	if len(ids) > 1 {
		what = "terminals"
	}
	return fmt.Sprintf("closed %s %s, still open at the end of the run", what, numberList(ids))
}

// numberList writes numbers out the way a sentence does: 1, 1 and 2, or
// 1, 2 and 4.
func numberList(ids []int) string {
	words := make([]string, len(ids))
	for i, id := range ids {
		words[i] = strconv.Itoa(id)
	}
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// describeSchema renders the shape a run was asked to answer in, empty when it
// was asked for none.
func describeSchema(schema *model.Schema) string {
	if schema == nil {
		return ""
	}
	return schema.String()
}

// runSession runs one prompt against a session: pick the conversation up where
// the last run left it, run, and store what it ends as.
//
// The snapshot is written only when the run finished. A run that failed partway
// leaves the session as it was, so resuming continues from the last complete
// exchange rather than from a question that was never answered.
//
// A run the user stopped is stored all the same, and that is not an exception
// to the rule but the same rule read properly. A failure means agi does not
// know what happened — a gateway that did not answer, a reply it could not
// make sense of — and storing a conversation it cannot vouch for would be
// storing a guess. An interruption is not in doubt: everything up to it was
// said, the outstanding calls have been answered as abandoned, and throwing
// that away would discard work the user watched happen and paid for. What they
// asked for last may then sit next to what they ask for next, two user turns
// running, and no provider minds.
func runSession(ctx context.Context, out output, lg *eventlog.Log, client transport.Backend, registry *tools.Registry, sess *session.Session, base, prompt string, schema *model.Schema, found []skills.Skill, exitCode bool, maxTurns int, set *hooks.Set) error {
	system := systemPromptFor(base, registry, skillsSection(found))
	// A verbose run opens by saying what it is about to do the work with: the
	// model, the prompt that will be read, and what each tool was described
	// as. It is said here because the system prompt does not exist any
	// earlier. Subagents say the same about themselves as they start, so
	// nothing is said here about agents this run may never spawn.
	preamble(out, client, schema, agentSetup{system: system, registry: registry}, set, sess.Dir())

	conv, err := sess.Resume(system, prompt)
	if err != nil {
		return err
	}

	// The system prompt is recorded because nothing else keeps it. A snapshot
	// deliberately stores only what was said, and a prompt is assembled from
	// how agi was invoked; without this line the log would describe a run that
	// could not be reproduced from it.
	lg.Record(eventlog.Event{Kind: eventlog.SystemPrompt, Content: system})
	if restored := conv.Len() - 1; restored > 0 {
		lg.Record(eventlog.Event{
			Kind:     eventlog.SessionResume,
			Session:  sess.Dir(),
			Messages: restored,
		})
	}

	ran := runAgent(ctx, out, lg, client, registry, conv, schema, exitCode, maxTurns, set)
	if ran != nil && !ended(ran) {
		return ran
	}

	if err := sess.Save(conv); err != nil {
		return err
	}
	lg.Record(eventlog.Event{
		Kind:     eventlog.SessionSave,
		Session:  sess.Dir(),
		Messages: conv.Len(),
	})
	// The conversation is stored either way; an interruption or a termination
	// is still what the run reports, since it produced no answer.
	return ran
}

// runAgent runs one prompt to an answer.
//
// It always ends the same way: the model talks, calls tools, and eventually
// answers in prose. A schema does not change that. The prose answer is taken
// as it comes and the model is then asked to restate it as JSON, rather than
// the shape being demanded from the start, because the reasoning that leads to
// an answer is worth having in the conversation and only some providers expose
// the hidden kind.
//
// The conversation comes in rather than being made here, because whether this
// is a first question or the next turn of a long one is the session's business
// and not the loop's. Either way the loop only sees turns to answer.
//
// --exit-code adds a third phase after both of those, and it changes what the
// run reports rather than what it delivers: the answer is printed either way,
// and the model's verdict on its own work decides the exit status. It is asked
// last, after the schema phase has taken its scaffolding back out, so that what
// the model is asked to judge is the answer it gave and not the reformatting of
// it.
//
// It takes a transport.Backend rather than a concrete gateway so a run can be
// exercised without a network. The backend is asked which model it is on,
// rather than the loop deciding: which model a run uses is settled by how that
// backend was configured, and nothing here needs a second opinion.
func runAgent(ctx context.Context, out output, lg *eventlog.Log, client transport.Backend, registry *tools.Registry, conv *model.Conversation, schema *model.Schema, exitCode bool, maxTurns int, set *hooks.Set) error {
	answer, err := converse(ctx, out, lg, client, registry, conv, maxTurns, set, eventlog.MainAgent)
	if err != nil {
		return err
	}

	// Everything the model wrote is shown through the display hooks, which may
	// hand back different text to print. Nothing else changes: the answer
	// below was stored, saved and recorded before this line was reached, so
	// what a hook rewrites is the printing and not the run.
	//
	// The turn is left at zero, because there is none. Both of these are
	// printed after the loop has ended, and a number would have to be either
	// the last turn — which is not when this happened — or invented.
	shown := hooked{out: out, log: lg, set: set, registry: registry, agent: eventlog.MainAgent}

	deliver := answer
	if schema != nil {
		// With a schema the prose answer is no longer the deliverable but the
		// reasoning behind it, so it joins the rest of the progress: a person
		// at a terminal sees the whole thing, a pipe carries the JSON alone.
		shown.say(ctx, answer)

		document, err := restate(ctx, shown, client, conv, schema)
		if err != nil {
			return err
		}
		deliver = document
	}

	if !exitCode {
		shown.answer(ctx, deliver)
		return nil
	}

	// The verdict is asked for before the answer is printed, so that at a
	// terminal the question scrolls past above the answer rather than after
	// it, which is where the schema phase's own progress goes.
	err = completed(ctx, shown, client, conv)
	// A run the user stopped printed nothing and answers nothing: the
	// interruption is what happened to it, and an answer underneath it would
	// claim the run finished. Every other way this returns leaves an answer
	// that was reached, and the exit status is what says not to trust it.
	if errors.Is(err, ErrInterrupted) {
		return err
	}
	shown.answer(ctx, deliver)
	return err
}

// converse drives the conversation to an answer: ask the model, run whatever
// tools it asks for, hand the results back, and repeat until it answers in
// prose. The conversation itself knows when the run is finished and what is
// outstanding, so this loop only has to do the work in between.
//
// Everything except the answer goes to out.step, which is dropped when stdout
// is not a terminal.
//
// The cap comes in rather than being read here, because how long a run may go
// on is settled by the command line and this loop is the last place that would
// know it. One cap is given to every agent of a run, the subagents included: a
// spawned run is an ordinary run, and a user who raised the cap because the
// work is long did not mean it for some of the agents doing it.
func converse(ctx context.Context, out output, lg *eventlog.Log, client transport.Backend, registry *tools.Registry, conv *model.Conversation, maxTurns int, set *hooks.Set, agent string) (string, error) {
	for turn := 1; turn <= maxTurns; turn++ {
		// Every point the loop could stop at is a point it checks. A run is
		// interrupted between one thing and the next as often as in the middle
		// of one, and a cancelled context that only surfaced as a failed
		// request would depend on there being a request left to fail.
		if interrupted(ctx) {
			return "", abandon(lg, conv, turn)
		}

		// The request is built before it is recorded, rather than inline in
		// the Send below, because the line and the call have to be about the
		// same request: recordRequest renders the body from what it is given,
		// and a second literal here would be a second request that only
		// happened to match.
		req := transport.Request{
			Conversation: conv,
			Tools:        registry.Definitions(),
		}
		recordRequest(lg, client, req, eventlog.Event{Turn: turn, Tools: registry.Names()})

		asked := time.Now()
		done := out.waiting("waiting for the model")
		reply, err := client.Send(ctx, req)
		done()
		if err != nil {
			lg.Record(eventlog.Event{
				Kind:    eventlog.ModelResponse,
				Turn:    turn,
				Reason:  err.Error(),
				Elapsed: eventlog.Since(asked),
			})
			// A request cut off midway fails with whatever the transport made
			// of the cancellation, which is not what happened: the user
			// stopped the run. The failure is recorded as it came back and
			// reported as what it was.
			if interrupted(ctx) {
				return "", abandon(lg, conv, turn)
			}
			return "", err
		}
		lg.Record(eventlog.Event{
			Kind:             eventlog.ModelResponse,
			Turn:             turn,
			Content:          reply.Message.Content,
			ReasoningContent: reply.Message.ReasoningContent,
			Requests:         loggedRequests(reply.Message.ToolRequests),
			Elapsed:          eventlog.Since(asked),
		})
		conv.AddAssistant(reply.Message)

		// The hooks of this turn: the ones around each call below, and the
		// ones over anything the model wrote that is about to be printed —
		// the deliberation on the next line included, which is why this is
		// built before the answer is looked for rather than after.
		guard := hooked{out: out, log: lg, set: set, registry: registry, agent: agent, turn: turn}

		// Above the turn it belongs to, and before anything else this turn
		// prints, which is what puts it above both things an assistant turn
		// can be: the prose written alongside tool calls, printed a few lines
		// below, and the final answer, printed by the caller once this loop
		// returns. One site covers both because the deliberation came back
		// with the turn either way, whatever the turn then turns out to be.
		guard.reason(ctx, reply.Message.ReasoningContent)

		if answer, done := conv.FinalAnswer(); done {
			answer = strings.TrimSpace(answer)
			if answer == "" {
				return "", errors.New("the model returned an empty answer")
			}
			lg.Record(eventlog.Event{Kind: eventlog.RunAnswer, Turn: turn, Content: answer})
			return answer, nil
		}

		// Prose alongside tool requests is the model thinking out loud. It is
		// markdown as much as the final answer is, so it goes through the
		// display hooks too — a renderer the user armed to read the answer
		// with is one they armed to read the run with.
		if thinking := strings.TrimSpace(reply.Message.Content); thinking != "" {
			guard.say(ctx, thinking)
		}

		// What the hooks asked to have put into the conversation, held until
		// every call of this turn has been answered. A harness instruction
		// between a request and its result would strand the requests made
		// alongside it, which model.Conversation.Validate refuses and a
		// provider would too, so the aside waits for the batch to close.
		var aside []string

		for _, req := range conv.PendingToolRequests() {
			// Stopping here is what leaves requests outstanding: the ones this
			// loop has not reached yet, and the one it was running when the
			// signal arrived. abandon answers all of them.
			if interrupted(ctx) {
				return "", abandon(lg, conv, turn)
			}

			out.step("%s\n", registry.Announce(req.Name, req.Arguments))
			lg.Record(eventlog.Event{
				Kind:      eventlog.ToolCall,
				Turn:      turn,
				Tool:      req.Name,
				RequestID: req.ID,
				Arguments: req.Arguments,
			})

			// The hooks see the call before it is made and may refuse it,
			// stop the agent, or hand back arguments of their own. The
			// request is a copy, so changing it changes what the tool is
			// handed and nothing else: the assistant turn above keeps the
			// call exactly as the model wrote it, which is the rule that the
			// record is never edited.
			arguments, context, err := guard.before(ctx, registry, req)
			if context != "" {
				aside = append(aside, context)
			}
			if stop, is := stopped(err); is {
				return "", halt(lg, conv, turn, stop)
			}
			if refusal, is := refused(err); is {
				// A refused call is a call that could not be made, which is
				// what a failure is for. The agent reads why and gets a turn
				// to explain itself or try another way.
				failure := conv.AddToolFailure(req.ID, refusal)
				out.step("%s\n\n", indent(failure.Render()))
				lg.Record(eventlog.Event{
					Kind:      eventlog.ToolFailure,
					Turn:      turn,
					Tool:      req.Name,
					Hook:      refusal.Hook,
					RequestID: req.ID,
					Reason:    failure.Reason,
				})
				continue
			}
			if err != nil {
				// Nothing else a hook can raise but the run being stopped
				// underneath it.
				return "", abandon(lg, conv, turn)
			}
			req.Arguments = arguments

			began := time.Now()
			done := out.waiting("running")
			answer, ran, err := registry.Answer(ctx, conv.Fork, req)
			done()
			if err != nil && interrupted(ctx) {
				// The tool did not refuse the call; the run was stopped
				// underneath it. Handing that back to the model as a
				// correctable mistake would be a lie, and there is no next
				// turn to correct it in.
				return "", abandon(lg, conv, turn)
			}
			if err != nil {
				// Report the failure to the model rather than aborting: it can
				// usually correct a malformed call on the next turn. The user
				// is shown the same words the model will read.
				failure := conv.AddToolFailure(req.ID, err)
				out.step("%s\n\n", indent(failure.Render()))
				lg.Record(eventlog.Event{
					Kind:      eventlog.ToolFailure,
					Turn:      turn,
					Tool:      req.Name,
					RequestID: req.ID,
					Reason:    failure.Reason,
					Elapsed:   eventlog.Since(began),
				})
				continue
			}

			// What the tool said, recorded before anything can replace it.
			// The line below holds what the model finally read, which is not
			// the same thing once a hook has rewritten it, and the arguments
			// have had both halves since hooks existed: tool.call keeps what
			// the model wrote and the hook's own line keeps what it left. This
			// is the output's missing half, and without it a PostToolUse hook
			// that replaced a command's output left no account anywhere of
			// what the command had actually printed.
			//
			// It carries no elapsed time. The call is timed on the line that
			// closes it, and a second duration here would be the tool's own
			// where that one is the tool's and its hooks' — two numbers a
			// reader would have to be told apart, for a breakdown each hook's
			// own line already gives.
			lg.Record(eventlog.Event{
				Kind:      eventlog.ToolOutput,
				Turn:      turn,
				Tool:      req.Name,
				RequestID: req.ID,
				Content:   answer.Render(),
			})

			// The hooks see the result before the model does, and may replace
			// it. That happens before the turn is appended, so what is stored
			// and what is logged are what the model actually reads: a
			// conversation holding a result nobody was shown would be a
			// record of a run that did not happen.
			replaced, context, err := guard.after(ctx, req, answer.Render(), ran, time.Since(began))
			if context != "" {
				aside = append(aside, context)
			}
			if replaced != answer.Render() {
				// A rewritten answer is a plain result, whatever the tool's
				// own answer was. A structured one — the sections a subagents
				// call comes back as — cannot survive being replaced by text,
				// and flattening it is the honest consequence of a hook
				// asking for the output to be replaced.
				answer = model.NewToolResult(req.ID, replaced)
			}

			conv.Append(answer)
			// What is logged is what the model reads, assembled the same way
			// it is for the gateway, so the log does not have a second opinion
			// about what the tool said.
			lg.Record(eventlog.Event{
				Kind:      eventlog.ToolResult,
				Turn:      turn,
				Tool:      req.Name,
				RequestID: req.ID,
				Content:   answer.Render(),
				Elapsed:   eventlog.Since(began),
			})
			if stop, is := stopped(err); is {
				// The tool ran and the model has been given what it produced,
				// which is what a post-call hook stopping the agent means:
				// the result stands and the run does not go on from it.
				return "", halt(lg, conv, turn, stop)
			}
			if err != nil {
				return "", abandon(lg, conv, turn)
			}
			// What a call produced, for the tools that have a line to say
			// about it. Under --verbose the whole result is printed below, so
			// the report is what stands in for it the rest of the time and
			// would only be saying it twice.
			if line := registry.Report(req.Name, answer.Render()); line != "" && !out.verbose {
				out.step("%s\n", line)
			}
			if out.verbose {
				out.step("%s\n", indent(answer.Render()))
			}
			out.step("\n")
		}

		// A hook that asked to say something to the model says it now, with
		// every call of the turn answered. It is a harness instruction
		// because that is what it is: agi speaking in its own voice, relaying
		// what a program the user wrote had to add. Several hooks each get
		// their own paragraph.
		if len(aside) > 0 {
			instruct(lg, conv, strings.Join(aside, "\n\n"))
		}
	}

	return "", fmt.Errorf("gave up after %d turns without a final answer", maxTurns)
}

// recordRequest writes the model.request line for one exchange, whichever of
// the three phases is asking: the agent loop, restating an answer as JSON, or
// asking whether the task was completed.
//
// It is one function because all three say the same things about a request and
// none of them knows anything the others do not. What used to be three
// assembled lines drifted the moment a field was added — a backend named on
// one of them and not the others is a log whose answer to "where did this go"
// depends on which phase was running — and the fields that say where a request
// went are now four. The phases differ in how they number an exchange, which
// is why that part comes in: the loop counts turns and the other two count
// attempts at a document.
//
// The body is rendered here and only when the log wants one, from the same
// request that is about to be sent. Backend.Payload is what Send itself
// encodes with, so what lands on the line is the bytes that go out rather than
// a second guess at them.
//
// A body that cannot be rendered is left off the line, and nothing is said
// about it here. Send is handed the same request a moment later and fails on
// the same refusal, and the reason reaches the log on the model.response line
// that answers this one — so the failure is recorded once, in the place that
// records failures, rather than twice in different words. A run is never
// stopped by this: the body is a debugging aid, and a request worth making
// does not become unmakeable because the record of it could not be written.
func recordRequest(lg *eventlog.Log, client transport.Backend, req transport.Request, e eventlog.Event) {
	e.Kind = eventlog.ModelRequest
	e.Backend = client.Name()
	e.Model = client.Model()
	e.Gateway = client.BaseURL()
	e.Messages = req.Conversation.Len()
	if lg.RecordsRequestBody() {
		if body, err := client.Payload(req); err == nil {
			e.RequestBody = body
		}
	}
	lg.Record(e)
}

// loggedRequests renders the tool calls an assistant turn asked for in the
// log's own shape, rather than handing the domain type to the encoder: the
// format is the log's to decide, and a field added to a message type should
// not silently change it.
func loggedRequests(reqs []model.ToolRequest) []eventlog.LoggedRequest {
	if len(reqs) == 0 {
		return nil
	}
	out := make([]eventlog.LoggedRequest, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, eventlog.LoggedRequest{ID: r.ID, Name: r.Name, Arguments: r.Arguments})
	}
	return out
}

// instruct appends an instruction from agi to the conversation and records it.
//
// The two are one function so that neither can happen without the other. An
// instruction is agi steering the run in its own voice, and a run steered by
// words nothing kept is a run that cannot be read back — which matters most
// for the instruction a fork is given, since that is the whole of what the
// copy was told to do.
//
// It does not cover Conversation.Fork, which appends its instruction itself as
// part of closing out the call that asked for the copy. That one is recorded
// beside it, into the fork's own log, so the line lands under the fork's name
// where a reader of that fork will be looking.
func instruct(lg *eventlog.Log, conv *model.Conversation, content string) {
	conv.AddHarnessInstruction(content)
	lg.Record(eventlog.Event{Kind: eventlog.HarnessInstruction, Content: content})
}

// restate asks the model to say the answer it has already given again as a
// JSON object matching the schema, and keeps asking, with each rejection
// explained, until it does or the attempts run out. Failing that, the run
// fails: a wrongly shaped document is worse than no document for whatever is
// reading the other end of the pipe.
//
// Nothing it appends survives. The instruction, every attempt, and the
// accepted JSON itself are all scaffolding, dropped before returning so that a
// run later resumed from this conversation finds the prose answer as the last
// word and no trace of the reformatting that followed it.
// It is handed the agent's hooked rather than an output and a log, because
// the phase prints something the model wrote: an attempt is an assistant turn
// and the deliberation behind it is shown above it, through whatever display
// hook the run armed over thinking. Two of the three things it needs were
// already travelling together in that value, and the third is the one that
// had been missing.
func restate(ctx context.Context, shown hooked, client transport.Backend, conv *model.Conversation, schema *model.Schema) (string, error) {
	out, lg := shown.out, shown.log
	mark := conv.Len()
	defer func() {
		// Removing turns from the conversation is itself an event. They will
		// not be in the snapshot — that is the point of dropping them — so
		// this line is the only place the run accounts for them, and it is
		// what makes the log the record the snapshot cannot be.
		lg.Record(eventlog.Event{
			Kind:    eventlog.Drop,
			From:    mark,
			Dropped: conv.Len() - mark,
		})
		// The mark is where the instruction below goes, so the dropped tail
		// always starts at a harness instruction and this cannot fail.
		_ = conv.DropFrom(mark)
	}()

	// The shape asked for is this line's own; the words the model was told it
	// in are the instruction's, and instruct records those.
	lg.Record(eventlog.Event{Kind: eventlog.SchemaRestate, Schema: schema.String()})
	instruct(lg, conv, schemaInstruction(schema))

	// The reason the last attempt was rejected, reported to the model and, if
	// the attempts run out, to the user.
	var rejected error

	for attempt := 1; attempt <= maxSchemaAttempts; attempt++ {
		// The phase is scaffolding and the deferred drop above takes it back
		// out whichever way this returns, so an interrupted schema run stores
		// the prose answer the loop ended on and nothing of the reformatting.
		if interrupted(ctx) {
			return "", abandon(lg, conv, 0)
		}

		out.step("%s %s\n", tools.Name("schema"),
			tools.Detail(fmt.Sprintf("restating the answer as JSON (attempt %d of %d)", attempt, maxSchemaAttempts)))

		// Every exchange with the gateway is recorded, this one included: a
		// log that skipped the restatement would describe a run that could
		// not be replayed from it.
		req := transport.Request{
			Conversation: conv,
			// No tools: the work is done, and the only thing left to do is say
			// the answer again in the right shape.
		}
		recordRequest(lg, client, req, eventlog.Event{Attempt: attempt})

		asked := time.Now()
		done := shown.out.waiting("waiting for the model")
		reply, err := client.Send(ctx, req)
		done()
		if err != nil {
			lg.Record(eventlog.Event{
				Kind:    eventlog.ModelResponse,
				Attempt: attempt,
				Reason:  err.Error(),
				Elapsed: eventlog.Since(asked),
			})
			if interrupted(ctx) {
				return "", abandon(lg, conv, 0)
			}
			return "", err
		}
		lg.Record(eventlog.Event{
			Kind:             eventlog.ModelResponse,
			Attempt:          attempt,
			Content:          reply.Message.Content,
			ReasoningContent: reply.Message.ReasoningContent,
			Elapsed:          eventlog.Since(asked),
		})

		// An attempt is an assistant turn like any other, so whatever thinking
		// went into it is shown above it.
		shown.reason(ctx, reply.Message.ReasoningContent)

		// Only the prose is kept. A turn that asks for a tool has misread the
		// instruction, and there is no tool on offer to answer it with.
		document := strings.TrimSpace(reply.Message.Content)
		lg.Record(eventlog.Event{Kind: eventlog.SchemaAttempt, Attempt: attempt, Content: document})
		if document == "" {
			rejected = errors.New("the reply contained no JSON at all")
		} else {
			conv.AddAssistant(model.NewAssistantMessage(document))
			rejected = schema.ValidateJSON(document)
			if rejected == nil {
				lg.Record(eventlog.Event{
					Kind:    eventlog.SchemaAccepted,
					Attempt: attempt,
					Content: document,
				})
				return document, nil
			}
		}

		lg.Record(eventlog.Event{
			Kind:    eventlog.SchemaRejected,
			Attempt: attempt,
			Reason:  describeRejection(rejected),
		})
		out.step("%s\n", indent(describeRejection(rejected)))
		instruct(lg, conv, correctionInstruction(rejected))
	}

	return "", fmt.Errorf("the model did not produce JSON matching the schema in %d attempts; last rejection: %w",
		maxSchemaAttempts, rejected)
}

// schemaInstruction is what the model is told once the prose answer is in. It
// is explicit about the habits models have anyway — a sentence of preamble, a
// markdown fence around the document — because each one costs an attempt.
//
// The two shapes are spelled out separately rather than as an object with a
// line about arrays added. Which one is wanted decides the first and the last
// thing the model reads, and one told to produce an object and then told to
// wrap it is one that sometimes returns the object alone.
func schemaInstruction(schema *model.Schema) string {
	const types = `A string field holds a JSON string; a bool field holds true or false; an int field holds a whole number, written without a decimal point; a decimal field holds a number that may have a fractional part.`

	if schema.List {
		return fmt.Sprintf(`Restate the answer above as a single JSON array of objects.

Every object in the array must have exactly these fields:
%s

%s Every field is required in every object, and any field beyond the ones listed is an error. No field holds an array or an object of its own: the array of objects is the whole of the nesting.

Put one object in the array for each thing the answer covers, in the order the answer gives them. Write [] if it covers none.

Output the raw JSON array and nothing else: no prose before or after it, no explanation, and no markdown code fence.`, schema.Describe(), types)
	}

	return fmt.Sprintf(`Restate the answer above as a single JSON object.

The object must have exactly these fields:
%s

%s Every field is required, and any field beyond the ones listed is an error. No field holds an array or an object of its own.

Output the raw JSON object and nothing else: no prose before or after it, no explanation, and no markdown code fence.`, schema.Describe(), types)
}

// correctionInstruction hands a rejected attempt back to the model. Saying
// what was wrong is the whole point of spending another attempt.
func correctionInstruction(rejected error) string {
	return fmt.Sprintf(`That was rejected:
%s

Output a corrected JSON object that fixes every one of those, and nothing else.`, describeRejection(rejected))
}

// describeRejection words a rejection for the model. A ValidationError carries
// every problem separately, so they are listed rather than run together; the
// package prefix a Go error carries is agi's own namespace and means nothing
// to the model, so it goes.
func describeRejection(rejected error) string {
	var invalid *model.ValidationError
	if errors.As(rejected, &invalid) {
		return "- " + strings.Join(invalid.Problems, "\n- ")
	}
	return "- " + strings.TrimPrefix(rejected.Error(), "model: ")
}

// indent prefixes every line and dims it, so tool output is visibly
// subordinate to the call that produced it.
func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = tools.Detail("  | " + l)
	}
	return strings.Join(lines, "\n")
}
