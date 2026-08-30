package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/tools"
)

// describedTool is a tool whose description and parameters are distinguishable
// from one another, so a test can tell which of the two reached the screen.
type describedTool struct {
	name        string
	description string
}

func (d describedTool) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name:        d.name,
		Description: d.description,
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"secret_parameter": map[string]any{"type": "string"}},
		},
	}
}

func (describedTool) Call(context.Context, json.RawMessage) (string, error) { return "", nil }

// verbose returns an output that prints everything to buf, as --verbose at a
// terminal does.
func verbose(buf *bytes.Buffer) output {
	return output{answer: buf, steps: buf, verbose: true}
}

// setup is what an agent was given, for a test that does not care which
// prompt it was.
func setup(system string, r *tools.Registry) agentSetup {
	return agentSetup{system: system, registry: r}
}

func TestPreambleShowsPromptAndDescriptions(t *testing.T) {
	registry := registryWith(t,
		describedTool{name: "bash", description: "Run a shell command."},
		describedTool{name: "writefile", description: "Create a file that is not there yet."},
	)

	var out bytes.Buffer
	preamble(verbose(&out), &scriptedClient{}, nil, setup("the whole system prompt", registry), nil)

	for _, want := range []string{
		"test-model",
		"http://gateway.test",
		"the whole system prompt",
		"bash",
		"Run a shell command.",
		"writefile",
		"Create a file that is not there yet.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %q, want it to contain %q", out.String(), want)
		}
	}
}

// The parameters are the bulk of a definition and say nothing a person
// watching a run is asking about, so they are deliberately left out.
func TestPreambleOmitsParameters(t *testing.T) {
	registry := registryWith(t, describedTool{name: "bash", description: "Run a shell command."})

	var out bytes.Buffer
	preamble(verbose(&out), &scriptedClient{}, nil, setup("system", registry), nil)

	if strings.Contains(out.String(), "secret_parameter") {
		t.Errorf("output = %q, want the tool's parameters left out", out.String())
	}
}

// Tools are described in the order they are offered to the model, so that
// what is on the screen and what was sent can be read against each other.
func TestPreambleDescribesToolsInDefinitionOrder(t *testing.T) {
	registry := registryWith(t,
		describedTool{name: "writefile", description: "second"},
		describedTool{name: "bash", description: "first"},
	)

	var out bytes.Buffer
	preamble(verbose(&out), &scriptedClient{}, nil, setup("system", registry), nil)

	if first, second := strings.Index(out.String(), "first"), strings.Index(out.String(), "second"); first > second {
		t.Errorf("output = %q, want the tools in name order", out.String())
	}
}

func TestPreambleShowsSchema(t *testing.T) {
	schema, err := model.ParseSchema(`id:int:"User ID"`)
	if err != nil {
		t.Fatalf("ParseSchema() = %v", err)
	}

	var out bytes.Buffer
	preamble(verbose(&out), &scriptedClient{}, schema, setup("system", registryWith(t)), nil)

	if !strings.Contains(out.String(), schema.String()) {
		t.Errorf("output = %q, want it to contain the schema", out.String())
	}
}

// Without --verbose the run says none of this: it is diagnostics, and the
// ordinary progress stream is for watching work happen.
func TestPreambleSilentWithoutVerbose(t *testing.T) {
	registry := registryWith(t, describedTool{name: "bash", description: "Run a shell command."})

	var out bytes.Buffer
	preamble(humanOutput(&out), &scriptedClient{}, nil, setup("the whole system prompt", registry), nil)

	if out.Len() != 0 {
		t.Errorf("output = %q, want nothing without --verbose", out.String())
	}
}

// A description is the only thing a workspace tool tells the model, so a tool
// with none is a mistake worth seeing rather than a gap on the screen.
func TestPreambleMarksAMissingDescription(t *testing.T) {
	registry := registryWith(t, describedTool{name: "bash", description: "  \n "})

	var out bytes.Buffer
	preamble(verbose(&out), &scriptedClient{}, nil, setup("system", registry), nil)

	if !strings.Contains(out.String(), "(no description)") {
		t.Errorf("output = %q, want a missing description said outright", out.String())
	}
}

