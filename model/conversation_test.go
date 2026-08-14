package model

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func requestingReply(ids ...string) AssistantMessage {
	m := AssistantMessage{}
	for _, id := range ids {
		m.ToolRequests = append(m.ToolRequests, ToolRequest{
			ID: id, Name: "bash", Arguments: json.RawMessage(`{}`),
		})
	}
	return m
}

func TestNewConversationStartsWithThePrompt(t *testing.T) {
	c := NewConversation("be helpful", "do a thing")

	if c.System != "be helpful" {
		t.Errorf("System = %q, want the system prompt", c.System)
	}
	if c.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", c.Len())
	}
	first, ok := c.Messages[0].(UserMessage)
	if !ok {
		t.Fatalf("first message = %T, want UserMessage", c.Messages[0])
	}
	if first.Content != "do a thing" {
		t.Errorf("first message = %q, want the prompt", first.Content)
	}
}

// A run is finished when the assistant replies without asking for tools.
func TestFinalAnswer(t *testing.T) {
	c := NewConversation("s", "p")
	if _, done := c.FinalAnswer(); done {
		t.Error("FinalAnswer() reports done before the assistant has replied")
	}

	c.AddAssistant(requestingReply("c1"))
	if _, done := c.FinalAnswer(); done {
		t.Error("FinalAnswer() reports done while a tool request is pending")
	}

	c.AddToolResult("c1", "output")
	if _, done := c.FinalAnswer(); done {
		t.Error("FinalAnswer() reports done after a tool result, before the model replied")
	}

	c.AddAssistant(NewAssistantMessage("all done"))
	answer, done := c.FinalAnswer()
	if !done {
		t.Fatal("FinalAnswer() = not done, want done")
	}
	if answer != "all done" {
		t.Errorf("answer = %q, want %q", answer, "all done")
	}
}

func TestPendingToolRequests(t *testing.T) {
	c := NewConversation("s", "p")
	if got := c.PendingToolRequests(); len(got) != 0 {
		t.Errorf("PendingToolRequests() = %v, want none at the start", got)
	}

	c.AddAssistant(requestingReply("c1", "c2"))
	if got := c.PendingToolRequests(); len(got) != 2 {
		t.Fatalf("PendingToolRequests() = %d, want 2", len(got))
	}

	// Once results are appended the conversation has moved on.
	c.AddToolResult("c1", "one")
	c.AddToolResult("c2", "two")
	if got := c.PendingToolRequests(); len(got) != 0 {
		t.Errorf("PendingToolRequests() = %v, want none after answering", got)
	}
}

func TestAddToolFailureIsMarkedAndVisibleToTheModel(t *testing.T) {
	c := NewConversation("s", "p")
	c.AddAssistant(requestingReply("c1"))
	returned := c.AddToolFailure("c1", errors.New("no tool named \"nope\""))

	last, _ := c.Last()
	failure, ok := last.(ToolFailure)
	if !ok {
		t.Fatalf("last message = %T, want ToolFailure: a refused call is not an answer", last)
	}
	if returned != failure {
		t.Errorf("AddToolFailure() = %+v, want the turn it appended: %+v", returned, failure)
	}
	if !strings.Contains(failure.Render(), "no tool named") {
		t.Errorf("Render() = %q, want the failure described", failure.Render())
	}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v, want a failure to be a valid turn", err)
	}
}

func TestLastAssistant(t *testing.T) {
	c := NewConversation("s", "p")
	if _, ok := c.LastAssistant(); ok {
		t.Error("LastAssistant() = ok on a conversation ending with the user turn")
	}

	c.AddAssistant(NewAssistantMessage("hi"))
	if m, ok := c.LastAssistant(); !ok || m.Content != "hi" {
		t.Errorf("LastAssistant() = %+v, %v; want the assistant turn", m, ok)
	}

	c.AddUser("more")
	if _, ok := c.LastAssistant(); ok {
		t.Error("LastAssistant() = ok after a later user turn")
	}
}

func TestLastOnEmptyConversation(t *testing.T) {
	var c Conversation
	if _, ok := c.Last(); ok {
		t.Error("Last() = ok on an empty conversation")
	}
	if _, done := c.FinalAnswer(); done {
		t.Error("FinalAnswer() = done on an empty conversation")
	}
}

