// Package tools defines the contract for tools the agent can call, and the
// registry that collects them.
//
// Tools fall into three categories, which differ in how much the model is told
// about them:
//
//   - Control-flow tools (subpackage controlflow) are primitives that act on
//     the agent itself — spawning subagents, rewinding, committing a
//     checkpoint. The model cannot infer when to reach for them from a
//     description alone, so they also contribute guidance to the system prompt
//     by implementing SystemPrompter.
//
//   - Workspace tools (subpackage workspace) do ordinary work on the machine:
//     bash, read, write, edit. They are a convenience and can be switched off
//     entirely, so they must never be mentioned in the system prompt. Their
//     Definition is all the model learns about them.
//
//   - User-defined tools come from the user's own configuration. Like
//     workspace tools, they are described only by their Definition.
//
// The rule that follows: implement SystemPrompter only for control-flow tools.
// A prompt that describes a tool which may not be registered would be a
// standing lie to the model.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ChrisVilches/agi/model"
)

// Tool is a single callable exposed to the model.
type Tool interface {
	// Definition describes the tool to the model.
	Definition() model.ToolDefinition
	// Call runs the tool with the raw JSON arguments the model produced and
	// returns the output to hand back to it.
	//
	// A returned error means the call could not be attempted — malformed
	// arguments, say. A tool that ran and failed on its own terms (a command
	// exiting non-zero) should report that in its output instead, so the model
	// can read what happened and try something else.
	Call(ctx context.Context, args json.RawMessage) (string, error)
}

// SystemPrompter is implemented by tools whose use has to be explained in the
// system prompt. Only control-flow tools should implement it; see the package
// documentation.
type SystemPrompter interface {
	// SystemPrompt returns guidance on when and how to use the tool.
	SystemPrompt() string
}

// Resulter is implemented by a tool whose output is worth more than a string.
// The registry prefers it when a tool has it, so the structure reaches the
// conversation — and a session snapshot — rather than being flattened into
// text at the moment it is produced.
//
// A tool that implements it still implements Tool: Call is what a caller with
// nothing to append uses, and it returns the same text this would render.
type Resulter interface {
	// Result runs the tool and returns the turn answering requestID.
	Result(ctx context.Context, requestID string, args json.RawMessage) (model.ToolAnswer, error)
}

// Announcer is implemented by tools that can describe a pending call, so the
// harness can show the user what the agent is about to do before it happens.
type Announcer interface {
	// Announce summarizes a call from its raw arguments, without the tool's
	// name: the registry adds that. It may span several lines. It is display
	// only and must not fail; return a best-effort string for unparsable
	// arguments.
	Announce(args json.RawMessage) string
}

// Registry is the set of tools available for a run.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a tool. It fails on an unnamed tool or a duplicate name, since
// the model addresses tools by name and could not tell two apart.
func (r *Registry) Register(ts ...Tool) error {
	for _, t := range ts {
		name := t.Definition().Name
		if name == "" {
			return fmt.Errorf("tools: cannot register a tool with no name")
		}
		if _, exists := r.tools[name]; exists {
			return fmt.Errorf("tools: %q is already registered", name)
		}
		r.tools[name] = t
	}
	return nil
}

// Len returns how many tools are registered.
func (r *Registry) Len() int { return len(r.tools) }

// Lookup returns the named tool.
func (r *Registry) Lookup(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Definitions returns every tool definition, ordered by name so the request
// sent to a provider is stable and stays cacheable.
func (r *Registry) Definitions() []model.ToolDefinition {
	defs := make([]model.ToolDefinition, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.Definition())
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

// Names returns the registered tool names, in the order Definitions offers
// them. It is for saying which tools a run was given, which is one of the
// things that has to be known to reproduce it.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SystemPrompt returns the guidance contributed by registered tools that
// implement SystemPrompter, in tool-name order. Tools that do not implement it
// contribute nothing, which is the intended behavior for workspace and
// user-defined tools.
//
// Each contribution is headed by the name of the tool it explains, so that a
// model reading several of them can tell where one ends and the next begins,
// and which tool each is about. The heading is === name === rather than a
// Markdown one because the guidance is Markdown itself: a heading would
// dissolve into the prose it is supposed to separate.
func (r *Registry) SystemPrompt() string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)

	var parts []string
	for _, name := range names {
		if p, ok := r.tools[name].(SystemPrompter); ok {
			if text := strings.TrimSpace(p.SystemPrompt()); text != "" {
				parts = append(parts, "=== "+name+" ===\n\n"+text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// Announce renders a pending call for display, as a colored [name] label
// followed by whatever the tool says about the call. Tools that do not
// implement Announcer are shown as the label alone.
func (r *Registry) Announce(name string, args json.RawMessage) string {
	label := Name(name)

	t, ok := r.tools[name]
	if !ok {
		return label
	}
	a, ok := t.(Announcer)
	if !ok {
		return label
	}
	text := strings.TrimSpace(a.Announce(args))
	if text == "" {
		return label
	}
	return label + " " + text
}

// Call runs the named tool and returns its output as text.
func (r *Registry) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("tools: no tool named %q", name)
	}
	return t.Call(ctx, args)
}

// Answer runs the tool a request names and returns the turn that answers it.
//
// A tool that implements Resulter produces that turn itself, keeping whatever
// structure it has; every other tool returns text, which is wrapped in a plain
// result here. Either way the caller appends one turn and does not need to
// know which kind of tool it came from.
func (r *Registry) Answer(ctx context.Context, req model.ToolRequest) (model.ToolAnswer, error) {
	t, ok := r.tools[req.Name]
	if !ok {
		return nil, fmt.Errorf("tools: no tool named %q", req.Name)
	}

	if structured, ok := t.(Resulter); ok {
		return structured.Result(ctx, req.ID, req.Arguments)
	}

	out, err := t.Call(ctx, req.Arguments)
	if err != nil {
		return nil, err
	}
	return model.NewToolResult(req.ID, out), nil
}
