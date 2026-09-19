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

// AddHarnessInstruction appends an instruction from agi to the model. An
// instruction may be left in place for the rest of the run; to remove it again
// once it has served its purpose, take a mark with Len before appending and
// unwind to it with DropFrom.
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
// cut away a real turn, only scaffolding and what it produced. Nothing here
// says scaffolding has to be dropped; this is only the one way of doing it.
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

// AddSubagentResults appends the answers to one subagents request, keeping
// them apart rather than storing the text they render to.
func (c *Conversation) AddSubagentResults(requestID string, results []SubagentResult) {
	c.Append(NewSubagentResults(requestID, results))
}

// AddToolFailure appends the answer to a tool call that could not be run, and
// returns it. The failure is reported to the model rather than ending the run,
// because it can usually correct a bad call on the next turn.
//
// It returns the turn it appended so that a caller showing the user what
// happened can show the words the model is reading, rather than assembling its
// own version of them.
func (c *Conversation) AddToolFailure(requestID string, err error) ToolFailure {
	m := NewToolFailure(requestID, err)
	c.Append(m)
	return m
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

// PendingToolRequests returns the tool requests still waiting to be answered:
// those made by the most recent assistant turn that nothing after it has
// answered yet. A request answered by a result or by a failure is settled
// either way, so a turn that asked for three tools and has had two of them
// answered leaves one pending.
//
// The rule is stated over the turns rather than over the loop that is running
// them, because a run does not always get to the end of that loop. A run cut
// short partway through a batch of calls has to know which of them it never
// got to, and asking the conversation is the only way to find out that does
// not depend on where the loop happened to be.
func (c *Conversation) PendingToolRequests() []ToolRequest {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		m, ok := c.Messages[i].(AssistantMessage)
		if !ok {
			continue
		}

		answered := make(map[string]struct{}, len(m.ToolRequests))
		for _, later := range c.Messages[i+1:] {
			if a, ok := later.(ToolAnswer); ok {
				answered[a.AnsweredRequest()] = struct{}{}
			}
		}

		var pending []ToolRequest
		for _, r := range m.ToolRequests {
			if _, done := answered[r.ID]; !done {
				pending = append(pending, r)
			}
		}
		return pending
	}
	return nil
}

// AbandonToolRequests answers every request still pending with a failure
// giving reason, and returns the failures it appended, in the order the
// requests were made.
//
// It is for a run that stops before it has finished: interrupted, above all.
// A conversation left with requests nobody answered is not one a later run can
// continue — a provider is entitled to reject a turn that moves on while a
// call it asked for is still outstanding — so the calls that were never made
// are recorded as calls that could not be made, which is exactly what a
// ToolFailure is. The record stays intact: nothing is rewritten or dropped,
// and the reason says what became of them.
func (c *Conversation) AbandonToolRequests(reason error) []ToolFailure {
	pending := c.PendingToolRequests()
	if len(pending) == 0 {
		return nil
	}

	failures := make([]ToolFailure, 0, len(pending))
	for _, r := range pending {
		failures = append(failures, c.AddToolFailure(r.ID, reason))
	}
	return failures
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
		case ToolAnswer:
			// Both kinds of answer count, so a turn that answers one request
			// does not strand the others made alongside it.
			id := msg.AnsweredRequest()
			if _, ok := pending[id]; !ok {
				return fmt.Errorf("message %d: tool result answers unknown request %q", i, id)
			}
			delete(pending, id)
		default:
			// A user turn or a harness instruction between a request and its
			// result would leave the request unanswered, so nothing stays
			// pending across one.
			pending = nil
		}
	}
	return nil
}
