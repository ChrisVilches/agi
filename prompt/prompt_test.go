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
		{"Terminate", Terminate()},
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

// Each native tool's guidance reaches the model only when that tool is
// registered, so a run given one of them and not the other must still read an
// explanation that stands on its own rather than half of a shared document.
func TestNativeToolGuidanceStandsAlone(t *testing.T) {
	tests := []struct {
		name string
		got  string
		tool string
	}{
		{"Subagents", Subagents(), "subagent"},
		{"Terminate", Terminate(), "run"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.ToLower(tt.got)
			if !strings.Contains(got, tt.tool) {
				t.Errorf("%s() never mentions %q", tt.name, tt.tool)
			}
			// A schema says what a tool does; only the prompt can say when to
			// reach for it and when not to.
			for _, when := range []string{"when", "do not"} {
				if !strings.Contains(got, when) {
					t.Errorf("%s() does not say %q, so it explains what but not when", tt.name, when)
				}
			}
		})
	}
}

// The other half of standing alone: neither file may describe the primitive
// the other one is for, since a run may be given either without the other.
func TestControlFlowGuidanceDescribesOnlyItsOwnTool(t *testing.T) {
	if strings.Contains(strings.ToLower(Terminate()), "subagent") {
		t.Error("the terminate guidance describes subagents, which a run given it may not have")
	}
	if strings.Contains(strings.ToLower(Subagents()), "exit") {
		t.Error("the subagents guidance describes ending the run, which a run given it may not be able to do")
	}
}

// Ending the run is the move a model is most likely to reach for too early, so
// the guidance has to say what does not justify it as plainly as what does.
func TestTerminatePromptRulesOutTheEasyWayOut(t *testing.T) {
	// A prompt is prose in a wrapped file, so a phrase to look for may be
	// split across two lines. Matching is done on the words, not the layout.
	got := strings.Join(strings.Fields(strings.ToLower(Terminate())), " ")

	for _, want := range []string{"cannot be understood", "were not given", "does not exist"} {
		if !strings.Contains(got, want) {
			t.Errorf("terminate guidance does not give %q as a reason a run can end this way", want)
		}
	}
	if !strings.Contains(got, "standard error") {
		t.Error("terminate guidance does not say where the reason is printed")
	}
	if !strings.Contains(got, "non-zero") {
		t.Error("terminate guidance does not say the exit status is non-zero")
	}

	// The guidance used to name "it cannot be done for this reason" as an
	// answer belonging in the reply, which is exactly the case the tool is
	// for: a model reading that was told to write the refusal as prose, where
	// it is printed as the answer and the run reports success. What replaced
	// it is a test the model can apply — an answer is about the thing asked
	// about, a reply about oneself is not — and the framing that makes the
	// tool an ending rather than a last resort.
	for _, want := range []string{"two ways", "is not an answer"} {
		if !strings.Contains(got, want) {
			t.Errorf("terminate guidance does not say %q, so prose explaining that "+
				"the model cannot answer reads as an answer again", want)
		}
	}
}
