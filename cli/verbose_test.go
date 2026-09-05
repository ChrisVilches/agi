package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/prompt"
	"github.com/ChrisVilches/agi/session"
	"github.com/ChrisVilches/agi/tools"
	"github.com/ChrisVilches/agi/tools/native"
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
	preamble(verbose(&out), &scriptedClient{}, nil, setup("the whole system prompt", registry))

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
	preamble(verbose(&out), &scriptedClient{}, nil, setup("system", registry))

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
	preamble(verbose(&out), &scriptedClient{}, nil, setup("system", registry))

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
	preamble(verbose(&out), &scriptedClient{}, schema, setup("system", registryWith(t)))

	if !strings.Contains(out.String(), schema.String()) {
		t.Errorf("output = %q, want it to contain the schema", out.String())
	}
}

// Without --verbose the run says none of this: it is diagnostics, and the
// ordinary progress stream is for watching work happen.
func TestPreambleSilentWithoutVerbose(t *testing.T) {
	registry := registryWith(t, describedTool{name: "bash", description: "Run a shell command."})

	var out bytes.Buffer
	preamble(humanOutput(&out), &scriptedClient{}, nil, setup("the whole system prompt", registry))

	if out.Len() != 0 {
		t.Errorf("output = %q, want nothing without --verbose", out.String())
	}
}

// A description is the only thing a workspace tool tells the model, so a tool
// with none is a mistake worth seeing rather than a gap on the screen.
func TestPreambleMarksAMissingDescription(t *testing.T) {
	registry := registryWith(t, describedTool{name: "bash", description: "  \n "})

	var out bytes.Buffer
	preamble(verbose(&out), &scriptedClient{}, nil, setup("system", registry))

	if !strings.Contains(out.String(), "(no description)") {
		t.Errorf("output = %q, want a missing description said outright", out.String())
	}
}

// A run says nothing about subagents before it has spawned one. Which skills
// a subagent gets is decided task by task, so anything said in advance would
// describe an agent the run may never spawn — which is what the fake one it
// used to describe was.
func TestPreambleSaysNothingAboutSubagents(t *testing.T) {
	registry, err := defaultRegistry(quiet(), &scriptedClient{}, nolog(), "", nil, nil, defaultMaxTurns)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}

	var out bytes.Buffer
	preamble(verbose(&out), &scriptedClient{}, nil, setup("the run's own prompt", registry))

	if strings.Contains(out.String(), firstLine(prompt.Subagent())) {
		t.Errorf("output = %q, want no subagent prompt before one is spawned", out.String())
	}
}

// A subagent says what it was given as it starts: which agent it is, what it
// was asked for, the prompt it reads, and the tools it may call.
func TestSubagentIntroShowsWhatTheAgentWasGiven(t *testing.T) {
	registry := registryWith(t, describedTool{name: "bash", description: "Run a shell command."})
	got := subagentIntro("subagent-2", "Check the tests",
		setup("the subagent's prompt", registry), map[string]string{})

	for _, want := range []string{"subagent-2", "Check the tests", "the subagent's prompt", "bash"} {
		if !strings.Contains(got, want) {
			t.Errorf("intro = %q, want it to contain %q", got, want)
		}
	}
}

// The skills a subagent was given are listed in the prompt it reads, so
// showing the prompt shows them and there is nothing separate to print.
func TestSubagentIntroShowsTheSkillsThroughThePrompt(t *testing.T) {
	found := oneSkill(t)
	registry, err := workspaceRegistry("", subagentName(native.Brief{Number: 1}), nil)
	if err != nil {
		t.Fatalf("workspaceRegistry() = %v", err)
	}
	if err := registry.Register(native.NewLoadSkill(found)); err != nil {
		t.Fatalf("Register() = %v", err)
	}

	got := subagentIntro("subagent-1", "Write it up",
		setup(subagentSystemPrompt(registry, found), registry), map[string]string{})

	if !strings.Contains(got, found[0].Name) {
		t.Errorf("intro = %q, want it to name the skill the task asked for", got)
	}
	if !strings.Contains(got, native.LoadSkillTool) {
		t.Errorf("intro = %q, want the tool that loads it listed", got)
	}
}