func TestValidateOrdering(t *testing.T) {
	t.Run("a valid exchange", func(t *testing.T) {
		c := NewConversation("s", "p")
		c.AddAssistant(requestingReply("c1"))
		c.AddToolResult("c1", "output")
		c.AddAssistant(NewAssistantMessage("done"))
		if err := c.Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil", err)
		}
	})

	t.Run("a result answering nothing", func(t *testing.T) {
		c := NewConversation("s", "p")
		c.AddToolResult("ghost", "output")
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "unknown request") {
			t.Errorf("Validate() = %v, want an unknown-request error", err)
		}
	})

	t.Run("a result answering the wrong request", func(t *testing.T) {
		c := NewConversation("s", "p")
		c.AddAssistant(requestingReply("c1"))
		c.AddToolResult("c2", "output")
		if err := c.Validate(); err == nil {
			t.Error("Validate() = nil, want an error for a mismatched id")
		}
	})

	t.Run("a result answered twice", func(t *testing.T) {
		c := NewConversation("s", "p")
		c.AddAssistant(requestingReply("c1"))
		c.AddToolResult("c1", "output")
		c.AddToolResult("c1", "output again")
		if err := c.Validate(); err == nil {
			t.Error("Validate() = nil, want an error for answering a request twice")
		}
	})

	t.Run("it reports which message failed", func(t *testing.T) {
		c := NewConversation("s", "p")
		c.Append(UserMessage{})
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "message 1") {
			t.Errorf("Validate() = %v, want the failing index", err)
		}
	})
}

// A harness instruction and everything it caused are dropped together, leaving
// the record of what was asked and answered as it was.
func TestDropFrom(t *testing.T) {
	c := NewConversation("system", "what is the status?")
	c.AddAssistant(NewAssistantMessage("everything is fine"))

	mark := c.Len()
	c.AddHarnessInstruction("answer in JSON")
	c.AddAssistant(NewAssistantMessage(`{"ok": nope}`))
	c.AddHarnessInstruction("that was rejected")
	c.AddAssistant(NewAssistantMessage(`{"ok": true}`))

	if err := c.DropFrom(mark); err != nil {
		t.Fatalf("DropFrom(%d) = %v, want nil", mark, err)
	}
	if c.Len() != mark {
		t.Fatalf("Len() = %d, want %d", c.Len(), mark)
	}

	// The prose answer must be the last word, with nothing about JSON after it.
	answer, done := c.FinalAnswer()
	if !done || answer != "everything is fine" {
		t.Errorf("FinalAnswer() = %q, %v, want the prose answer", answer, done)
	}
	for i, m := range c.Messages {
		if m.Truncatable() {
			t.Errorf("message %d is a %T, want the scaffolding gone", i, m)
		}
	}
}

// Dropping is only ever allowed to cut scaffolding, never the record.
func TestDropFromRefusesToCutTheRecord(t *testing.T) {
	c := NewConversation("system", "prompt")
	c.AddAssistant(NewAssistantMessage("answer"))
	c.AddHarnessInstruction("restate it")

	tests := []struct {
		name string
		at   int
	}{
		{"the user's prompt", 0},
		{"the model's answer", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := c.DropFrom(tt.at); err == nil {
				t.Fatalf("DropFrom(%d) = nil, want an error", tt.at)
			}
			if c.Len() != 3 {
				t.Fatalf("Len() = %d, want the conversation untouched", c.Len())
			}
		})
	}

	// Out of range is a caller's mistake, not a silent no-op.
	if err := c.DropFrom(4); err == nil {
		t.Error("DropFrom(4) of 3 turns = nil, want an error")
	}
	if err := c.DropFrom(-1); err == nil {
		t.Error("DropFrom(-1) = nil, want an error")
	}

	// Unwinding a mark that nothing was appended after drops nothing.
	if err := c.DropFrom(c.Len()); err != nil {
		t.Errorf("DropFrom(Len()) = %v, want nil", err)
	}
	if c.Len() != 3 {
		t.Errorf("Len() = %d, want the conversation untouched", c.Len())
	}
}

// A harness instruction is a well-formed turn wherever a user turn would be.
func TestConversationValidateWithHarnessInstruction(t *testing.T) {
	c := NewConversation("system", "prompt")
	c.AddAssistant(NewAssistantMessage("answer"))
	c.AddHarnessInstruction("restate it as JSON")
	c.AddAssistant(NewAssistantMessage(`{"ok": true}`))

	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}
