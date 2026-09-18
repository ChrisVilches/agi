package controlflow

import (
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/tools"
)

// Control-flow tools are the only ones allowed to reach the system prompt, so
// the shared base must satisfy that interface.
func TestBaseIsASystemPrompter(t *testing.T) {
	var p tools.SystemPrompter = Base{}
	if strings.TrimSpace(p.SystemPrompt()) == "" {
		t.Error("SystemPrompt() is empty, so a control-flow tool would explain nothing")
	}
}

func TestPromptIsTrimmed(t *testing.T) {
	got := Prompt()
	if got != strings.TrimSpace(got) {
		t.Error("Prompt() has surrounding whitespace")
	}
	if got == "" {
		t.Fatal("Prompt() is empty")
	}
}

// The guidance has to cover all three primitives, and say when to use them —
// a schema alone never tells the model that.
func TestGuidanceCoversEveryPrimitive(t *testing.T) {
	got := strings.ToLower(Prompt())
	for _, want := range []string{"subagent", "checkpoint", "rewind"} {
		if !strings.Contains(got, want) {
			t.Errorf("guidance does not mention %q", want)
		}
	}
	for _, when := range []string{"when", "do not"} {
		if !strings.Contains(got, when) {
			t.Errorf("guidance does not say %q, so it explains what but not when", when)
		}
	}
}
