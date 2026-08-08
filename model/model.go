// Package model holds the core domain types agi operates on, independent of
// how they are transported to a provider or presented on the command line.
//
// A conversation is a sequence of three kinds of message, each with its own
// type rather than a single struct with optional fields: what a user may say,
// what the assistant may reply, and what a tool reports back. The types make
// the impossible states unrepresentable — a user turn cannot request a tool,
// and a tool result always answers a specific request.
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
	RoleTool      Role = "tool"
)

// Message is one turn in a Conversation. The set of implementations is closed:
// UserMessage, AssistantMessage, and ToolResultMessage.
type Message interface {
	// Role identifies who produced the message.
	Role() Role
	// Validate reports whether the message is well formed.
	Validate() error

	// isMessage keeps the set of message types closed to this package.
	isMessage()
}

// UserMessage is something the user said.
type UserMessage struct {
	Content string
}

// NewUserMessage returns a user turn.
func NewUserMessage(content string) UserMessage { return UserMessage{Content: content} }

// Role implements Message.
func (UserMessage) Role() Role { return RoleUser }

func (UserMessage) isMessage() {}

// Validate implements Message.
func (m UserMessage) Validate() error {
	if m.Content == "" {
		return fmt.Errorf("model: user message is empty")
	}
	return nil
}

// ToolDefinition describes a tool to the model. It is the only thing the model
// is told about a workspace or user-defined tool, which is why Description has
// to carry everything needed to call it correctly.
type ToolDefinition struct {
	Name        string
	Description string
	// Parameters is a JSON Schema object describing the tool's input.
	Parameters map[string]any
}

// ToolRequest is the assistant asking for one tool to be run. It is part of
// AssistantMessage rather than a message of its own, because that is how it
// reaches us: a single assistant turn may request several tools at once.
type ToolRequest struct {
	// ID correlates this request with the ToolResultMessage answering it.
	ID string
	// Name of the tool to run.
	Name string
	// Arguments is the JSON object the assistant produced for the tool's
	// schema. It stays raw so each tool decodes its own parameters.
	Arguments json.RawMessage
}

// Validate reports whether the request is well formed.
func (r ToolRequest) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("model: tool request for %q has no id", r.Name)
	}
	if r.Name == "" {
		return fmt.Errorf("model: tool request %q names no tool", r.ID)
	}
	return nil
}

// AssistantMessage is a reply from the model. It carries prose, tool requests,
// or both: a turn that only requests tools has no prose, and a turn with
// neither is the model saying nothing at all, which is an error.
type AssistantMessage struct {
	Content      string
	ToolRequests []ToolRequest
}

// NewAssistantMessage returns an assistant turn carrying only prose.
func NewAssistantMessage(content string) AssistantMessage {
	return AssistantMessage{Content: content}
}

// Role implements Message.
func (AssistantMessage) Role() Role { return RoleAssistant }

func (AssistantMessage) isMessage() {}

// WantsTools reports whether the assistant asked for any tool to be run, which
// is how a run knows it is not finished.
func (m AssistantMessage) WantsTools() bool { return len(m.ToolRequests) > 0 }

// Validate implements Message.
func (m AssistantMessage) Validate() error {
	if m.Content == "" && !m.WantsTools() {
		return fmt.Errorf("model: assistant message is empty")
	}
	seen := make(map[string]struct{}, len(m.ToolRequests))
	for _, r := range m.ToolRequests {
		if err := r.Validate(); err != nil {
			return err
		}
		if _, dup := seen[r.ID]; dup {
			return fmt.Errorf("model: duplicate tool request id %q", r.ID)
		}
		seen[r.ID] = struct{}{}
	}
	return nil
}

// ToolResultMessage reports what a tool produced, answering one ToolRequest.
type ToolResultMessage struct {
	// RequestID names the ToolRequest this answers.
	RequestID string
	// Content is what the tool produced, as the model should see it.
	Content string
	// Failed marks a tool that could not be run at all, as opposed to one that
	// ran and reported a problem itself. It is for display; the model reads
	// the same Content either way.
	Failed bool
}

// NewToolResult returns a result for the given request. Empty output is
// replaced with a placeholder, since a message with no content tells the model
// nothing and some providers reject it.
func NewToolResult(requestID, content string) ToolResultMessage {
	if content == "" {
		content = "(no output)"
	}
	return ToolResultMessage{RequestID: requestID, Content: content}
}

// NewToolFailure returns a result marking a tool that could not be run.
func NewToolFailure(requestID string, err error) ToolResultMessage {
	return ToolResultMessage{
		RequestID: requestID,
		Content:   "error: " + err.Error(),
		Failed:    true,
	}
}

// Role implements Message.
func (ToolResultMessage) Role() Role { return RoleTool }

func (ToolResultMessage) isMessage() {}

// Validate implements Message.
func (m ToolResultMessage) Validate() error {
	if m.RequestID == "" {
		return fmt.Errorf("model: tool result answers no request")
	}
	if m.Content == "" {
		return fmt.Errorf("model: tool result for %q is empty", m.RequestID)
	}
	return nil
}
