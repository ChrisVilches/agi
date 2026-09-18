package model

import (
	"strings"
	"testing"
)

func TestRoleValid(t *testing.T) {
	for _, r := range []Role{RoleSystem, RoleUser, RoleAssistant} {
		if !r.Valid() {
			t.Errorf("Role(%q).Valid() = false, want true", r)
		}
	}
	for _, r := range []Role{"", "agent", "User"} {
		if r.Valid() {
			t.Errorf("Role(%q).Valid() = true, want false", r)
		}
	}
}

func TestMessageValidate(t *testing.T) {
	tests := []struct {
		name    string
		msg     Message
		wantErr string
	}{
		{"ok", Message{Role: RoleUser, Content: "hi"}, ""},
		{"bad role", Message{Role: "nope", Content: "hi"}, "unknown role"},
		{"empty content", Message{Role: RoleUser}, "is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.msg.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("Validate() = nil, want error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestConversationAppendAndLen(t *testing.T) {
	var c Conversation
	if c.Len() != 0 {
		t.Fatalf("Len() on zero value = %d, want 0", c.Len())
	}

	c.Append(Message{Role: RoleUser, Content: "one"})
	c.Append(
		Message{Role: RoleAssistant, Content: "two"},
		Message{Role: RoleUser, Content: "three"},
	)
	if got := c.Len(); got != 3 {
		t.Fatalf("Len() = %d, want 3", got)
	}
	if got := c.Messages[2].Content; got != "three" {
		t.Errorf("last message = %q, want %q", got, "three")
	}
}

// Validate must report which message failed, not just that one did.
func TestConversationValidateReportsIndex(t *testing.T) {
	c := Conversation{Messages: []Message{
		{Role: RoleUser, Content: "ok"},
		{Role: "bogus", Content: "bad"},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want an error")
	}
	if !strings.Contains(err.Error(), "message 1") {
		t.Errorf("Validate() = %v, want the failing index in the message", err)
	}

	valid := Conversation{Messages: []Message{{Role: RoleUser, Content: "ok"}}}
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate() on a valid conversation = %v, want nil", err)
	}
}

func TestToolMessageValidation(t *testing.T) {
	tests := []struct {
		name    string
		msg     Message
		wantErr string
	}{
		{
			"tool result is valid",
			Message{Role: RoleTool, ToolCallID: "call-1", Content: "output"},
			"",
		},
		{
			"tool result without an id",
			Message{Role: RoleTool, Content: "output"},
			"no tool call id",
		},
		{
			"assistant asking for tools needs no content",
			Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c", Name: "bash"}}},
			"",
		},
		{
			"assistant with neither content nor calls",
			Message{Role: RoleAssistant},
			"is empty",
		},
		{
			"user must not carry a tool call id",
			Message{Role: RoleUser, Content: "hi", ToolCallID: "call-1"},
			"must not carry a tool call id",
		},
		{
			"user must not request tool calls",
			Message{Role: RoleUser, Content: "hi", ToolCalls: []ToolCall{{ID: "c", Name: "bash"}}},
			"must not request tool calls",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.msg.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("Validate() = nil, want an error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("Validate() = %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestRoleToolIsValid(t *testing.T) {
	if !RoleTool.Valid() {
		t.Error("RoleTool.Valid() = false, want true")
	}
}
