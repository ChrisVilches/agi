package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// runPrompt wires up a gateway and tools, then runs the agent on prompt.
func runPrompt(ctx context.Context, out io.Writer, prompt string, verbose bool) error {
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

	if verbose {
		fmt.Fprintf(out, "model %s via %s, %d tool(s)\n\n", modelName(), gateway.BaseURL(), registry.Len())
	}
	return runAgent(ctx, out, gateway, registry, prompt, verbose)
}

// runAgent drives the conversation: ask the model, run whatever tools it asks
// for, hand the results back, and repeat until it answers in prose. The
// conversation itself knows when the run is finished and what is outstanding,
// so this loop only has to do the work in between.
//
// It takes a transport.Client rather than a concrete gateway so a run can be
// exercised without a network.
func runAgent(ctx context.Context, out io.Writer, client transport.Client, registry *tools.Registry, prompt string, verbose bool) error {
	if strings.TrimSpace(prompt) == "" {
		return errors.New("the prompt is empty")
	}

	conv := model.NewConversation(systemPrompt(registry), prompt)

	for turn := 0; turn < maxTurns; turn++ {
		reply, err := client.Send(ctx, transport.Request{
			Model:        modelName(),
			Conversation: conv,
			Tools:        registry.Definitions(),
		})
		if err != nil {
			return err
		}
		conv.AddAssistant(reply.Message)

		if answer, done := conv.FinalAnswer(); done {
			answer = strings.TrimSpace(answer)
			if answer == "" {
				return errors.New("the model returned an empty answer")
			}
			fmt.Fprintln(out, answer)
			return nil
		}

		// Prose alongside tool requests is the model thinking out loud.
		if thinking := strings.TrimSpace(reply.Message.Content); thinking != "" {
			fmt.Fprintf(out, "%s\n\n", thinking)
		}

		for _, req := range conv.PendingToolRequests() {
			fmt.Fprintln(out, registry.Announce(req.Name, req.Arguments))

			output, err := registry.Call(ctx, req.Name, req.Arguments)
			if err != nil {
				// Report the failure to the model rather than aborting: it can
				// usually correct a malformed call on the next turn.
				conv.AddToolFailure(req.ID, err)
				fmt.Fprintf(out, "%s\n\n", indent("error: "+err.Error()))
				continue
			}

			conv.AddToolResult(req.ID, output)
			if verbose {
				fmt.Fprintf(out, "%s\n", indent(output))
			}
			fmt.Fprintln(out)
		}
	}

	return fmt.Errorf("gave up after %d turns without a final answer", maxTurns)
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
