package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/model"
)

// testSchema is the schema the restatement tests are written against.
func testSchema(t *testing.T) *model.Schema {
	t.Helper()
	s, err := model.ParseSchema(`id:int:"User ID", name:str:"Full name"`)
	if err != nil {
		t.Fatalf("ParseSchema() = %v", err)
	}
	return s
}

// answered returns a conversation that has already reached a prose answer,
// which is where the restatement phase picks up.
func answered(answer string) *model.Conversation {
	c := model.NewConversation("system", "who is user 1?")
	c.AddAssistant(model.NewAssistantMessage(answer))
	return c
}

// replies turns JSON documents into assistant turns.
func replies(documents ...string) []model.AssistantMessage {
	msgs := make([]model.AssistantMessage, 0, len(documents))
	for _, d := range documents {
		msgs = append(msgs, model.NewAssistantMessage(d))
	}
	return msgs
}

// assertUntouched checks that the conversation is back to the prose answer it
// started from, with nothing the harness added left in it.
func assertUntouched(t *testing.T, conv *model.Conversation, answer string) {
	t.Helper()
	if conv.Len() != 2 {
		t.Fatalf("conversation has %d turns, want the prompt and the answer: %+v", conv.Len(), conv.Messages)
	}
	last, done := conv.FinalAnswer()
	if !done || last != answer {
		t.Errorf("FinalAnswer() = %q, %v, want the prose answer %q", last, done, answer)
	}
	for i, m := range conv.Messages {
		if m.Truncatable() {
			t.Errorf("turn %d is a %T, want every harness instruction gone", i, m)
		}
	}
}

func TestRestateReturnsTheDocumentAndLeavesNoTrace(t *testing.T) {
	const document = `{"id": 1, "name": "Ada"}`
	client := &scriptedClient{replies: replies(document)}
	conv := answered("User 1 is Ada.")

	var out bytes.Buffer
	got, err := restate(context.Background(), humanOutput(&out), client, conv, testSchema(t))
	if err != nil {
		t.Fatalf("restate() = %v, want nil", err)
	}
	if got != document {
		t.Errorf("restate() = %q, want %q", got, document)
	}

	// The instruction the model was asked to follow must name the fields.
	sent := client.sent[0].Conversation.Messages
	instruction, ok := sent[len(sent)-1].(model.HarnessInstruction)
	if !ok {
		t.Fatalf("last turn sent = %T, want a harness instruction", sent[len(sent)-1])
	}
	for _, want := range []string{"id (int): User ID", "name (string): Full name", "JSON object"} {
		if !strings.Contains(instruction.Content, want) {
			t.Errorf("instruction = %q, want it to mention %q", instruction.Content, want)
		}
	}

	// Nothing is left to do, so nothing is offered to do it with.
	if len(client.sent[0].Tools) != 0 {
		t.Errorf("tools sent = %+v, want none during restatement", client.sent[0].Tools)
	}

	assertUntouched(t, conv, "User 1 is Ada.")
}

// A rejected attempt must come back to the model with the reason, so the next
// one has something to go on.
func TestRestateFeedsRejectionsBack(t *testing.T) {
	const document = `{"id": 1, "name": "Ada"}`
	client := &scriptedClient{replies: replies(
		"Sure! Here is the JSON:",    // not JSON at all
		`{"id": "1", "name": "Ada"}`, // right fields, wrong type
		document,
	)}
	conv := answered("User 1 is Ada.")

	var out bytes.Buffer
	got, err := restate(context.Background(), humanOutput(&out), client, conv, testSchema(t))
	if err != nil {
		t.Fatalf("restate() = %v, want nil", err)
	}
	if got != document {
		t.Errorf("restate() = %q, want %q", got, document)
	}
	if len(client.sent) != 3 {
		t.Fatalf("requests sent = %d, want one per attempt", len(client.sent))
	}

	// Each retry carries the rejected attempt and why it was rejected.
	second := client.sent[1].Conversation.Messages
	correction, ok := second[len(second)-1].(model.HarnessInstruction)
	if !ok {
		t.Fatalf("last turn of the second request = %T, want a harness instruction", second[len(second)-1])
	}
	if !strings.Contains(correction.Content, "invalid JSON") {
		t.Errorf("correction = %q, want it to say what was wrong", correction.Content)
	}
	if _, ok := second[len(second)-2].(model.AssistantMessage); !ok {
		t.Errorf("turn before the correction = %T, want the rejected attempt", second[len(second)-2])
	}

	third := client.sent[2].Conversation.Messages
	correction, ok = third[len(third)-1].(model.HarnessInstruction)
	if !ok {
		t.Fatalf("last turn of the third request = %T, want a harness instruction", third[len(third)-1])
	}
	if !strings.Contains(correction.Content, `field "id": want int, got a string`) {
		t.Errorf("correction = %q, want the schema violation spelled out", correction.Content)
	}

	assertUntouched(t, conv, "User 1 is Ada.")
}

// Every problem in one document is reported at once, so one more attempt can
// fix all of them.
func TestRestateReportsEveryProblemAtOnce(t *testing.T) {
	client := &scriptedClient{replies: replies(
		`{"id": 1.5, "extra": true}`,
		`{"id": 1, "name": "Ada"}`,
	)}
	conv := answered("User 1 is Ada.")

	var out bytes.Buffer
	if _, err := restate(context.Background(), humanOutput(&out), client, conv, testSchema(t)); err != nil {
		t.Fatalf("restate() = %v, want nil", err)
	}

	second := client.sent[1].Conversation.Messages
	correction := second[len(second)-1].(model.HarnessInstruction)
	for _, want := range []string{`missing field "name"`, `unexpected field "extra"`, "want int, got the number 1.5"} {
		if !strings.Contains(correction.Content, want) {
			t.Errorf("correction = %q, want it to mention %q", correction.Content, want)
		}
	}
}

