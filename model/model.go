// Package model holds the core domain types agi operates on, independent of
// how they are transported to a provider or presented on the command line.
package model

import (
	"encoding/json"
	"fmt"
)

// Role identifies who produced a Message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	// RoleTool carries the output of a tool back to the model. A message with
	// this role must name the call it answers in ToolCallID.
	RoleTool Role = "tool"
)

// Valid reports whether r is a role agi knows about.
func (r Role) Valid() bool {
	switch r {
	case RoleSystem, RoleUser, RoleAssistant, RoleTool:
		return true
	default:
		return false
	}
}

// ToolCall is a request from the model to run one tool.
type ToolCall struct {
	// ID correlates this call with the RoleTool message answering it.
	ID string `json:"id"`
	// Name of the tool to run.
	Name string `json:"name"`
	// Arguments is the JSON object the model produced for the tool's schema.
	// It is kept raw so each tool decodes its own parameters.
	Arguments json.RawMessage `json:"arguments"`
}

// ToolDefinition describes a tool to the model. It is the only thing the model
// is told about a tool, which is why Description has to carry everything the
// model needs in order to call it correctly.
type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Parameters is a JSON Schema object describing the tool's input.
	Parameters map[string]any `json:"parameters"`
}

// Message is a single turn in a Conversation.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
	// ToolCalls is set on an assistant message that wants tools run. Such a
	// message may have empty Content.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID is set on a RoleTool message, naming the call it answers.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// Validate reports whether the message is well formed.
func (m Message) Validate() error {
	if !m.Role.Valid() {
		return fmt.Errorf("model: unknown role %q", m.Role)
	}

	if m.Role == RoleTool {
		if m.ToolCallID == "" {
			return fmt.Errorf("model: tool message has no tool call id")
		}
		if len(m.ToolCalls) > 0 {
			return fmt.Errorf("model: tool message must not request further calls")
		}
		return nil
	}

	if m.ToolCallID != "" {
		return fmt.Errorf("model: %q message must not carry a tool call id", m.Role)
	}
	// An assistant turn that only requests tools carries no prose, so content is
	// required only when there is nothing else in the message.
	if m.Content == "" && len(m.ToolCalls) == 0 {
		return fmt.Errorf("model: message with role %q is empty", m.Role)
	}
	if len(m.ToolCalls) > 0 && m.Role != RoleAssistant {
		return fmt.Errorf("model: %q message must not request tool calls", m.Role)
	}
	return nil
}

// Conversation is an ordered sequence of messages.
type Conversation struct {
	Messages []Message `json:"messages"`
}

// Append adds a message to the end of the conversation.
func (c *Conversation) Append(msgs ...Message) {
	c.Messages = append(c.Messages, msgs...)
}

// Len returns the number of messages in the conversation.
func (c *Conversation) Len() int { return len(c.Messages) }

// Validate reports the first problem found in the conversation, if any.
func (c *Conversation) Validate() error {
	for i, m := range c.Messages {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("message %d: %w", i, err)
		}
	}
	return nil
}
