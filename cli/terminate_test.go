package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/eventlog"
	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/session"
	"github.com/ChrisVilches/agi/tools"
	"github.com/ChrisVilches/agi/tools/controlflow"
)

// ending returns an assistant turn asking to end the run, with the reason the
// model gave for it.
func ending(id, reason string) model.AssistantMessage {
	args, err := json.Marshal(controlflow.TerminateParams{Reason: reason})
	if err != nil {
		panic(err)
	}
	return model.AssistantMessage{ToolRequests: []model.ToolRequest{
		{ID: id, Name: "terminate", Arguments: args},
	}}
}

// terminateRegistry is what a run that can end itself is given.
func terminateRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	return registryWith(t, controlflow.NewTerminate())
}

// A run the model ended reports the termination rather than an answer, and
// writes nothing at all to standard output: whatever is reading the other end
// of the pipe must see no data to go with the non-zero exit status.
func TestATerminatedRunAnswersNothing(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		ending("c1", "the prompt asks about a file that does not exist"),
	}}

	var out bytes.Buffer
	err := runAgent(context.Background(), pipedOutput(&out), nolog(), client, terminateRegistry(t), asked("summarize nofile.txt"), nil)

	var end *controlflow.Termination
	if !errors.As(err, &end) {
		t.Fatalf("runAgent() = %v, want a termination", err)
	}
	if end.Reason != "the prompt asks about a file that does not exist" {
		t.Errorf("reason = %q, want the model's own words", end.Reason)
	}
	if out.Len() != 0 {
		t.Errorf("output = %q, want nothing on the data channel", out.String())
	}
}

// The gateway is not asked again. The model's call is answered in the
// conversation because a stored conversation with an outstanding request is
// one a provider may refuse — but nothing reads that answer, so sending it
// would buy a round trip and a reply agi would throw away.
func TestATerminatedRunMakesNoFurtherRequest(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		ending("c1", "unintelligible"),
	}}

	var out bytes.Buffer
	conv := asked("?????")
	if err := runAgent(context.Background(), pipedOutput(&out), nolog(), client, terminateRegistry(t), conv, nil); err == nil {
		t.Fatal("runAgent() = nil, want a termination")
	}

	if len(client.sent) != 1 {
		t.Errorf("sent %d requests, want only the one that produced the call", len(client.sent))
	}
	// The answer is in the conversation all the same, which is what makes the
	// snapshot resumable.
	last, _ := conv.Last()
	result, ok := last.(model.ToolResultMessage)
	if !ok {
		t.Fatalf("last turn = %#v, want the terminate call answered", last)
	}
	if result.RequestID != "c1" {
		t.Errorf("answered %q, want the call that ended the run", result.RequestID)
	}
	if got := conv.PendingToolRequests(); len(got) != 0 {
		t.Errorf("conversation has %v outstanding, want nothing a provider could object to", got)
	}
}

// A schema run ends the same way, and never reaches the phase that would
// restate the answer: there is no answer to restate, and a wrongly shaped
// document is worse than none for whatever is reading the pipe.
func TestATerminatedSchemaRunProducesNoDocument(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		ending("c1", "the request cannot be understood"),
	}}
	schema, err := model.ParseSchema(`id:int:"User ID"`)
	if err != nil {
		t.Fatalf("ParseSchema() = %v", err)
	}

	var out bytes.Buffer
	var end *controlflow.Termination
	if err := runAgent(context.Background(), pipedOutput(&out), nolog(), client, terminateRegistry(t), asked("?????"), schema); !errors.As(err, &end) {
		t.Fatalf("runAgent() = %v, want a termination", err)
	}
	if out.Len() != 0 {
		t.Errorf("output = %q, want no document", out.String())
	}
	if len(client.sent) != 1 {
		t.Errorf("sent %d requests, want the schema phase never to have started", len(client.sent))
	}
}

