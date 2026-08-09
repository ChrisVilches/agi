package model

import "fmt"

// Conversation is the complete state of one run: the system prompt and every
// turn so far. It aggregates the message types and owns the rules about how
// they may follow one another, so the agent loop can ask what to do next
// instead of working it out from raw fields.
type Conversation struct {
	// System is the system prompt, sent ahead of the turns.
	System string
	// Messages are the turns, oldest first.
	Messages []Message
}

// NewConversation starts a run from a system prompt and the user's prompt.
func NewConversation(system, prompt string) *Conversation {
	return &Conversation{
		System:   system,
		Messages: []Message{NewUserMessage(prompt)},
	}
}

// Len returns the number of turns.
func (c *Conversation) Len() int { return len(c.Messages) }

// Append adds turns to the end of the conversation.
func (c *Conversation) Append(msgs ...Message) {
	c.Messages = append(c.Messages, msgs...)
}

// AddUser appends a user turn.
func (c *Conversation) AddUser(content string) {
	c.Append(NewUserMessage(content))
}

// AddAssistant appends an assistant turn.
func (c *Conversation) AddAssistant(m AssistantMessage) {
	c.Append(m)
}

// AddHarnessInstruction appends an instruction from agi to the model. Take a
// mark with Len first if the instruction is scaffolding that should be removed
// again, which it usually is.
func (c *Conversation) AddHarnessInstruction(content string) {
	c.Append(NewHarnessInstruction(content))
}

// DropFrom removes every turn from index i onward, which is how a harness
// instruction and the exchange it drove are taken back out of the
// conversation. They go together: the turns after the instruction exist only
// to answer it, and mean nothing once it is gone.
//
// The turn at i must be truncatable, and only a harness instruction is. That
// is what keeps the record intact — what the user asked and what the model
// answered can be added to but never rewritten, so a caller cannot use this to
// cut away a real turn, only scaffolding and what it produced.
//
// Dropping nothing is allowed, so a caller that took a mark can unwind to it
// whether or not anything was appended in between.
func (c *Conversation) DropFrom(i int) error {
	if i < 0 || i > len(c.Messages) {
		return fmt.Errorf("model: cannot drop from turn %d of %d", i, len(c.Messages))
	}
	if i == len(c.Messages) {
		return nil
	}
	if !c.Messages[i].Truncatable() {
		return fmt.Errorf("model: turn %d is a %T, which is a record and may not be dropped", i, c.Messages[i])
	}
	c.Messages = c.Messages[:i]
	return nil
}

// AddToolResult appends the output of a tool that ran.
func (c *Conversation) AddToolResult(requestID, content string) {
	c.Append(NewToolResult(requestID, content))
}

// AddToolFailure appends a result for a tool that could not be run. The
// failure is reported to the model rather than ending the run, because it can
// usually correct a bad call on the next turn.
func (c *Conversation) AddToolFailure(requestID string, err error) {
	c.Append(NewToolFailure(requestID, err))
}

// Last returns the most recent turn, and false if there is none.
func (c *Conversation) Last() (Message, bool) {
	if len(c.Messages) == 0 {
		return nil, false
	}
	return c.Messages[len(c.Messages)-1], true
}

// LastAssistant returns the most recent assistant turn, and false if the
// conversation does not end with one.
func (c *Conversation) LastAssistant() (AssistantMessage, bool) {
	last, ok := c.Last()
	if !ok {
		return AssistantMessage{}, false
	}
	m, ok := last.(AssistantMessage)
	return m, ok
}

// PendingToolRequests returns the tool requests waiting to be answered: those
// from the last assistant turn, when that turn is the most recent one. Once a
// result has been appended the conversation has moved on, so nothing is
// pending.
func (c *Conversation) PendingToolRequests() []ToolRequest {
	m, ok := c.LastAssistant()
	if !ok {
		return nil
	}
	return m.ToolRequests
}

// FinalAnswer returns the assistant's answer and true when the run is
// finished, which is when the last turn is an assistant turn that asks for no
// further tools.
func (c *Conversation) FinalAnswer() (string, bool) {
	m, ok := c.LastAssistant()
	if !ok || m.WantsTools() {
		return "", false
	}
	return m.Content, true
}

// Validate reports the first problem found, checking both each message and the
// order they appear in: every tool result must answer a request made by the
// assistant turn before it.
func (c *Conversation) Validate() error {
	// Requests that have been made and not yet answered.
	var pending map[string]struct{}

	for i, m := range c.Messages {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("message %d: %w", i, err)
		}

		switch msg := m.(type) {
		case AssistantMessage:
			pending = make(map[string]struct{}, len(msg.ToolRequests))
			for _, r := range msg.ToolRequests {
				pending[r.ID] = struct{}{}
			}
		case ToolResultMessage:
			if _, ok := pending[msg.RequestID]; !ok {
				return fmt.Errorf("message %d: tool result answers unknown request %q", i, msg.RequestID)
			}
			delete(pending, msg.RequestID)
		default:
			// A user turn or a harness instruction between a request and its
			// result would leave the request unanswered, so nothing stays
			// pending across one.
			pending = nil
		}
	}
	return nil
}
