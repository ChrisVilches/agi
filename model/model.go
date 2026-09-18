// Package model holds the core domain types agi operates on, independent of
// how they are transported to a provider or presented on the command line.
package model

import "fmt"

// Role identifies who produced a Message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Valid reports whether r is a role agi knows about.
func (r Role) Valid() bool {
	switch r {
	case RoleSystem, RoleUser, RoleAssistant:
		return true
	default:
		return false
	}
}

// Message is a single turn in a Conversation.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// Validate reports whether the message is well formed.
func (m Message) Validate() error {
	if !m.Role.Valid() {
		return fmt.Errorf("model: unknown role %q", m.Role)
	}
	if m.Content == "" {
		return fmt.Errorf("model: message with role %q has empty content", m.Role)
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
