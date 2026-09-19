package prompt

import (
	"strings"
	"testing"
)

// Each prompt is a file, and an empty or untrimmed one would be sent to a
// model exactly as it is.
func TestPromptsAreReadyToSend(t *testing.T) {
	tests := []struct {
		name string
		got  string
	}{
		{"Base", Base()},
		{"Subagents", Subagents()},
		{"Subagent", Subagent()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got == "" {
				t.Fatalf("%s() is empty", tt.name)
			}
			if tt.got != strings.TrimSpace(tt.got) {
				t.Errorf("%s() has surrounding whitespace", tt.name)
			}
		})
	}
}

// The base is the half that is meant to be swapped, so it must not describe
// anything agi does: whatever replaces it has to work just as well.
func TestBaseAssumesNothing(t *testing.T) {
	got := strings.ToLower(Base())
	for _, word := range []string{"subagent", "tool", "json", "schema", "session"} {
		if strings.Contains(got, word) {
			t.Errorf("base prompt mentions %q, which a replacement would have to know about", word)
		}
	}
}

// A subagent's answer is read by a machine that cannot reply to it, so the
// prompt has to rule out the conversational habits a model would otherwise
// finish on.
func TestSubagentPromptForbidsAskingForMore(t *testing.T) {
	got := strings.ToLower(Subagent())

	if !strings.Contains(got, "final") {
		t.Error("subagent prompt does not say the answer is final")
	}
	for _, want := range []string{"let me know", "would you like"} {
		if !strings.Contains(got, want) {
			t.Errorf("subagent prompt does not rule out closing with %q", want)
		}
	}
	if !strings.Contains(got, "cannot be done") {
		t.Error("subagent prompt does not say what to do when the task fails")
	}
}

// A control-flow tool's guidance reaches the model only when that tool is
// registered, so it has to stand on its own rather than as half of a document
// shared with primitives a run may not have been given.
func TestControlFlowGuidanceStandsAlone(t *testing.T) {
	got := strings.ToLower(Subagents())
	if !strings.Contains(got, "subagent") {
		t.Error("Subagents() never mentions subagents")
	}
	// A schema says what a tool does; only the prompt can say when to reach
	// for it and when not to.
	for _, when := range []string{"when", "do not"} {
		if !strings.Contains(got, when) {
			t.Errorf("Subagents() does not say %q, so it explains what but not when", when)
		}
	}
}
