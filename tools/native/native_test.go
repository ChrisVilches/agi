package native

import (
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/tools"
)

// everyTool is the native tools that explain themselves, each paired with the
// word its own guidance has to be about. Native tools are the only ones
// allowed to reach the system prompt, and a tool that can say on its own what
// it is for has to take that up rather than leaving it to a schema.
//
// loadskill is deliberately not here. What the model needs to know about it is
// half a section — the other half is the list of skills this run was given —
// so it is assembled where the prompt is rather than returned from the tool.
var everyTool = []struct {
	name string
	tool tools.Tool
	// about is a word that must appear in this tool's guidance, and must not
	// appear in any other's: each explanation is sent on its own account, so
	// none of them may lean on another being there.
	about string
}{
	{"subagents", NewSubagents(nil, nil, nil, testMost), "subagent"},
	{"terminate", NewTerminate(), "exit"},
}

func TestEveryNativeToolWithGuidanceExplainsItself(t *testing.T) {
	for _, tt := range everyTool {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := tt.tool.(tools.SystemPrompter)
			if !ok {
				t.Fatalf("%s does not implement SystemPrompter, so it would explain nothing", tt.name)
			}
			got := p.SystemPrompt()
			if strings.TrimSpace(got) == "" {
				t.Fatalf("%s explains nothing", tt.name)
			}
			if got != strings.TrimSpace(got) {
				t.Errorf("%s guidance has surrounding whitespace", tt.name)
			}
			if name := tt.tool.Definition().Name; name != tt.name {
				t.Errorf("tool name = %q, want %q", name, tt.name)
			}

			// A schema says what a tool does; only the prompt can say when to
			// use it, and when not to.
			for _, when := range []string{"when", "do not"} {
				if !strings.Contains(strings.ToLower(got), when) {
					t.Errorf("%s guidance does not say %q, so it explains what but not when", tt.name, when)
				}
			}
		})
	}
}

// The guidance is per tool because each one reaches the model only when its
// own tool is registered. A run given one primitive and not the other must
// therefore read about the one it has and nothing about the one it does not.
func TestGuidanceIsPerToolAndNotShared(t *testing.T) {
	for _, tt := range everyTool {
		got := strings.ToLower(tt.tool.(tools.SystemPrompter).SystemPrompt())
		for _, other := range everyTool {
			present := strings.Contains(got, other.about)
			switch {
			case other.name == tt.name && !present:
				t.Errorf("%s guidance never mentions %q", tt.name, other.about)
			case other.name != tt.name && present:
				t.Errorf("%s guidance mentions %q, which belongs to %s and may not be registered",
					tt.name, other.about, other.name)
			}
		}
	}
}

// Only the primitives that exist may be described. A prompt that explains a
// tool nobody wrote is a standing lie to the model.
func TestGuidanceDescribesNothingUnimplemented(t *testing.T) {
	for _, tt := range everyTool {
		got := strings.ToLower(tt.tool.(tools.SystemPrompter).SystemPrompt())
		for _, absent := range []string{"checkpoint", "rewind"} {
			if strings.Contains(got, absent) {
				t.Errorf("%s guidance describes %q, which is not implemented yet", tt.name, absent)
			}
		}
	}
}

// The tool refuses a call with one task, so the guidance must not invite one.
// A model going by the prompt alone would otherwise spend a turn finding out.
func TestGuidanceAgreesThatSubagentsComeInTwos(t *testing.T) {
	got := strings.ToLower(NewSubagents(nil, nil, nil, testMost).SystemPrompt())
	for _, want := range []string{"two or more", "refused"} {
		if !strings.Contains(got, want) {
			t.Errorf("guidance does not say %q, so it reads as though one subagent were a call worth making", want)
		}
	}
}
