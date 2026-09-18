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

// Guidance from several control-flow tools must be ordered and separated.
func TestSystemPromptOrdersAndJoins(t *testing.T) {
	r := NewRegistry()
	first := &prompted{stub: stub{name: "alpha"}}
	first.prompt = "First guidance."
	second := &prompted{stub: stub{name: "beta"}}
	second.prompt = "Second guidance."
	if err := r.Register(second, first); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	if got := r.SystemPrompt(); got != "First guidance.\n\nSecond guidance." {
		t.Errorf("SystemPrompt() = %q, want the two joined in name order", got)
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
