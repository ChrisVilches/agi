package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/tools"
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
	// baseSystemPrompt is deliberately minimal. Tools are described to the
	// model through their definitions, not here; only control-flow tools add
	// to this, and they do it themselves through the registry.
	baseSystemPrompt = "You are a helpful assistant."
)

// modelName returns the model to run on.
func modelName() string {
	if v := strings.TrimSpace(os.Getenv(EnvModel)); v != "" {
		return v
	}
	return DefaultModel
}

// defaultRegistry returns the tools available to a run.
func defaultRegistry() (*tools.Registry, error) {
	r := tools.NewRegistry()
	if err := r.Register(workspace.NewBash()); err != nil {
		return nil, err
	}
	return r, nil
}

// systemPrompt assembles the system prompt: a base instruction plus whatever
// the registered tools insist on explaining. Workspace and user-defined tools
// contribute nothing here by design.
func systemPrompt(r *tools.Registry) string {
	parts := []string{baseSystemPrompt}
	if guidance := r.SystemPrompt(); guidance != "" {
		parts = append(parts, guidance)
	}
	return strings.Join(parts, "\n\n")
}

// runPrompt wires up a gateway and tools, then runs the agent on prompt. A nil
// schema means the answer is whatever prose the model ends on.
func runPrompt(ctx context.Context, out output, prompt string, schema *model.Schema) error {
	gateway, err := litellm.FromEnv()
	if err != nil {
		if errors.Is(err, litellm.ErrNotConfigured) {
			return fmt.Errorf("no gateway configured: set %s to the gateway's base URL", litellm.EnvURL)
		}
		return err
	}

	registry, err := defaultRegistry()
	if err != nil {
		return err
	}

	if out.verbose {
		out.step("model %s via %s, %d tool(s)\n\n", modelName(), gateway.BaseURL(), registry.Len())
		if schema != nil {
			out.step("schema %s\n\n", schema)
		}
	}
	return runAgent(ctx, out, gateway, registry, prompt, schema)
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
// It takes a transport.Client rather than a concrete gateway so a run can be
// exercised without a network.
func runAgent(ctx context.Context, out output, client transport.Client, registry *tools.Registry, prompt string, schema *model.Schema) error {
	if strings.TrimSpace(prompt) == "" {
		return errors.New("the prompt is empty")
	}

	conv := model.NewConversation(systemPrompt(registry), prompt)

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

			result, err := registry.Call(ctx, req.Name, req.Arguments)
			if err != nil {
				// Report the failure to the model rather than aborting: it can
				// usually correct a malformed call on the next turn.
				conv.AddToolFailure(req.ID, err)
				out.step("%s\n\n", indent("error: "+err.Error()))
				continue
			}

			conv.AddToolResult(req.ID, result)
			if out.verbose {
				out.step("%s\n", indent(result))
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
