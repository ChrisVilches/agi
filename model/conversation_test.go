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
	c.AddToolFailure("c1", errors.New("no tool named \"nope\""))

	last, _ := c.Last()
	result, ok := last.(ToolResultMessage)
	if !ok {
		t.Fatalf("last message = %T, want ToolResultMessage", last)
	}
	if !result.Failed {
		t.Error("Failed = false, want true")
	}
	if !strings.Contains(result.Content, "no tool named") {
		t.Errorf("Content = %q, want the failure described", result.Content)
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