func TestPreambleShowsTheSubagentPrompt(t *testing.T) {
	var out bytes.Buffer
	preamble(verbose(&out), &scriptedClient{}, nil,
		setup("the run's own prompt", registryWith(t)),
		&agentSetup{system: "the subagent's prompt", registry: registryWith(t)})

	for _, want := range []string{"the run's own prompt", "the subagent's prompt"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %q, want it to contain %q", out.String(), want)
		}
	}
}

// A subagent's tools are mostly the run's own, and a second full listing of
// them would bury the one thing the list is for.
func TestPreambleDoesNotExplainASharedToolTwice(t *testing.T) {
	shared := describedTool{name: "bash", description: "Run a shell command."}

	var out bytes.Buffer
	preamble(verbose(&out), &scriptedClient{}, nil,
		setup("system", registryWith(t, shared)),
		&agentSetup{system: "subagent system", registry: registryWith(t, shared)})

	if got := strings.Count(out.String(), "Run a shell command."); got != 1 {
		t.Errorf("description printed %d times, want 1:\n%s", got, out.String())
	}
	if !strings.Contains(out.String(), "explained above") {
		t.Errorf("output = %q, want the repeat named as already explained", out.String())
	}
}

// A tool only a subagent has was never explained, so it is explained here.
func TestPreambleExplainsAToolOnlyASubagentHas(t *testing.T) {
	var out bytes.Buffer
	preamble(verbose(&out), &scriptedClient{}, nil,
		setup("system", registryWith(t, describedTool{name: "bash", description: "Run a shell command."})),
		&agentSetup{system: "subagent system", registry: registryWith(t,
			describedTool{name: "bash", description: "Run a shell command."},
			describedTool{name: "writefile", description: "Create a file that is not there yet."},
		)})

	if !strings.Contains(out.String(), "Create a file that is not there yet.") {
		t.Errorf("output = %q, want the tool the run itself lacks explained", out.String())
	}
}

// What counts as already explained is the description, not the name: the same
// tool is described differently to two agents when what it can do differs,
// and pointing upward at the other agent's description would mislead.
func TestPreambleExplainsAToolDescribedDifferently(t *testing.T) {
	var out bytes.Buffer
	preamble(verbose(&out), &scriptedClient{}, nil,
		setup("system", registryWith(t, describedTool{name: "patchfile", description: "It keeps a copy first."})),
		&agentSetup{system: "subagent system", registry: registryWith(t,
			describedTool{name: "patchfile", description: "It keeps no copy."},
		)})

	if !strings.Contains(out.String(), "It keeps no copy.") {
		t.Errorf("output = %q, want the subagent's own description shown", out.String())
	}
	if strings.Contains(out.String(), "explained above") {
		t.Errorf("output = %q, want a differing description not called already explained", out.String())
	}
}

// A run with no way to spawn a subagent has nothing to say about one, which
// is the rule that keeps an unregistered tool out of the system prompt.
func TestSubagentPreviewNeedsTheTool(t *testing.T) {
	sub, err := subagentPreview(registryWith(t, describedTool{name: "bash", description: "Run a shell command."}), "", nil)
	if err != nil {
		t.Fatalf("subagentPreview() = %v", err)
	}
	if sub != nil {
		t.Errorf("subagentPreview() = %v, want nothing for a run that cannot spawn one", sub)
	}
}

// What the preview shows has to be what a subagent actually gets: the same
// tools, and the same prompt assembled from them.
func TestSubagentPreviewMatchesWhatASubagentGets(t *testing.T) {
	registry, err := defaultRegistry(&scriptedClient{}, nolog(), "", nil, nil)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}

	sub, err := subagentPreview(registry, "", nil)
	if err != nil {
		t.Fatalf("subagentPreview() = %v", err)
	}
	if sub == nil {
		t.Fatal("subagentPreview() = nil, want the tools a subagent would get")
	}

	real, err := workspaceRegistry("", "subagent-1", nil)
	if err != nil {
		t.Fatalf("workspaceRegistry() = %v", err)
	}
	if got, want := sub.registry.Names(), real.Names(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("preview tools = %v, want %v", got, want)
	}
	if got, want := sub.system, subagentSystemPrompt(real); got != want {
		t.Errorf("preview prompt = %q, want %q", got, want)
	}
}
