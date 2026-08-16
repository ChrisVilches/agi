package controlflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/tools"
)

// The tool never produces output. Either the call ends the run, or it could
// not be read at all — and the two have to be told apart by their type, since
// one is reported to the model and the other is the last thing that happens.
func TestTerminateEndsTheRun(t *testing.T) {
	tests := []struct {
		name   string
		args   string
		reason string
	}{
		{"with a reason", `{"reason": "the prompt names no file that exists"}`, "the prompt names no file that exists"},
		{"reason trimmed", `{"reason": "  spaces around it \n"}`, "spaces around it"},
		{"without a reason", `{}`, ""},
		{"empty reason", `{"reason": ""}`, ""},
		{"no arguments at all", ``, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := NewTerminate().Call(context.Background(), json.RawMessage(tt.args))
			if out != "" {
				t.Errorf("Call() returned %q, want no output", out)
			}

			var end *Termination
			if !errors.As(err, &end) {
				t.Fatalf("Call() = %v, want a *Termination", err)
			}
			if end.Reason != tt.reason {
				t.Errorf("Reason = %q, want %q", end.Reason, tt.reason)
			}
		})
	}
}

// Arguments that cannot be read are an ordinary failure: the model is told
// what was wrong and gets a turn to call again, the same as for any tool.
func TestTerminateRefusesUnreadableArguments(t *testing.T) {
	_, err := NewTerminate().Call(context.Background(), json.RawMessage(`{"reason":`))
	if err == nil {
		t.Fatal("Call() = nil, want a failure")
	}
	var end *Termination
	if errors.As(err, &end) {
		t.Errorf("Call() = %v, want a failure the model can correct, not a termination", err)
	}
}

// The reason is the only thing the user is given, so it reaches standard error
// as the model wrote it. A run ended without one still has to say something.
func TestTerminationReportsTheModelsReason(t *testing.T) {
	given := &Termination{Reason: "the request asks for a machine this run cannot reach"}
	if got := given.Error(); got != given.Reason {
		t.Errorf("Error() = %q, want the reason verbatim", got)
	}

	silent := &Termination{}
	if got := silent.Error(); got == "" {
		t.Error("Error() is empty, so a run ended without a reason would say nothing at all")
	}
}

// The result is what a resumed session shows the model in place of the answer
// its call never got. It has to read as an ending either way.
func TestTerminationResultSaysTheRunEnded(t *testing.T) {
	given := &Termination{Reason: "nothing to answer"}
	got := given.Result()
	if !strings.Contains(got, "ended") {
		t.Errorf("Result() = %q, want it to say the run ended", got)
	}
	if !strings.Contains(got, given.Reason) {
		t.Errorf("Result() = %q, want the reason in it", got)
	}
	if silent := (&Termination{}).Result(); silent == "" {
		t.Error("Result() is empty, which is not a tool result a provider will accept")
	}
}

// The reason is optional in the schema, because a run has to be able to end
// even when nothing can be said about why. Requiring it would leave a model
// with nothing to say unable to call the tool at all.
func TestTerminateAsksForAReasonWithoutRequiringIt(t *testing.T) {
	var tool tools.Tool = NewTerminate()
	def := tool.Definition()
	if def.Name != "terminate" {
		t.Errorf("Name = %q, want %q", def.Name, "terminate")
	}
	if _, required := def.Parameters["required"]; required {
		t.Error("the schema requires a field, but the only one there is optional")
	}

	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Parameters have no properties: %+v", def.Parameters)
	}
	if _, ok := props["reason"]; !ok {
		t.Errorf("the schema has no reason field: %+v", props)
	}

	// The description is the whole of what a model reads about a call, so it
	// has to say that this is the end of the run rather than a step in it. It
	// is wrapped prose, so a phrase to look for may be split across two lines:
	// the words are what matters, not the layout.
	got := strings.Join(strings.Fields(strings.ToLower(def.Description)), " ")
	for _, want := range []string{"non-zero", "standard error"} {
		if !strings.Contains(got, want) {
			t.Errorf("description does not mention %q", want)
		}
	}
}
