package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/model"
)

type stub struct {
	name     string
	prompt   string // non-empty means it implements SystemPrompter via prompted
	announce string
	out      string
}

func (s *stub) Definition() model.ToolDefinition {
	return model.ToolDefinition{Name: s.name, Description: "stub"}
}
func (s *stub) Call(context.Context, json.RawMessage) (string, error) { return s.out, nil }

type prompted struct{ stub }

func (p *prompted) SystemPrompt() string { return p.prompt }

type announcing struct{ stub }

func (a *announcing) Announce(json.RawMessage) string { return a.announce }

type reporting struct{ stub }

func (r *reporting) Report(result string) string {
	if result == "" {
		return ""
	}
	return "made " + result + "\n"
}

func TestRegisterRejectsDuplicatesAndUnnamed(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&stub{name: "a"}); err != nil {
		t.Fatalf("Register() = %v, want nil", err)
	}
	if err := r.Register(&stub{name: "a"}); err == nil {
		t.Error("Register() of a duplicate = nil, want an error")
	}
	if err := r.Register(&stub{}); err == nil {
		t.Error("Register() of an unnamed tool = nil, want an error")
	}
	if r.Len() != 1 {
		t.Errorf("Len() = %d, want 1", r.Len())
	}
}

// Definitions must be ordered so repeated requests are byte-identical.
func TestDefinitionsAreSortedByName(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&stub{name: "zebra"}, &stub{name: "alpha"}, &stub{name: "mike"}); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	var names []string
	for _, d := range r.Definitions() {
		names = append(names, d.Name)
	}
	want := []string{"alpha", "mike", "zebra"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("Definitions() = %v, want %v", names, want)
	}
}

// This is the central rule of the package: only tools that opt in through
// SystemPrompter reach the system prompt.
func TestSystemPromptCollectsOnlyPrompters(t *testing.T) {
	r := NewRegistry()
	work := &stub{name: "bash"}
	control := &prompted{stub: stub{name: "rewind"}}
	control.prompt = "Rewind to abandon a failed approach."
	if err := r.Register(work, control); err != nil {
		t.Fatalf("Register() = %v", err)
	}

	got := r.SystemPrompt()
	if !strings.Contains(got, "Rewind to abandon a failed approach.") {
		t.Errorf("SystemPrompt() = %q, want the control-flow guidance", got)
	}
	if strings.Contains(got, "bash") {
		t.Errorf("SystemPrompt() = %q, want no mention of a work tool", got)
	}
}

func TestSystemPromptEmptyWithoutPrompters(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&stub{name: "bash"}, &stub{name: "read"}); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	if got := r.SystemPrompt(); got != "" {
		t.Errorf("SystemPrompt() = %q, want empty", got)
	}
}

// Guidance from several control-flow tools must be ordered, and each piece
// headed by the tool it is about: the guidance is Markdown prose, so without a
// delimiter a model has no way to tell where one explanation ends.
func TestSystemPromptOrdersAndJoins(t *testing.T) {
	r := NewRegistry()
	first := &prompted{stub: stub{name: "alpha"}}
	first.prompt = "First guidance."
	second := &prompted{stub: stub{name: "beta"}}
	second.prompt = "Second guidance."
	if err := r.Register(second, first); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	want := "=== alpha ===\n\nFirst guidance.\n\n=== beta ===\n\nSecond guidance."
	if got := r.SystemPrompt(); got != want {
		t.Errorf("SystemPrompt() = %q, want %q", got, want)
	}
}

func TestAnnounceFallsBackToName(t *testing.T) {
	r := NewRegistry()
	plain := &stub{name: "plain"}
	fancy := &announcing{stub: stub{name: "fancy"}}
	fancy.announce = "doing something"
	if err := r.Register(plain, fancy); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	// The registry supplies the bracketed label; the tool supplies the rest.
	if got := r.Announce("plain", nil); got != "[plain]" {
		t.Errorf("Announce(plain) = %q, want just the label", got)
	}
	if got := r.Announce("fancy", nil); got != "[fancy] doing something" {
		t.Errorf("Announce(fancy) = %q, want the label and the tool's summary", got)
	}
	if got := r.Announce("absent", nil); got != "[absent]" {
		t.Errorf("Announce(absent) = %q, want the label", got)
	}
}

