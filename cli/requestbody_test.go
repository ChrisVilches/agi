package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/eventlog"
	"github.com/ChrisVilches/agi/hooks"
	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/prompt"
	"github.com/ChrisVilches/agi/session"
	"github.com/ChrisVilches/agi/transport"
)

// errBodyRefused is what refusingPayload fails with.
var errBodyRefused = errors.New("this backend cannot say what it would send")

// A run records no request body unless it was asked to. The body is the whole
// conversation and the default has to be silence, or every run would pay for a
// debugging aid nobody asked for.
func TestNoRequestBodyIsRecordedByDefault(t *testing.T) {
	sess, lg, read := recorded(t)
	client := &scriptedClient{replies: replies("4")}

	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), lg, client, registryWith(t), sess, prompt.Base(), "what is 2+2?", nil, nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	requests := only(read(), eventlog.ModelRequest)
	if len(requests) == 0 {
		t.Fatal("no request was recorded, so this proves nothing")
	}
	for i, e := range requests {
		if len(e.RequestBody) != 0 {
			t.Errorf("request %d recorded a body nobody asked for: %s", i+1, e.RequestBody)
		}
	}
}

// --log-request-body puts the body on every model.request line, as the object
// it is rather than a string holding one, so a reader can ask about what is
// inside it without unquoting it first.
func TestRequestBodyIsRecordedWhenAskedFor(t *testing.T) {
	sess, lg, read := recordedWith(t, true)
	client := &scriptedClient{replies: []model.AssistantMessage{
		{ToolRequests: []model.ToolRequest{{ID: "call_1", Name: "bash", Arguments: json.RawMessage(`{"command":"uname"}`)}}},
		model.NewAssistantMessage("it is Linux"),
	}}

	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), lg, client, registryWith(t, &fakeTool{name: "bash", output: "Linux"}), sess, prompt.Base(), "what system is this?", nil, nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	requests := only(read(), eventlog.ModelRequest)
	if len(requests) != 2 {
		t.Fatalf("recorded %d requests, want 2", len(requests))
	}
	for i, e := range requests {
		var body struct {
			Model    string `json:"model"`
			Messages int    `json:"messages"`
		}
		if err := json.Unmarshal(e.RequestBody, &body); err != nil {
			t.Fatalf("request %d recorded a body that is not a JSON object: %v", i+1, err)
		}
		if body.Model != "test-model" {
			t.Errorf("request %d recorded a body naming model %q", i+1, body.Model)
		}
	}

	// The body belongs to the request it is on, and a later request carries
	// more of the conversation than an earlier one: that growth is the whole
	// cost of the flag, and a body copied from one line to the next would
	// hide it.
	var first, second struct {
		Messages int `json:"messages"`
	}
	if err := json.Unmarshal(requests[0].RequestBody, &first); err != nil {
		t.Fatalf("unmarshalling the first body: %v", err)
	}
	if err := json.Unmarshal(requests[1].RequestBody, &second); err != nil {
		t.Fatalf("unmarshalling the second body: %v", err)
	}
	if second.Messages <= first.Messages {
		t.Errorf("the second request carried %d turns and the first %d; a later request carries more", second.Messages, first.Messages)
	}
}

// The body is rendered by the same method the backend sends with, so what the
// log holds is the request that went out and not a second rendering of it. The
// fake records what it was sent, which is what makes the comparison possible
// at all.
func TestTheRecordedBodyIsTheRequestThatWasSent(t *testing.T) {
	sess, lg, read := recordedWith(t, true)
	client := &scriptedClient{replies: replies("4")}

	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), lg, client, registryWith(t), sess, prompt.Base(), "what is 2+2?", nil, nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	requests := only(read(), eventlog.ModelRequest)
	if len(requests) != 1 || len(client.sent) != 1 {
		t.Fatalf("recorded %d requests and sent %d, want 1 of each", len(requests), len(client.sent))
	}
	want, err := client.Payload(client.sent[0])
	if err != nil {
		t.Fatalf("Payload() = %v", err)
	}
	if string(requests[0].RequestBody) != string(want) {
		t.Errorf("recorded body\n %s\nwant\n %s", requests[0].RequestBody, want)
	}
}

