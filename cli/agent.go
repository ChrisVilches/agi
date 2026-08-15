package cli

// TODO: this package imports too much. The agent loop, the tool registry, the
// session, the gateway, and the prompts all converge here, so cli is where the
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
	"os"
	"strings"
	"time"

	"github.com/ChrisVilches/agi/eventlog"
	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/prompt"
	"github.com/ChrisVilches/agi/session"
	"github.com/ChrisVilches/agi/tools"
	"github.com/ChrisVilches/agi/tools/controlflow"
	"github.com/ChrisVilches/agi/tools/workspace"
	"github.com/ChrisVilches/agi/transport"
	"github.com/ChrisVilches/agi/transport/litellm"
)

const (
	// EnvModel overrides the model the agent runs on.
	EnvModel = "AGI_MODEL"
	// DefaultModel is used when EnvModel is unset.
	DefaultModel = "deepseek-chat"
	// maxTurns bounds one run, so a loop of tool calls cannot run forever.
	maxTurns = 20
	// maxSchemaAttempts bounds how many times the model is asked to restate
	// its answer as JSON before the run gives up.
	//
	// TODO: parameterize this instead of hardcoding it.
	maxSchemaAttempts = 10
)

// modelName returns the model to run on.
func modelName() string {
	if v := strings.TrimSpace(os.Getenv(EnvModel)); v != "" {
		return v
	}
	return DefaultModel
}

// workspaceRegistry returns the tools that do ordinary work on the machine.
// This is the one list of them: an ordinary run and a subagent both start from
// it, so a tool added here reaches both and the two cannot drift apart.
func workspaceRegistry() (*tools.Registry, error) {
	r := tools.NewRegistry()
	if err := r.Register(workspace.NewBash()); err != nil {
		return nil, err
	}
	return r, nil
}

// defaultRegistry returns the tools available to a run: the work tools, and
// the control-flow tools that act on the run itself.
//
// The log is passed in because a subagent records its own work under its own
// name in it, and the subagents tool is where subagents come from.
func defaultRegistry(client transport.Client, lg *eventlog.Log) (*tools.Registry, error) {
	r, err := workspaceRegistry()
	if err != nil {
		return nil, err
	}
	if err := r.Register(controlflow.NewSubagents(subagentRunner(client, lg))); err != nil {
		return nil, err
	}
	return r, nil
}

