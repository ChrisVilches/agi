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
// for, hand the results back, and repeat until it answers in prose.
//
// It takes a transport.Client rather than a concrete gateway so a run can be
// exercised without a network.
func runAgent(ctx context.Context, out io.Writer, client transport.Client, registry *tools.Registry, prompt string, verbose bool) error {
	if strings.TrimSpace(prompt) == "" {
		return errors.New("the prompt is empty")
	}

	conv := model.Conversation{Messages: []model.Message{
		{Role: model.RoleUser, Content: prompt},
	}}
	system := systemPrompt(registry)

	for turn := 0; turn < maxTurns; turn++ {
		resp, err := client.Send(ctx, transport.Request{
			Model:        modelName(),
			System:       system,
			Conversation: conv,
			Tools:        registry.Definitions(),
		})
		if err != nil {
			return err
		}
		conv.Append(resp.Message)

		// The model is done as soon as it stops asking for tools.
		if len(resp.Message.ToolCalls) == 0 {
			answer := strings.TrimSpace(resp.Message.Content)
			if answer == "" {
				return errors.New("the model returned an empty answer")
			}
			fmt.Fprintln(out, answer)
			return nil
		}

		// Prose alongside tool calls is the model thinking out loud.
		if thinking := strings.TrimSpace(resp.Message.Content); thinking != "" {
			fmt.Fprintf(out, "%s\n\n", thinking)
		}

		for _, call := range resp.Message.ToolCalls {
			fmt.Fprintln(out, registry.Announce(call.Name, call.Arguments))

			output, err := registry.Call(ctx, call.Name, call.Arguments)
			if err != nil {
				// Report the failure to the model rather than aborting: it can
				// usually correct a malformed call on the next turn.
				output = "error: " + err.Error()
			}
			if strings.TrimSpace(output) == "" {
				output = "(no output)"
			}
			if verbose {
				fmt.Fprintf(out, "%s\n", indent(output))
			}
			fmt.Fprintln(out)

			conv.Append(model.Message{
				Role:       model.RoleTool,
				ToolCallID: call.ID,
				Content:    output,
			})
		}
	}

	return fmt.Errorf("gave up after %d turns without a final answer", maxTurns)
}

// indent prefixes every line, so tool output is visibly subordinate to the
// call that produced it.
func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  | " + l
	}
	return strings.Join(lines, "\n")
}
