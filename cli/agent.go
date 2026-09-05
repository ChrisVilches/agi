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
	"strings"
	"sync"
	"time"

	"github.com/ChrisVilches/agi/eventlog"
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

// terminate ends a run the model asked to end, leaving the conversation in a
// state a later run can continue from.
//
// The call that ended the run is answered first, with what became of it. The
// model never reads that in this run — there is no next turn — but a session
// resumed from the snapshot sends it, and a model that saw itself ask for a
// tool and never saw an answer would be owed one. It is an ordinary result
// rather than a failure: the tool did exactly what it was asked.
//
// Whatever the model asked for alongside it is abandoned, the way an interrupt
// abandons what it did not reach. Those calls were never made, and a stored
// conversation that moves on from an outstanding request is one a provider may
// refuse.
//
// Nothing is printed here. The reason is the whole of what the user gets and it
// belongs on standard error, which is where the termination ends up once it has
// been reported as the run's error — not in the progress stream, which a piped
// run discards.
func terminate(lg *eventlog.Log, conv *model.Conversation, turn int, req model.ToolRequest, end *native.Termination) error {
	conv.AddToolResult(req.ID, end.Result())
	lg.Record(eventlog.Event{
		Kind:      eventlog.RunTerminate,
		Turn:      turn,
		Tool:      req.Name,
		RequestID: req.ID,
		Reason:    end.Error(),
		Messages:  conv.Len(),
	})

	for _, f := range conv.AbandonToolRequests(errTerminated) {
		lg.Record(eventlog.Event{
			Kind:      eventlog.ToolFailure,
			Turn:      turn,
			RequestID: f.RequestID,
			Reason:    f.Reason,
		})
	}
	return end
}

// errTerminated answers the calls the model asked for alongside the one that
// ended the run. They were never made, which is what a failure is for, and the
// reason says why rather than leaving the model to infer it from a gap.
var errTerminated = errors.New("the run was ended before this call was made")

// ended reports whether err left a conversation worth storing.
//
// A run that answered is the ordinary case. The other two are a run the user
// stopped and a run the model ended: both got as far as they got, everything
// said was said, and nothing about them is in doubt. An ordinary failure is —
// a gateway that did not answer, a reply agi could not make sense of — and
// storing a conversation it cannot vouch for would be storing a guess.
func ended(err error) bool {
	if errors.Is(err, ErrInterrupted) {
		return true
	}
	var end *native.Termination
	return errors.As(err, &end)
}

