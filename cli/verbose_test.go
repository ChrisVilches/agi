package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/model"
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

func TestPreambleShowsPromptAndDescriptions(t *testing.T) {
	registry := registryWith(t,
		describedTool{name: "bash", description: "Run a shell command."},
		describedTool{name: "writefile", description: "Create a file that is not there yet."},
	)

	var out bytes.Buffer
	preamble(verbose(&out), &scriptedClient{}, registry, nil, "the whole system prompt")

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
	preamble(verbose(&out), &scriptedClient{}, registry, nil, "system")

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
	preamble(verbose(&out), &scriptedClient{}, registry, nil, "system")

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
	preamble(verbose(&out), &scriptedClient{}, registryWith(t), schema, "system")

	if !strings.Contains(out.String(), schema.String()) {
		t.Errorf("output = %q, want it to contain the schema", out.String())
	}
}

// Without --verbose the run says none of this: it is diagnostics, and the
// ordinary progress stream is for watching work happen.
func TestPreambleSilentWithoutVerbose(t *testing.T) {
	registry := registryWith(t, describedTool{name: "bash", description: "Run a shell command."})

	var out bytes.Buffer
	preamble(humanOutput(&out), &scriptedClient{}, registry, nil, "the whole system prompt")

	if out.Len() != 0 {
		t.Errorf("output = %q, want nothing without --verbose", out.String())
	}
}

// A description is the only thing a workspace tool tells the model, so a tool
// with none is a mistake worth seeing rather than a gap on the screen.
func TestPreambleMarksAMissingDescription(t *testing.T) {
	registry := registryWith(t, describedTool{name: "bash", description: "  \n "})

	var out bytes.Buffer
	preamble(verbose(&out), &scriptedClient{}, registry, nil, "system")

	if !strings.Contains(out.String(), "(no description)") {
		t.Errorf("output = %q, want a missing description said outright", out.String())
	}
}
