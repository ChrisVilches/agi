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

// stoppingClient replies from a script and stops the run — as Ctrl-C does,
// by cancelling the context it is running under — just before the reply
// numbered at. It is how these tests put a signal in the middle of a run
// without sending one.
type stoppingClient struct {
	testBackend
	replies []model.AssistantMessage
	at      int
	stop    context.CancelFunc
	sent    int
}

func (c *stoppingClient) Send(ctx context.Context, _ transport.Request) (transport.Response, error) {
	c.sent++
	if c.sent == c.at {
		c.stop()
		// What a transport makes of a cancelled request is its own business;
		// what matters is that the run notices why it failed.
		return transport.Response{}, ctx.Err()
	}
	if len(c.replies) == 0 {
		return transport.Response{}, errors.New("stoppingClient: ran out of replies")
	}
	next := c.replies[0]
	c.replies = c.replies[1:]
	return transport.Response{Message: next}, nil
}

// stoppingTool answers the call it was given and stops the run while doing it,
// which is how a signal arrives in the middle of a batch of tool calls.
type stoppingTool struct {
	fakeTool
	stop context.CancelFunc
}

func (s *stoppingTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	s.stop()
	return s.fakeTool.Call(ctx, args)
}

// killedTool is the other way a stopped run meets a tool: the command it was
// running is killed along with the run, so the tool comes back with a failure
// it never meant to report.
type killedTool struct {
	fakeTool
	stop context.CancelFunc
}

func (k *killedTool) Call(context.Context, json.RawMessage) (string, error) {
	k.stop()
	return "", errors.New("signal: killed")
}

// requesting returns an assistant turn asking for the named tool once per id.
func requesting(tool string, ids ...string) model.AssistantMessage {
	m := model.AssistantMessage{}
	for _, id := range ids {
		m.ToolRequests = append(m.ToolRequests, model.ToolRequest{
			ID: id, Name: tool, Arguments: json.RawMessage(`{}`),
		})
	}
	return m
}

// A run the user stopped ends the way any other run ends: the conversation it
// got as far as is stored, and the session is there to be continued.
func TestAnInterruptedRunIsStored(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := sessionDir(t, "chat")
	sess, err := session.Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	client := &stoppingClient{at: 1, stop: cancel}

	var out bytes.Buffer
	err = runSession(ctx, pipedOutput(&out), nolog(), client, registryWith(t), sess, prompt.Base(), "what is 2+2?", nil, nil, defaultMaxTurns, hooks.None())
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("runSession() = %v, want ErrInterrupted", err)
	}

	stored, err := sess.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if stored == nil || stored.Len() != 1 {
		t.Fatalf("stored %+v, want the prompt the run got as far as", stored)
	}
	if u, ok := stored.Messages[0].(model.UserMessage); !ok || u.Content != "what is 2+2?" {
		t.Errorf("stored turn 0 = %#v, want the prompt", stored.Messages[0])
	}
}

// Stopping in the middle of a batch of tool calls leaves requests the run
// never got to. They are answered as calls that could not be made, so the
// stored conversation has nothing outstanding in it and a later run can send
// it as it is.
func TestAnInterruptedRunAnswersTheCallsItNeverMade(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := sessionDir(t, "chat")
	sess, err := session.Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	tool := &stoppingTool{fakeTool: fakeTool{name: "bash", output: "Linux"}, stop: cancel}
	client := &stoppingClient{replies: []model.AssistantMessage{requesting("bash", "c1", "c2")}}

	var out bytes.Buffer
	err = runSession(ctx, pipedOutput(&out), nolog(), client, registryWith(t, tool), sess, prompt.Base(), "what system is this?", nil, nil, defaultMaxTurns, hooks.None())
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("runSession() = %v, want ErrInterrupted", err)
	}

	stored, err := sess.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got := stored.PendingToolRequests(); len(got) != 0 {
		t.Errorf("stored conversation has %v outstanding, want a conversation that can be sent as it is", got)
	}
	last, _ := stored.Last()
	failure, ok := last.(model.ToolFailure)
	if !ok {
		t.Fatalf("last stored turn = %T, want the call that was never made", last)
	}
	if failure.RequestID != "c2" {
		t.Errorf("abandoned %q, want the call the run never reached", failure.RequestID)
	}
	if err := stored.Validate(); err != nil {
		t.Errorf("Validate() = %v, want a conversation a later run can continue", err)
	}
}

// The stored conversation is resumed like any other. Its last turn is the
// prompt the interrupted run never answered, so the next run puts a second
// user turn after it, which no provider minds.
func TestAnInterruptedSessionIsResumedLikeAnyOther(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := sessionDir(t, "chat")
	sess, err := session.Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	var out bytes.Buffer
	if err := runSession(ctx, pipedOutput(&out), nolog(), &stoppingClient{at: 1, stop: cancel}, registryWith(t), sess, prompt.Base(), "what is 2+2?", nil, nil, defaultMaxTurns, hooks.None()); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("runSession() = %v, want ErrInterrupted", err)
	}

	reopened, err := session.Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	second := &scriptedClient{replies: replies("12")}
	out.Reset()
	if err := runSession(context.Background(), pipedOutput(&out), nolog(), second, registryWith(t), reopened, prompt.Base(), "and times 3?", nil, nil, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want the second run to answer", err)
	}
	if got := out.String(); got != "12\n" {
		t.Errorf("output = %q, want the second answer", got)
	}

	sent := second.sent[0].Conversation.Messages
	if len(sent) != 2 {
		t.Fatalf("second run sent %d turns, want the unanswered prompt and the new one: %+v", len(sent), sent)
	}
	for i, want := range []string{"what is 2+2?", "and times 3?"} {
		u, ok := sent[i].(model.UserMessage)
		if !ok || u.Content != want {
			t.Errorf("turn %d = %#v, want the user turn %q", i, sent[i], want)
		}
	}
}

