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
func defaultRegistry(client transport.Client) (*tools.Registry, error) {
	r, err := workspaceRegistry()
	if err != nil {
		return nil, err
	}
	if err := r.Register(controlflow.NewSubagents(subagentRunner(client))); err != nil {
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
func subagentRunner(client transport.Client) controlflow.Runner {
	return func(ctx context.Context, task string) (string, error) {
		registry, err := workspaceRegistry()
		if err != nil {
			return "", err
		}
		conv := model.NewConversation(subagentSystemPrompt(registry), task)
		return converse(ctx, quiet(), client, registry, conv)
	}
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
func systemPromptFor(base string, r *tools.Registry) string {
	parts := []string{base}
	if guidance := r.SystemPrompt(); guidance != "" {
		parts = append(parts, guidance)
	}
	return strings.Join(parts, "\n\n")
}

// runPrompt wires up a gateway, tools, and somewhere to store the run, then
// runs the agent on prompt. A nil schema means the answer is whatever prose
// the model ends on.
//
// The gateway is settled before the session, so a run that cannot happen at
// all does not leave a directory behind.
func runPrompt(ctx context.Context, out output, prompt string, schema *model.Schema, store sessionFlags) error {
	gateway, err := litellm.FromEnv()
	if err != nil {
		if errors.Is(err, litellm.ErrNotConfigured) {
			return fmt.Errorf("no gateway configured: set %s to the gateway's base URL", litellm.EnvURL)
		}
		return err
	}

	registry, err := defaultRegistry(gateway)
	if err != nil {
		return err
	}

	sess, err := store.open()
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

	return runSession(ctx, out, gateway, registry, sess, prompt, schema)
}

// runSession runs one prompt against a session: pick the conversation up where
// the last run left it, run, and store what it ends as.
//
// The snapshot is written only when the run finished. A run that failed partway
// leaves the session as it was, so resuming continues from the last complete
// exchange rather than from a question that was never answered.
func runSession(ctx context.Context, out output, client transport.Client, registry *tools.Registry, sess *session.Session, prompt string, schema *model.Schema) error {
	conv, err := sess.Resume(systemPrompt(registry), prompt)
	if err != nil {
		return err
	}

	if err := runAgent(ctx, out, client, registry, conv, schema); err != nil {
		return err
	}
	return sess.Save(conv)
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
func runAgent(ctx context.Context, out output, client transport.Client, registry *tools.Registry, conv *model.Conversation, schema *model.Schema) error {
	answer, err := converse(ctx, out, client, registry, conv)
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

	document, err := restate(ctx, out, client, conv, schema)
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
func converse(ctx context.Context, out output, client transport.Client, registry *tools.Registry, conv *model.Conversation) (string, error) {
	for turn := 0; turn < maxTurns; turn++ {
		reply, err := client.Send(ctx, transport.Request{
			Model:        modelName(),
			Conversation: conv,
			Tools:        registry.Definitions(),
		})
		if err != nil {
			return "", err
		}
		conv.AddAssistant(reply.Message)

		if answer, done := conv.FinalAnswer(); done {
			answer = strings.TrimSpace(answer)
			if answer == "" {
				return "", errors.New("the model returned an empty answer")
			}
			return answer, nil
		}

		// Prose alongside tool requests is the model thinking out loud.
		if thinking := strings.TrimSpace(reply.Message.Content); thinking != "" {
			out.step("%s\n\n", thinking)
		}

		for _, req := range conv.PendingToolRequests() {
			out.step("%s\n", registry.Announce(req.Name, req.Arguments))

			answer, err := registry.Answer(ctx, req)
			if err != nil {
				// Report the failure to the model rather than aborting: it can
				// usually correct a malformed call on the next turn. The user
				// is shown the same words the model will read.
				failure := conv.AddToolFailure(req.ID, err)
				out.step("%s\n\n", indent(failure.Render()))
				continue
			}

			conv.Append(answer)
			if out.verbose {
				out.step("%s\n", indent(answer.Render()))
			}
			out.step("\n")
		}
	}

	return "", fmt.Errorf("gave up after %d turns without a final answer", maxTurns)
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
func restate(ctx context.Context, out output, client transport.Client, conv *model.Conversation, schema *model.Schema) (string, error) {
	mark := conv.Len()
	defer func() {
		// The mark is where the instruction below goes, so the dropped tail
		// always starts at a harness instruction and this cannot fail.
		_ = conv.DropFrom(mark)
	}()

	conv.AddHarnessInstruction(schemaInstruction(schema))

	// The reason the last attempt was rejected, reported to the model and, if
	// the attempts run out, to the user.
	var rejected error

	for attempt := 1; attempt <= maxSchemaAttempts; attempt++ {
		out.step("%s %s\n", tools.Name("schema"),
			tools.Detail(fmt.Sprintf("restating the answer as JSON (attempt %d of %d)", attempt, maxSchemaAttempts)))

		reply, err := client.Send(ctx, transport.Request{
			Model:        modelName(),
			Conversation: conv,
			// No tools: the work is done, and the only thing left to do is say
			// the answer again in the right shape.
		})
		if err != nil {
			return "", err
		}

		// Only the prose is kept. A turn that asks for a tool has misread the
		// instruction, and there is no tool on offer to answer it with.
		document := strings.TrimSpace(reply.Message.Content)
		if document == "" {
			rejected = errors.New("the reply contained no JSON at all")
		} else {
			conv.AddAssistant(model.NewAssistantMessage(document))
			rejected = schema.ValidateJSON(document)
			if rejected == nil {
				return document, nil
			}
		}

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