// A turn with no prose is not an answer; it costs an attempt and is explained.
func TestRestateTreatsAnEmptyReplyAsAFailedAttempt(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		{ToolRequests: []model.ToolRequest{{ID: "c1", Name: "bash"}}},
		model.NewAssistantMessage(`{"id": 1, "name": "Ada"}`),
	}}
	conv := answered("User 1 is Ada.")

	var out bytes.Buffer
	if _, err := restate(context.Background(), humanOutput(&out), client, conv, testSchema(t)); err != nil {
		t.Fatalf("restate() = %v, want nil", err)
	}

	// The tool request is not recorded: there was no tool on offer, and the
	// turn carried no answer to keep.
	second := client.sent[1].Conversation.Messages
	for i, m := range second {
		if a, ok := m.(model.AssistantMessage); ok && a.WantsTools() {
			t.Errorf("turn %d = %+v, want the tool request left out", i, a)
		}
	}
	correction := second[len(second)-1].(model.HarnessInstruction)
	if !strings.Contains(correction.Content, "no JSON") {
		t.Errorf("correction = %q, want it to say the reply held no JSON", correction.Content)
	}
}

// A model that never gets it right must fail the run rather than hand a
// wrongly shaped document to whatever is reading the pipe.
func TestRestateGivesUpAfterMaxAttempts(t *testing.T) {
	bad := make([]string, maxSchemaAttempts+1)
	for i := range bad {
		bad[i] = `{"id": "not a number", "name": "Ada"}`
	}
	client := &scriptedClient{replies: replies(bad...)}
	conv := answered("User 1 is Ada.")

	var out bytes.Buffer
	got, err := restate(context.Background(), humanOutput(&out), client, conv, testSchema(t))
	if err == nil {
		t.Fatalf("restate() = %q, want an error", got)
	}
	if !strings.Contains(err.Error(), "10 attempts") {
		t.Errorf("restate() = %v, want it to say how many attempts were spent", err)
	}
	var invalid *model.ValidationError
	if !errors.As(err, &invalid) {
		t.Errorf("restate() = %v, want the last rejection wrapped", err)
	}
	if len(client.sent) != maxSchemaAttempts {
		t.Errorf("requests sent = %d, want the %d-attempt cap", len(client.sent), maxSchemaAttempts)
	}

	// A failed run still leaves the conversation as it found it.
	assertUntouched(t, conv, "User 1 is Ada.")
}

// A gateway that fails ends the run, and still leaves nothing behind.
func TestRestatePropagatesTransportError(t *testing.T) {
	client := &scriptedClient{err: errors.New("gateway exploded")}
	conv := answered("User 1 is Ada.")

	var out bytes.Buffer
	_, err := restate(context.Background(), humanOutput(&out), client, conv, testSchema(t))
	if err == nil || !strings.Contains(err.Error(), "gateway exploded") {
		t.Fatalf("restate() = %v, want the transport error", err)
	}
	assertUntouched(t, conv, "User 1 is Ada.")
}

// Piped, a schema run carries the JSON and nothing else — not the prose answer
// that led to it, not the attempts.
func TestSchemaRunPipedPrintsOnlyTheDocument(t *testing.T) {
	const document = `{"id": 1, "name": "Ada"}`
	client := &scriptedClient{replies: replies(
		"User 1 is Ada, and she is active.",
		`{"id": "1", "name": "Ada"}`,
		document,
	)}

	var out bytes.Buffer
	err := runAgent(context.Background(), pipedOutput(&out), client, registryWith(t), "who is user 1?", testSchema(t))
	if err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}
	if got := out.String(); got != document+"\n" {
		t.Errorf("output = %q, want only the document", got)
	}
}

// At a terminal the whole process is on show: the reasoning, the attempts, and
// the document.
func TestSchemaRunAtATerminalShowsTheWholeProcess(t *testing.T) {
	const document = `{"id": 1, "name": "Ada"}`
	client := &scriptedClient{replies: replies(
		"User 1 is Ada, and she is active.",
		`{"id": "1", "name": "Ada"}`,
		document,
	)}

	var out bytes.Buffer
	err := runAgent(context.Background(), humanOutput(&out), client, registryWith(t), "who is user 1?", testSchema(t))
	if err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}
	for _, want := range []string{
		"User 1 is Ada, and she is active.",
		"attempt 1 of 10",
		"attempt 2 of 10",
		`field "id": want int, got a string`,
		document,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %q, want it to show %q", out.String(), want)
		}
	}
}

// Without a schema the run is exactly what it was: prose, printed as the answer.
func TestRunWithoutSchemaIsUnchanged(t *testing.T) {
	client := &scriptedClient{replies: replies("User 1 is Ada.")}

	var out bytes.Buffer
	if err := runAgent(context.Background(), pipedOutput(&out), client, registryWith(t), "who is user 1?", nil); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}
	if got := out.String(); got != "User 1 is Ada.\n" {
		t.Errorf("output = %q, want the prose answer", got)
	}
	if len(client.sent) != 1 {
		t.Errorf("requests sent = %d, want no restatement", len(client.sent))
	}
}
