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
	for _, r := range []Role{"", "tool", "User"} {
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
		{"empty content", Message{Role: RoleUser}, "empty content"},
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