// Everything the model asked for alongside the call that ended the run was
// never made, so the stored conversation answers those too and has nothing
// outstanding in it.
func TestATerminatedRunAnswersTheCallsItNeverMade(t *testing.T) {
	sess, err := session.Open(sessionDir(t, "chat"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	reply := ending("c1", "nothing to answer")
	reply.ToolRequests = append(reply.ToolRequests, model.ToolRequest{
		ID: "c2", Name: "probe", Arguments: json.RawMessage(`{}`),
	})
	client := &scriptedClient{replies: []model.AssistantMessage{reply}}
	probe := &fakeTool{name: "probe", output: "never runs"}

	var out bytes.Buffer
	err = runSession(context.Background(), pipedOutput(&out), nolog(), client,
		registryWith(t, controlflow.NewTerminate(), probe), sess, "?????", nil)
	var end *controlflow.Termination
	if !errors.As(err, &end) {
		t.Fatalf("runSession() = %v, want a termination", err)
	}
	if len(probe.calls) != 0 {
		t.Errorf("the other tool ran %d times, want the run to have ended before it", len(probe.calls))
	}

	stored, err := sess.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got := stored.PendingToolRequests(); len(got) != 0 {
		t.Errorf("stored conversation has %v outstanding, want one that can be sent as it is", got)
	}
	if err := stored.Validate(); err != nil {
		t.Errorf("Validate() = %v, want a conversation a later run can continue", err)
	}
	last, _ := stored.Last()
	failure, ok := last.(model.ToolFailure)
	if !ok || failure.RequestID != "c2" {
		t.Errorf("last stored turn = %#v, want the call that was never made", last)
	}
}

// The whole point of storing a terminated run: the user reads the reason on
// standard error, writes a better prompt, and runs agi again against the same
// session. The tool is not run again — tools never are on a resume — so the
// second run answers as any other would.
func TestATerminatedSessionIsResumedAndAnswers(t *testing.T) {
	dir := sessionDir(t, "chat")
	sess, err := session.Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	first := &scriptedClient{replies: []model.AssistantMessage{
		ending("c1", "the prompt does not say which file to read"),
	}}
	var out bytes.Buffer
	var end *controlflow.Termination
	if err := runSession(context.Background(), pipedOutput(&out), nolog(), first,
		terminateRegistry(t), sess, "summarize it", nil); !errors.As(err, &end) {
		t.Fatalf("runSession() = %v, want a termination", err)
	}

	reopened, err := session.Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	second := &scriptedClient{replies: replies("it is about goats")}
	out.Reset()
	if err := runSession(context.Background(), pipedOutput(&out), nolog(), second,
		terminateRegistry(t), reopened, "summarize goats.txt", nil); err != nil {
		t.Fatalf("runSession() = %v, want the second run to answer", err)
	}
	if got := out.String(); got != "it is about goats\n" {
		t.Errorf("output = %q, want the second answer", got)
	}

	// The second run sends the first exchange as it was stored: the call, the
	// answer saying the run ended, and then the new prompt. Nothing in that
	// asks for a tool, so nothing is run again.
	sent := second.sent[0].Conversation.Messages
	if len(sent) != 4 {
		t.Fatalf("second run sent %d turns, want the ended exchange plus the new prompt: %+v", len(sent), sent)
	}
	if a, ok := sent[1].(model.AssistantMessage); !ok || len(a.ToolRequests) != 1 {
		t.Errorf("turn 1 = %#v, want the call that ended the first run", sent[1])
	}
	result, ok := sent[2].(model.ToolResultMessage)
	if !ok || result.RequestID != "c1" {
		t.Errorf("turn 2 = %#v, want the stored answer to that call", sent[2])
	}
	if !strings.Contains(result.Content, "ended") {
		t.Errorf("stored answer = %q, want it to say the run ended", result.Content)
	}
	if u, ok := sent[3].(model.UserMessage); !ok || u.Content != "summarize goats.txt" {
		t.Errorf("turn 3 = %#v, want the new prompt", sent[3])
	}
}

// The log accounts for the run being ended, at the turn it reached and naming
// the call that did it, before the run finishes like any other.
func TestATerminationIsRecorded(t *testing.T) {
	sess, lg, read := recorded(t)
	client := &scriptedClient{replies: []model.AssistantMessage{
		ending("c1", "the request asks for a machine this run cannot reach"),
	}}

	var out bytes.Buffer
	var end *controlflow.Termination
	if err := runSession(context.Background(), pipedOutput(&out), lg, client,
		terminateRegistry(t), sess, "read /etc/hosts on prod", nil); !errors.As(err, &end) {
		t.Fatalf("runSession() = %v, want a termination", err)
	}

	events := read()
	assertKinds(t, events,
		eventlog.SystemPrompt,
		eventlog.Request, eventlog.Response,
		eventlog.ToolCall,
		eventlog.RunTerminate,
		eventlog.SessionSave,
	)
	got := only(events, eventlog.RunTerminate)[0]
	if got.Turn != 1 {
		t.Errorf("the termination was recorded at turn %d, want the turn the run reached", got.Turn)
	}
	if got.Tool != "terminate" || got.RequestID != "c1" {
		t.Errorf("the termination was recorded as %+v, want the call that ended the run", got)
	}
	if got.Reason != "the request asks for a machine this run cannot reach" {
		t.Errorf("recorded reason = %q, want the model's own words", got.Reason)
	}
}