// A subagent's tools are mostly the run's own, and explaining them again
// would bury the one thing the list is for.
func TestSubagentIntroNamesAToolTheRunAlreadyExplained(t *testing.T) {
	shared := describedTool{name: "bash", description: "Run a shell command."}
	run := registryWith(t, shared)

	got := subagentIntro("subagent-1", "Check the tests",
		setup("subagent system", registryWith(t, shared)), explainedBy(run))

	if strings.Contains(got, "Run a shell command.") {
		t.Errorf("intro = %q, want a description the run gave not repeated", got)
	}
	if !strings.Contains(got, "explained above") {
		t.Errorf("intro = %q, want the repeat named as already explained", got)
	}
}

// A tool the run itself was not given was never explained, so it is explained
// here.
func TestSubagentIntroExplainsAToolTheRunLacks(t *testing.T) {
	run := registryWith(t, describedTool{name: "bash", description: "Run a shell command."})

	got := subagentIntro("subagent-1", "Write it up", setup("subagent system", registryWith(t,
		describedTool{name: "bash", description: "Run a shell command."},
		describedTool{name: "writefile", description: "Create a file that is not there yet."},
	)), explainedBy(run))

	if !strings.Contains(got, "Create a file that is not there yet.") {
		t.Errorf("intro = %q, want the tool the run itself lacks explained", got)
	}
}

// What counts as already explained is the description, not the name: the same
// tool is described differently to two agents when what it can do differs,
// and pointing upward at the other agent's description would mislead.
func TestSubagentIntroExplainsAToolDescribedDifferently(t *testing.T) {
	run := registryWith(t, describedTool{name: "patchfile", description: "It keeps a copy first."})

	got := subagentIntro("subagent-1", "Change it", setup("subagent system",
		registryWith(t, describedTool{name: "patchfile", description: "It keeps no copy."})),
		explainedBy(run))

	if !strings.Contains(got, "It keeps no copy.") {
		t.Errorf("intro = %q, want the subagent's own description shown", got)
	}
	if strings.Contains(got, "explained above") {
		t.Errorf("intro = %q, want a differing description not called already explained", got)
	}
}

// Each subagent gets a copy of what the run explained, because they start at
// the same moment: one shared map would be several goroutines writing into
// one, and a tool would be called already explained by whichever agent lost
// the race to describe it.
func TestExplainedByReturnsACopyPerCaller(t *testing.T) {
	run := registryWith(t, describedTool{name: "bash", description: "Run a shell command."})

	first, second := explainedBy(run), explainedBy(run)
	first["writefile"] = "only the first list saw this"
	if _, ok := second["writefile"]; ok {
		t.Error("two callers of explainedBy share one map")
	}
}

// The whole point of naming each block is telling several subagents apart, so
// a run that fans out says which agent each block is about, under the names
// the log files them by.
func TestAVerboseRunIntroducesEverySubagent(t *testing.T) {
	sess, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	var out bytes.Buffer
	client := &fanoutClient{}
	registry, err := defaultRegistry(verbose(&out), client, nolog(), "", nil, nil, defaultMaxTurns)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	if err := runSession(context.Background(), verbose(&out), nolog(), client, registry, sess,
		prompt.Base(), "look around", nil, nil, defaultMaxTurns); err != nil {
		t.Fatalf("runSession() = %v", err)
	}

	for _, want := range []string{"subagent-1", "subagent-2", "Read the README", "Check the tests"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not contain %q:\n%s", want, out.String())
		}
	}
	if got := strings.Count(out.String(), firstLine(prompt.Subagent())); got != 2 {
		t.Errorf("the subagent prompt appeared %d times, want one per subagent", got)
	}
}

// firstLine is a needle for a prompt that reaches the screen indented, where
// the whole text of it no longer appears as it was written.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