// The schema and exit-code phases each ask the model again, and each exchange
// is a request like any other: a log that held the bodies of the agent loop's
// requests and not of these would be missing the ones a reader most often
// turns the flag on for, since a rejected document is the thing being
// debugged.
func TestEveryPhaseRecordsItsRequestBody(t *testing.T) {
	sess, lg, read := recordedWith(t, true)
	client := &scriptedClient{replies: replies(
		"User 1 is Ada.",
		`{"id": 1, "name": "Ada"}`,
		`{"success": true}`,
	)}

	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), lg, client, registryWith(t), sess, prompt.Base(), "who is user 1?", testSchema(t), nil, true, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	events := read()
	requests := only(events, eventlog.ModelRequest)
	// The agent loop, the restatement, and the question about the outcome:
	// three exchanges, and three bodies.
	if len(requests) != 3 {
		t.Fatalf("recorded %d requests, want 3 (the loop, the restatement, the outcome)", len(requests))
	}
	for i, e := range requests {
		if len(e.RequestBody) == 0 {
			t.Errorf("request %d recorded no body", i+1)
		}
		if e.Backend == "" || e.Gateway == "" {
			t.Errorf("request %d does not say where it went: %+v", i+1, e)
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

// A subagent's requests are recorded on the same terms as the run's own. The
// flag is a property of the run's log, and Log.Agent carries it over, so
// nothing spawning an agent has to remember to pass it on.
func TestASubagentRecordsItsRequestBodyToo(t *testing.T) {
	sess, lg, read := recordedWith(t, true)
	sub := lg.Agent("agent-1")
	if !sub.RecordsRequestBody() {
		t.Fatal("a subagent's log does not record request bodies, so the run's trace has a hole in it")
	}
	if !lg.Fork("fork-1").RecordsRequestBody() {
		t.Error("a fork's log does not record request bodies")
	}

	client := &scriptedClient{replies: replies("4")}
	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), sub, client, registryWith(t), sess, prompt.Base(), "what is 2+2?", nil, nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	request := only(read(), eventlog.ModelRequest)[0]
	if request.Agent != "agent-1" {
		t.Fatalf("the request was recorded under %q, want %q", request.Agent, "agent-1")
	}
	if len(request.RequestBody) == 0 {
		t.Error("the subagent's request recorded no body")
	}
}

// --no-log and --log-request-body contradict each other and are refused
// together, rather than one of them being quietly dropped: a run whose most
// expensive flag did nothing looks exactly like a run that worked.
func TestSilenceAndRequestBodiesAreRefusedTogether(t *testing.T) {
	sess, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	_, err = sessionFlags{noLog: true, requestBody: true}.openLog(sess)
	if err == nil {
		t.Fatal("openLog() = nil, want a refusal: the two flags contradict each other")
	}
	for _, want := range []string{"--no-log", "--log-request-body"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s: %v", want, err)
		}
	}

	// Either flag on its own is fine, and --no-log on its own still buys
	// silence rather than a refusal.
	lg, err := sessionFlags{noLog: true}.openLog(sess)
	if err != nil {
		t.Fatalf("openLog() with only --no-log = %v, want a discarded log", err)
	}
	if lg.Recording() {
		t.Error("--no-log opened a log that records")
	}
}

// A body that cannot be rendered costs the line its body and nothing else. The
// request is still recorded, the run still goes on, and the reason reaches the
// log through the response that answers it — which is where failures are
// recorded, rather than twice in two phrasings.
func TestARequestIsRecordedEvenWhenItsBodyCannotBe(t *testing.T) {
	sess, lg, read := recordedWith(t, true)
	client := &refusingPayload{scriptedClient: scriptedClient{replies: replies("4")}}

	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), lg, client, registryWith(t), sess, prompt.Base(), "what is 2+2?", nil, nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want nil: a body that cannot be rendered must not stop a run", err)
	}

	requests := only(read(), eventlog.ModelRequest)
	if len(requests) != 1 {
		t.Fatalf("recorded %d requests, want 1", len(requests))
	}
	if len(requests[0].RequestBody) != 0 {
		t.Errorf("a body was recorded although rendering one failed: %s", requests[0].RequestBody)
	}
	if requests[0].Backend == "" || requests[0].Model == "" {
		t.Errorf("the request lost more than its body: %+v", requests[0])
	}
}

// refusingPayload is a backend that can answer a conversation but cannot say
// what it would send, which is the one case recordRequest has to survive.
type refusingPayload struct{ scriptedClient }

func (refusingPayload) Payload(transport.Request) (json.RawMessage, error) {
	return nil, errBodyRefused
}