// workspaceRegistry returns the tools that do ordinary work on the machine:
// the ones agi ships, and the ones the user brought with --tools. This is the
// one list of them: an ordinary run and a subagent both start from it, so a
// tool added here reaches both and the two cannot drift apart.
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
func workspaceRegistry(backups, agent string, user []tools.Tool) (*tools.Registry, error) {
	r := tools.NewRegistry()
	if err := r.Register(
		workspace.NewBash(),
		workspace.NewMkTemp(),
		workspace.NewPatchFile(backups, agent),
		workspace.NewWriteFile(),
	); err != nil {
		return nil, err
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
// manifest that works until the day somebody adds --skills to the command.
func providedNames() (map[string]bool, error) {
	r, err := defaultRegistry(quiet(), nil, nil, "", nil, []skills.Skill{{Name: "reserved"}}, defaultMaxTurns)
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
func defaultRegistry(out output, client transport.Backend, lg *eventlog.Log, backups string, user []tools.Tool, found []skills.Skill, maxTurns int) (*tools.Registry, error) {
	r, err := workspaceRegistry(backups, eventlog.MainAgent, user)
	if err != nil {
		return nil, err
	}
	if err := r.Register(
		native.NewSubagents(subagentRunner(out, client, lg, backups, user, r, maxTurns), found),
		native.NewTerminate(),
	); err != nil {
		return nil, err
	}
	if len(found) > 0 {
		if err := r.Register(native.NewLoadSkill(found)); err != nil {
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
func subagentRunner(out output, client transport.Backend, lg *eventlog.Log, backups string, user []tools.Tool, run *tools.Registry, maxTurns int) native.Runner {
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

		registry, err := workspaceRegistry(backups, name, user)
		if err != nil {
			return "", err
		}
		if len(brief.Skills) > 0 {
			if err := registry.Register(native.NewLoadSkill(brief.Skills)); err != nil {
				return "", err
			}
		}

		sub := lg.Agent(name)
		started := time.Now()
		sub.Record(eventlog.Event{
			Kind:    eventlog.SubagentStart,
			Title:   brief.Title,
			Content: brief.Body,
			Tools:   registry.Names(),
		})

		system := subagentSystemPrompt(registry, brief.Skills)
		if out.verbose {
			intro := subagentIntro(name, brief.Title,
				agentSetup{system: system, registry: registry}, explainedBy(run))
			screen.Lock()
			out.step("%s", intro)
			screen.Unlock()
		}

		conv := model.NewConversation(system, brief.Body)
		sub.Record(eventlog.Event{Kind: eventlog.SystemPrompt, Content: conv.System})

		answer, err := converse(ctx, quiet(), sub, client, registry, conv, maxTurns)
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
// schema means the answer is whatever prose the model ends on.
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
func runPrompt(ctx context.Context, out output, base, prompt string, schema *model.Schema, store sessionFlags, user []tools.Tool, found []skills.Skill, maxTurns int) error {
	backend, err := openBackend()
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

	registry, err := defaultRegistry(out, backend, lg, sess.BackupDir(), user, found, maxTurns)
	if err != nil {
		return err
	}

	// Where the run is being kept is how the user resumes it, so it is worth
	// saying, quietly, before anything else scrolls past.
	out.step("%s\n\n", tools.Detail("session "+sess.Dir()))

	started := time.Now()
	lg.Record(eventlog.Event{
		Kind:    eventlog.RunStart,
		Model:   backend.Model(),
		Gateway: backend.BaseURL(),
		Tools:   registry.Names(),
		Session: sess.Dir(),
		Schema:  describeSchema(schema),
		Content: prompt,
	})

	err = runSession(ctx, out, lg, backend, registry, sess, base, prompt, schema, found, maxTurns)

	finish := eventlog.Event{Kind: eventlog.RunFinish, Elapsed: eventlog.Since(started)}
	if err != nil {
		finish.Reason = err.Error()
	}
	lg.Record(finish)
	return err
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
func runSession(ctx context.Context, out output, lg *eventlog.Log, client transport.Backend, registry *tools.Registry, sess *session.Session, base, prompt string, schema *model.Schema, found []skills.Skill, maxTurns int) error {
	system := systemPromptFor(base, registry, skillsSection(found))
	// A verbose run opens by saying what it is about to do the work with: the
	// model, the prompt that will be read, and what each tool was described
	// as. It is said here because the system prompt does not exist any
	// earlier. Subagents say the same about themselves as they start, so
	// nothing is said here about agents this run may never spawn.
	preamble(out, client, schema, agentSetup{system: system, registry: registry})

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

	ran := runAgent(ctx, out, lg, client, registry, conv, schema, maxTurns)
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
// It takes a transport.Backend rather than a concrete gateway so a run can be
// exercised without a network. The backend is asked which model it is on,
// rather than the loop deciding: which model a run uses is settled by how that
// backend was configured, and nothing here needs a second opinion.
func runAgent(ctx context.Context, out output, lg *eventlog.Log, client transport.Backend, registry *tools.Registry, conv *model.Conversation, schema *model.Schema, maxTurns int) error {
	answer, err := converse(ctx, out, lg, client, registry, conv, maxTurns)
	if err != nil {
		return err
	}

	if schema == nil {
		out.final(answer)
		return nil
	}

	// With a schema the prose answer is no longer the deliverable but the
	// reasoning behind it, so it joins the rest of the progress: a person at a
	// terminal sees the whole thing, a pipe carries the JSON alone.
	out.step("%s\n\n", answer)

	document, err := restate(ctx, out, lg, client, conv, schema)
	if err != nil {
		return err
	}
	out.final(document)
	return nil
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
func converse(ctx context.Context, out output, lg *eventlog.Log, client transport.Backend, registry *tools.Registry, conv *model.Conversation, maxTurns int) (string, error) {
	for turn := 1; turn <= maxTurns; turn++ {
		// Every point the loop could stop at is a point it checks. A run is
		// interrupted between one thing and the next as often as in the middle
		// of one, and a cancelled context that only surfaced as a failed
		// request would depend on there being a request left to fail.
		if interrupted(ctx) {
			return "", abandon(lg, conv, turn)
		}

		lg.Record(eventlog.Event{
			Kind:     eventlog.Request,
			Turn:     turn,
			Model:    client.Model(),
			Messages: conv.Len(),
			Tools:    registry.Names(),
		})

		asked := time.Now()
		reply, err := client.Send(ctx, transport.Request{
			Conversation: conv,
			Tools:        registry.Definitions(),
		})
		if err != nil {
			lg.Record(eventlog.Event{
				Kind:    eventlog.Response,
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
			Kind:     eventlog.Response,
			Turn:     turn,
			Content:  reply.Message.Content,
			Requests: loggedRequests(reply.Message.ToolRequests),
			Elapsed:  eventlog.Since(asked),
		})
		conv.AddAssistant(reply.Message)

		if answer, done := conv.FinalAnswer(); done {
			answer = strings.TrimSpace(answer)
			if answer == "" {
				return "", errors.New("the model returned an empty answer")
			}
			lg.Record(eventlog.Event{Kind: eventlog.Answer, Turn: turn, Content: answer})
			return answer, nil
		}

		// Prose alongside tool requests is the model thinking out loud.
		if thinking := strings.TrimSpace(reply.Message.Content); thinking != "" {
			out.step("%s\n\n", thinking)
		}

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

			ran := time.Now()
			answer, err := registry.Answer(ctx, req)
			// The model asking to end the run is not a call that failed, so it
			// is not reported back as one: there is no next turn to read it.
			var end *native.Termination
			if errors.As(err, &end) {
				return "", terminate(lg, conv, turn, req, end)
			}
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
					Elapsed:   eventlog.Since(ran),
				})
				continue
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
				Elapsed:   eventlog.Since(ran),
			})
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
	}

	return "", fmt.Errorf("gave up after %d turns without a final answer", maxTurns)
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
func restate(ctx context.Context, out output, lg *eventlog.Log, client transport.Backend, conv *model.Conversation, schema *model.Schema) (string, error) {
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

	instruction := schemaInstruction(schema)
	conv.AddHarnessInstruction(instruction)
	lg.Record(eventlog.Event{
		Kind:    eventlog.SchemaRestate,
		Schema:  schema.String(),
		Content: instruction,
	})

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
		lg.Record(eventlog.Event{
			Kind:     eventlog.Request,
			Attempt:  attempt,
			Model:    client.Model(),
			Messages: conv.Len(),
		})

		asked := time.Now()
		reply, err := client.Send(ctx, transport.Request{
			Conversation: conv,
			// No tools: the work is done, and the only thing left to do is say
			// the answer again in the right shape.
		})
		if err != nil {
			lg.Record(eventlog.Event{
				Kind:    eventlog.Response,
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
			Kind:    eventlog.Response,
			Attempt: attempt,
			Content: reply.Message.Content,
			Elapsed: eventlog.Since(asked),
		})

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
		conv.AddHarnessInstruction(correctionInstruction(rejected))
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