// The log accounts for the interruption itself, and for every call the run
// abandoned because of it, before the run finishes like any other.
func TestAnInterruptionIsRecorded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess, lg, read := recorded(t)
	tool := &stoppingTool{fakeTool: fakeTool{name: "bash", output: "Linux"}, stop: cancel}
	client := &stoppingClient{replies: []model.AssistantMessage{requesting("bash", "c1", "c2")}}

	var out bytes.Buffer
	err := runSession(ctx, pipedOutput(&out), lg, client, registryWith(t, tool), sess, prompt.Base(), "what system is this?", nil, nil, defaultMaxTurns, hooks.None())
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("runSession() = %v, want ErrInterrupted", err)
	}

	events := read()
	assertKinds(t, events,
		eventlog.SystemPrompt,
		eventlog.ModelRequest, eventlog.ModelResponse,
		eventlog.ToolCall, eventlog.ToolOutput, eventlog.ToolResult,
		eventlog.ToolFailure,
		eventlog.RunInterrupt,
		eventlog.SessionSave,
	)
	if got := only(events, eventlog.ToolFailure)[0]; got.RequestID != "c2" || got.Reason != ErrInterrupted.Error() {
		t.Errorf("the abandoned call was recorded as %+v", got)
	}
	if got := only(events, eventlog.RunInterrupt)[0]; got.Turn != 1 {
		t.Errorf("the interruption was recorded at turn %d, want the turn the run had reached", got.Turn)
	}
}

// A run stopped before it started is stopped all the same: nothing is sent to
// the gateway, because the loop asks whether it may go on before it goes on.
func TestARunStoppedBeforeItStartsSendsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &scriptedClient{replies: replies("4")}
	var out bytes.Buffer
	err := runAgent(ctx, pipedOutput(&out), nolog(), client, registryWith(t), asked("what is 2+2?"), nil, defaultMaxTurns, hooks.None())
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("runAgent() = %v, want ErrInterrupted", err)
	}
	if len(client.sent) != 0 {
		t.Errorf("sent %d requests, want none: the run was over before it began", len(client.sent))
	}
}

// Interrupting the schema phase leaves the prose answer as the last word, the
// same as finishing it does: the scaffolding comes out whichever way the phase
// ends.
func TestAnInterruptedSchemaRunStoresTheProseAnswer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := sessionDir(t, "chat")
	sess, err := session.Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	// The first reply is the prose answer the loop ends on; the run is stopped
	// when it is asked to restate that answer as JSON.
	client := &stoppingClient{
		replies: []model.AssistantMessage{model.NewAssistantMessage("User 1 is Ada.")},
		at:      2,
		stop:    cancel,
	}

	var out bytes.Buffer
	err = runSession(ctx, pipedOutput(&out), nolog(), client, registryWith(t), sess, prompt.Base(), "who is user 1?", testSchema(t), nil, defaultMaxTurns, hooks.None())
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("runSession() = %v, want ErrInterrupted", err)
	}

	stored, err := sess.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if stored.Len() != 2 {
		t.Fatalf("stored %d turns, want the prompt and the prose answer: %+v", stored.Len(), stored.Messages)
	}
	answer, done := stored.FinalAnswer()
	if !done || answer != "User 1 is Ada." {
		t.Errorf("stored answer = %q, want the prose the run reasoned to", answer)
	}
	if out.String() != "" {
		t.Errorf("output = %q, want nothing on the pipe: there is no document", out.String())
	}
}

// A tool killed by the same signal that stopped the run did not refuse the
// call, and the model is not told it did: there is no next turn to correct
// anything in, and the reason stored is what actually happened.
func TestAToolKilledWithTheRunIsNotBlamedForIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := sessionDir(t, "chat")
	sess, err := session.Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	tool := &killedTool{fakeTool: fakeTool{name: "bash"}, stop: cancel}
	client := &stoppingClient{replies: []model.AssistantMessage{requesting("bash", "c1")}}

	var out bytes.Buffer
	if err := runSession(ctx, pipedOutput(&out), nolog(), client, registryWith(t, tool), sess, prompt.Base(), "what system is this?", nil, nil, defaultMaxTurns, hooks.None()); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("runSession() = %v, want ErrInterrupted", err)
	}

	stored, err := sess.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	last, _ := stored.Last()
	failure, ok := last.(model.ToolFailure)
	if !ok {
		t.Fatalf("last stored turn = %T, want the call answered", last)
	}
	if failure.Reason != ErrInterrupted.Error() {
		t.Errorf("stored reason = %q, want %q", failure.Reason, ErrInterrupted.Error())
	}
}

// An interruption is reported once. It reaches the user as the run's error,
// which main prints on standard error like any other; saying it again through
// the progress stream would print it twice at a terminal, where the two are
// the same screen.
func TestAnInterruptionIsNotAnnouncedInTheProgressStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out bytes.Buffer
	err := runAgent(ctx, humanOutput(&out), nolog(), &stoppingClient{at: 1, stop: cancel},
		registryWith(t), asked("what is 2+2?"), nil, defaultMaxTurns, hooks.None())
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("runAgent() = %v, want ErrInterrupted", err)
	}
	if strings.Contains(out.String(), ErrInterrupted.Error()) {
		t.Errorf("progress stream = %q, want nothing about the interruption", out.String())
	}
}
