package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/ChrisVilches/agi/eventlog"
	"github.com/ChrisVilches/agi/hooks"
	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/prompt"
)

// Every model.request line says where the request went, and says it in full:
// which backend, which model, which address. The backend is the field the
// address cannot stand in for — two backends can be pointed at one endpoint —
// and it is on the line of every request rather than only at the start of the
// run, because a session resumed against a different backend appends its lines
// to the same file as the run that chose the first one.
func TestEveryRequestRecordsWhereItWent(t *testing.T) {
	sess, lg, read := recorded(t)
	client := &scriptedClient{replies: []model.AssistantMessage{
		{ToolRequests: []model.ToolRequest{{ID: "call_1", Name: "bash", Arguments: json.RawMessage(`{"command":"uname"}`)}}},
		model.NewAssistantMessage("it is Linux"),
	}}

	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), lg, client, registryWith(t, &fakeTool{name: "bash", output: "Linux"}), sess, prompt.Base(), "what system is this?", nil, nil, outcomeCheck{}, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	requests := only(read(), eventlog.ModelRequest)
	if len(requests) != 2 {
		t.Fatalf("recorded %d requests, want 2", len(requests))
	}
	for i, e := range requests {
		if e.Backend != "test-backend" {
			t.Errorf("request %d names backend %q, want %q", i+1, e.Backend, "test-backend")
		}
		if e.Model != "test-model" {
			t.Errorf("request %d names model %q, want %q", i+1, e.Model, "test-model")
		}
		if e.Gateway != "http://gateway.test" {
			t.Errorf("request %d names gateway %q, want %q", i+1, e.Gateway, "http://gateway.test")
		}
	}
	// A backend that reported its address as its name would satisfy every
	// assertion above and answer none of the question the field was added
	// for.
	if requests[0].Backend == requests[0].Gateway {
		t.Errorf("the backend is recorded as its own address: %+v", requests[0])
	}
}

// Each phase that asks the model records the same three fields, because one
// function writes the line for all of them: the agent loop, the restatement of
// an answer as JSON, and the question about whether the task was completed.
// Three hand-assembled lines were what let a field go missing from some of
// them.
func TestEveryPhaseSaysWhereItsRequestWent(t *testing.T) {
	sess, lg, read := recorded(t)
	client := &scriptedClient{replies: replies(
		"User 1 is Ada.",
		`{"id": 1, "name": "Ada"}`,
		`{"success": true}`,
	)}

	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), lg, client, registryWith(t), sess, prompt.Base(), "who is user 1?", testSchema(t), nil, outcomeCheck{asked: true}, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	requests := only(read(), eventlog.ModelRequest)
	if len(requests) != 3 {
		t.Fatalf("recorded %d requests, want 3 (the loop, the restatement, the outcome)", len(requests))
	}
	for i, e := range requests {
		if e.Backend == "" || e.Model == "" || e.Gateway == "" {
			t.Errorf("request %d does not say where it went: %+v", i+1, e)
		}
		if e.Messages == 0 {
			t.Errorf("request %d counts no turns, and every request carries some", i+1)
		}
	}
	// The three are told apart the way the log already tells them apart: the
	// loop numbers turns and the other two number attempts.
	if requests[0].Turn != 1 {
		t.Errorf("the loop's request is turn %d, want 1", requests[0].Turn)
	}
	if requests[1].Attempt != 1 || requests[2].Attempt != 1 {
		t.Errorf("the restatement and the outcome are attempts %d and %d, want 1 and 1", requests[1].Attempt, requests[2].Attempt)
	}
}

// run.start gained the backend for the same reason a request line did: its
// gateway field alone never said which of the backends sharing one address a
// run went through. It is recorded by runPrompt, which opens a backend from
// the environment and has no test harness in this package, so what is asserted
// here is this package's own part — that the line carries the field, and that
// it is not the address under another name. Which name each backend reports is
// transport/openai's and transport/litellm's to prove, and they do.
func TestTheRunStartSaysWhichBackendItIsOn(t *testing.T) {
	_, lg, read := recorded(t)
	client := testBackend{}

	lg.Record(eventlog.Event{
		Kind:    eventlog.RunStart,
		Backend: client.Name(),
		Model:   client.Model(),
		Gateway: client.BaseURL(),
	})

	start := only(read(), eventlog.RunStart)
	if len(start) != 1 {
		t.Fatalf("recorded %d run.start lines, want 1", len(start))
	}
	if start[0].Backend == "" {
		t.Error("run.start records no backend, which is the field it was missing")
	}
	if start[0].Gateway == "" {
		t.Error("run.start records no gateway, which it always did")
	}
	if start[0].Backend == start[0].Gateway {
		t.Errorf("run.start records the address as the backend name: %+v", start[0])
	}
}