// A tool with nothing to say after the fact reports the empty string, and the
// caller prints nothing for it.
func TestReportIsEmptyUnlessTheToolHasSomethingToSay(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&stub{name: "plain"}, &reporting{stub: stub{name: "fancy"}}); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	if got := r.Report("plain", "anything"); got != "" {
		t.Errorf("Report(plain) = %q, want empty for a tool that does not report", got)
	}
	if got := r.Report("absent", "anything"); got != "" {
		t.Errorf("Report(absent) = %q, want empty", got)
	}
	if got := r.Report("fancy", ""); got != "" {
		t.Errorf("Report(fancy) of a result with nothing in it = %q, want empty", got)
	}
	// The trailing newline is the caller's to add, so it is trimmed here.
	if got := r.Report("fancy", "/tmp/x"); got != "made /tmp/x" {
		t.Errorf("Report(fancy) = %q, want the tool's line without its newline", got)
	}
}

func TestCallUnknownTool(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Call(context.Background(), "nope", nil); err == nil {
		t.Error("Call() of an unknown tool = nil error, want an error")
	}
}

func TestCallDispatches(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&stub{name: "a", out: "output"}); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	got, err := r.Call(context.Background(), "a", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call() = %v, want nil", err)
	}
	if got != "output" {
		t.Errorf("Call() = %q, want %q", got, "output")
	}
}

// structuredTool returns a typed answer instead of a string.
type structuredTool struct{ stub }

func (s *structuredTool) Result(_ context.Context, requestID string, _ json.RawMessage) (model.ToolAnswer, error) {
	return model.NewSubagentResults(requestID, []model.SubagentResult{
		{Title: "First", Answer: "one"},
		{Title: "Second", Answer: "two"},
	}), nil
}

// A tool that has structure to report keeps it all the way to the turn that is
// appended; one that does not is wrapped in a plain result.
func TestRegistryAnswer(t *testing.T) {
	plain := &stub{name: "probe", out: "disk is fine"}
	structured := &structuredTool{stub: stub{name: "subagents"}}

	r := NewRegistry()
	if err := r.Register(plain, structured); err != nil {
		t.Fatalf("Register() = %v", err)
	}

	got, err := r.Answer(context.Background(), model.ToolRequest{ID: "c1", Name: "probe"})
	if err != nil {
		t.Fatalf("Answer() = %v", err)
	}
	if _, ok := got.(model.ToolResultMessage); !ok {
		t.Errorf("Answer() = %T, want a plain result for a tool with no structure", got)
	}
	if got.AnsweredRequest() != "c1" || got.Render() != "disk is fine" {
		t.Errorf("Answer() = %+v, want the tool output answering c1", got)
	}

	got, err = r.Answer(context.Background(), model.ToolRequest{ID: "c2", Name: "subagents"})
	if err != nil {
		t.Fatalf("Answer() = %v", err)
	}
	results, ok := got.(model.SubagentResults)
	if !ok {
		t.Fatalf("Answer() = %T, want the tool's own typed answer", got)
	}
	if len(results.Results) != 2 {
		t.Errorf("results = %+v, want both answers kept apart", results.Results)
	}
	if results.AnsweredRequest() != "c2" {
		t.Errorf("AnsweredRequest() = %q, want c2", results.AnsweredRequest())
	}

	if _, err := r.Answer(context.Background(), model.ToolRequest{ID: "c3", Name: "missing"}); err == nil {
		t.Error("Answer() of an unregistered tool = nil, want an error")
	}
}

// Which tools a run was given is part of what has to be known to reproduce it,
// and the order is the one the model is offered them in.
func TestNamesAreOrderedLikeDefinitions(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&stub{name: "write"}, &stub{name: "bash"}, &stub{name: "read"}); err != nil {
		t.Fatalf("Register() = %v", err)
	}

	got := r.Names()
	want := []string{"bash", "read", "write"}
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
	}

	defs := r.Definitions()
	for i := range defs {
		if defs[i].Name != got[i] {
			t.Errorf("Names() and Definitions() disagree at %d: %q and %q", i, got[i], defs[i].Name)
		}
	}

	if len(NewRegistry().Names()) != 0 {
		t.Error("an empty registry names tools")
	}
}