// subagentRunner returns the function the subagents tool spawns one with.
//
// A subagent is an ordinary run: the same gateway, the same loop, a
// conversation that starts from a prompt. Two things differ. It runs under the
// system prompt written for it rather than the ordinary base, and it is given
// the work tools without the control-flow ones — which is the whole of what
// stops a subagent spawning subagents. Nothing in its prompt forbids it; it
// simply has no such tool, the same way it has no tool nobody registered.
//
// Its conversation ends with it: only the final answer goes back to the agent
// that asked, and nothing is stored. Its progress is discarded too, since
// several subagents run at once and their steps would interleave into
// nonsense; what the user sees is the list of titles the tool announces before
// they start.
//
// The log is the exception to that silence, and it is why the two channels are
// separate. Interleaving is a problem for a display, which a person reads top
// to bottom; it is not a problem for a log, where every line says which agent
// wrote it. So a subagent records everything an ordinary run records, under a
// name of its own, and a run that fanned out can still be read back in full.
func subagentRunner(client transport.Client, lg *eventlog.Log) controlflow.Runner {
	return func(ctx context.Context, brief controlflow.Brief) (string, error) {
		registry, err := workspaceRegistry()
		if err != nil {
			return "", err
		}

		sub := lg.Agent(subagentName(brief))
		started := time.Now()
		sub.Record(eventlog.Event{
			Kind:    eventlog.SubagentStart,
			Title:   brief.Title,
			Content: brief.Body,
			Tools:   registry.Names(),
		})

		conv := model.NewConversation(subagentSystemPrompt(registry), brief.Body)
		sub.Record(eventlog.Event{Kind: eventlog.SystemPrompt, Content: conv.System})

		answer, err := converse(ctx, quiet(), sub, client, registry, conv)
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
func subagentName(brief controlflow.Brief) string {
	return fmt.Sprintf("subagent-%d", brief.Number)
}

// systemPrompt assembles the system prompt for an ordinary run.
func systemPrompt(r *tools.Registry) string {
	return systemPromptFor(prompt.Base(), r)
}

// subagentSystemPrompt assembles the system prompt for a spawned agent, which
// runs under an instruction of its own: its answer is final and read by a
// machine, not the start of a conversation with a person.
func subagentSystemPrompt(r *tools.Registry) string {
	return systemPromptFor(prompt.Subagent(), r)
}

// systemPromptFor puts a base instruction together with whatever the
// registered tools insist on explaining. The base is the swappable half;
// control-flow tools append to it whatever base is in use, because a model
// that has not been told what they are for will never reach for them.
// Workspace and user-defined tools contribute nothing here by design.
//
// It is assembled on every run and never stored, so how agi was invoked is the
// only thing that decides it. The rule that makes that work: the same
// arguments and the same files always produce the same prompt. Nothing here
// may depend on the time, the run, or anything else that varies on its own.
func systemPromptFor(base string, r *tools.Registry) string {
	parts := []string{base}
	if guidance := r.SystemPrompt(); guidance != "" {
		parts = append(parts, guidance)
	}
	return strings.Join(parts, "\n\n")
}

// runPrompt wires up a gateway, somewhere to store the run, a log, and tools,
// then runs the agent on prompt. A nil schema means the answer is whatever
// prose the model ends on.
//
// The order matters twice. The gateway is settled first, so a run that cannot
// happen at all does not leave a directory behind. The session comes before
// the log because the log lives in the session's directory, and both come
// before the tools because a subagent records under the same log as the run
// that spawned it.
func runPrompt(ctx context.Context, out output, prompt string, schema *model.Schema, store sessionFlags) error {
	gateway, err := litellm.FromEnv()
	if err != nil {
		if errors.Is(err, litellm.ErrNotConfigured) {
			return fmt.Errorf("no gateway configured: set %s to the gateway's base URL", litellm.EnvURL)
		}
		return err
	}

	sess, err := store.open()
	if err != nil {
		return err
	}

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

	registry, err := defaultRegistry(gateway, lg)
	if err != nil {
		return err
	}

	if out.verbose {
		out.step("model %s via %s, %d tool(s)\n\n", modelName(), gateway.BaseURL(), registry.Len())
		if schema != nil {
			out.step("schema %s\n\n", schema)
		}
	}
	// Where the run is being kept is how the user resumes it, so it is worth
	// saying, quietly, before anything else scrolls past.
	if sess.Stored() {
		out.step("%s\n\n", tools.Detail("session "+sess.Dir()))
	}

	started := time.Now()
	lg.Record(eventlog.Event{
		Kind:    eventlog.RunStart,
		Model:   modelName(),
		Gateway: gateway.BaseURL(),
		Tools:   registry.Names(),
		Session: sess.Dir(),
		Schema:  describeSchema(schema),
		Content: prompt,
	})

	err = runSession(ctx, out, lg, gateway, registry, sess, prompt, schema)

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
func runSession(ctx context.Context, out output, lg *eventlog.Log, client transport.Client, registry *tools.Registry, sess *session.Session, prompt string, schema *model.Schema) error {
	system := systemPrompt(registry)
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

	if err := runAgent(ctx, out, lg, client, registry, conv, schema); err != nil {
		return err
	}

	if err := sess.Save(conv); err != nil {
		return err
	}
	if sess.Stored() {
		lg.Record(eventlog.Event{
			Kind:     eventlog.SessionSave,
			Session:  sess.Dir(),
			Messages: conv.Len(),
		})
	}
	return nil
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
// It takes a transport.Client rather than a concrete gateway so a run can be
// exercised without a network.
func runAgent(ctx context.Context, out output, lg *eventlog.Log, client transport.Client, registry *tools.Registry, conv *model.Conversation, schema *model.Schema) error {
	answer, err := converse(ctx, out, lg, client, registry, conv)
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
func converse(ctx context.Context, out output, lg *eventlog.Log, client transport.Client, registry *tools.Registry, conv *model.Conversation) (string, error) {
	for turn := 1; turn <= maxTurns; turn++ {
		lg.Record(eventlog.Event{
			Kind:     eventlog.Request,
			Turn:     turn,
			Model:    modelName(),
			Messages: conv.Len(),
			Tools:    registry.Names(),
		})

		asked := time.Now()
		reply, err := client.Send(ctx, transport.Request{
			Model:        modelName(),
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
func restate(ctx context.Context, out output, lg *eventlog.Log, client transport.Client, conv *model.Conversation, schema *model.Schema) (string, error) {
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
		out.step("%s %s\n", tools.Name("schema"),
			tools.Detail(fmt.Sprintf("restating the answer as JSON (attempt %d of %d)", attempt, maxSchemaAttempts)))

		// Every exchange with the gateway is recorded, this one included: a
		// log that skipped the restatement would describe a run that could
		// not be replayed from it.
		lg.Record(eventlog.Event{
			Kind:     eventlog.Request,
			Attempt:  attempt,
			Model:    modelName(),
			Messages: conv.Len(),
		})

		asked := time.Now()
		reply, err := client.Send(ctx, transport.Request{
			Model:        modelName(),
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
// markdown fence around the object — because each one costs an attempt.
func schemaInstruction(schema *model.Schema) string {
	return fmt.Sprintf(`Restate the answer above as a single JSON object.

The object must have exactly these fields:
%s

A string field holds a JSON string; a bool field holds true or false; an int field holds a whole number, written without a decimal point; a decimal field holds a number that may have a fractional part. Every field is required, and any field beyond the ones listed is an error.

Output the raw JSON object and nothing else: no prose before or after it, no explanation, and no markdown code fence.`, schema.Describe())
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
