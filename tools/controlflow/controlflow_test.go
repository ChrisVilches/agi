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

func TestGuidanceSaysWhenToUseAPrimitive(t *testing.T) {
	got := strings.ToLower(Base{}.SystemPrompt())
	if got != strings.TrimSpace(got) {
		t.Error("SystemPrompt() has surrounding whitespace")
	}

	// Only the primitives that exist may be described. A prompt that explains
	// a tool the run was never given is a standing lie to the model.
	if !strings.Contains(got, "subagent") {
		t.Error("guidance does not mention subagents, the one primitive there is")
	}
	for _, absent := range []string{"checkpoint", "rewind"} {
		if strings.Contains(got, absent) {
			t.Errorf("guidance describes %q, which is not implemented yet", absent)
		}
	}

	// A schema says what a tool does; only the prompt can say when to use it.
	for _, when := range []string{"when", "do not"} {
		if !strings.Contains(got, when) {
			t.Errorf("guidance does not say %q, so it explains what but not when", when)
		}
	}
}
