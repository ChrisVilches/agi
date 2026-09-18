// Package model holds the core domain types agi operates on, independent of
// how they are transported to a provider or presented on the command line.
//
// A conversation is a sequence of messages, each kind with its own type rather
// than a single struct with optional fields: what a user may say, what the
// assistant may reply, what a tool reports back, and what agi itself instructs
// the model to do. The types make the impossible states unrepresentable — a
// user turn cannot request a tool, and a tool result always answers a specific
// request.
//
// The types also carry the rules about what may later be removed. A turn the
// user or the model produced is a permanent record and is never rewritten; a
// turn agi wrote to steer the run is scaffolding, and comes back out once it
// has done its job. That difference is the whole reason HarnessInstruction is
// a type of its own rather than a user turn with a prefix on it.
package model

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Role is the role a message travels under when it is sent to a provider. The
// four below are the whole set every mainstream provider agrees on.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one turn in a Conversation. The set of implementations is closed:
// UserMessage, AssistantMessage, ToolResultMessage, SubagentResults, and
// HarnessInstruction.
type Message interface {
	// Role is the role the turn travels under when it is sent to a provider.
	// It is not always who produced the turn: a harness instruction is agi's
	// own, but no provider has a role for that, so it goes as a user turn.
	Role() Role
	// Validate reports whether the message is well formed.
	Validate() error
	// Truncatable reports whether the turn is eligible to be dropped from
	// the conversation. It is permission, not a plan: nothing obliges a
	// caller to drop a truncatable turn, and some are kept for the whole
	// run.
	//
	// The guarantee worth relying on is the false case. What the user asked
	// and what the model answered are a record, and a record is only ever
	// appended to, so those turns are never dropped by anything. Only a
	// turn agi wrote itself can be, which is what confines the question to
	// agi's own scaffolding.
	Truncatable() bool

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

// Truncatable implements Message. What the user said stands for the whole run
// and is never dropped.
func (UserMessage) Truncatable() bool { return false }

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

// Truncatable implements Message. What the model answered stands for the whole
// run, kept exactly as it came back and never dropped.
func (AssistantMessage) Truncatable() bool { return false }

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

// AnsweredRequest implements ToolAnswer.
func (m ToolResultMessage) AnsweredRequest() string { return m.RequestID }

// Render implements ToolAnswer. A plain result is already the text the model
// reads, so there is nothing to assemble.
func (m ToolResultMessage) Render() string { return m.Content }

// Truncatable implements Message. A result belongs to the request it answers,
// so it lasts as long as the assistant turn that asked for it, which is the
// whole run.
func (ToolResultMessage) Truncatable() bool { return false }

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

// HarnessInstruction is a turn agi wrote itself, telling the model how to
// behave for the next stretch of the run — restating an answer as JSON, say.
// It differs from a user turn in two ways that matter enough to justify a type
// of its own.
//
// First, nobody said it. The user did not type it and the model did not
// produce it, so when it reaches the provider it is marked as coming from the
// program: Render wraps it in delimiters the model can recognize, and the turn
// travels under the user role because that is the only role a provider offers
// for it.
//
// Second, it is scaffolding rather than record, so it is the one turn that may
// be dropped again. That is eligibility and not a schedule: an instruction can
// just as well stand for the whole run, and it is up to whatever wrote it to
// decide whether it has outlived its use. What is fixed is the other side of
// the rule — a user or assistant turn is never dropped by anything — so being
// the only Truncatable type is what confines the question to agi's own words.
//
// Conversation.DropFrom is how a caller takes one back out, along with the
// turns it caused. The schema run does exactly that, so a run resumed from the
// conversation afterwards sees what the user asked and what the model
// answered, with no trace of how agi steered it in between.
type HarnessInstruction struct {
	// Content is the instruction as agi wrote it, without the delimiters that
	// Render adds. It is kept unwrapped so the instruction can be inspected
	// and compared without parsing its presentation back off.
	Content string
}

// instructionPrefix marks a harness instruction as the program speaking. The
// model would otherwise read it as the user interrupting with a new request.
const instructionPrefix = "[Instructions] Follow these instructions: "

// NewHarnessInstruction returns an instruction from agi to the model.
func NewHarnessInstruction(content string) HarnessInstruction {
	return HarnessInstruction{Content: content}
}

// Role implements Message. A provider has no role for the harness, so the
// instruction goes as a user turn and Render is what distinguishes it.
func (HarnessInstruction) Role() Role { return RoleUser }

func (HarnessInstruction) isMessage() {}

// Truncatable implements Message. An instruction may be removed once it has
// served its purpose — whether it is depends on what wrote it — which is what
// makes it an instruction rather than a record.
func (HarnessInstruction) Truncatable() bool { return true }

// Render returns the text to send to the provider: the instruction wrapped in
// delimiters that mark it as the program speaking rather than the user.
func (m HarnessInstruction) Render() string { return instructionPrefix + m.Content }

// Validate implements Message.
func (m HarnessInstruction) Validate() error {
	if m.Content == "" {
		return fmt.Errorf("model: harness instruction is empty")
	}
	return nil
}

// ToolAnswer is the subset of messages that answer a tool request. There are
// two, because some tools have more to say than a string: a plain
// ToolResultMessage, and SubagentResults, which keeps each subagent's answer
// apart until the moment it is sent.
//
// Everything that has to treat the two alike goes through this interface —
// checking that a result answers a request that was made, encoding a turn for
// a gateway — so a third kind of answer is added by implementing it rather
// than by finding every switch that would otherwise have to learn about it.
type ToolAnswer interface {
	Message
	// AnsweredRequest names the ToolRequest this turn answers.
	AnsweredRequest() string
	// Render returns the text the model reads as the tool's output. A message
	// that keeps its parts separate assembles them here, and nowhere earlier:
	// the structure is worth having right up to the point where something has
	// to be sent.
	Render() string
}

// SubagentResult is what one subagent came back with. A subagent that failed
// says so in its answer, in words meant for the model that spawned it.
type SubagentResult struct {
	// Title is the subagent's brief in a few words, as the model titled it.
	Title string
	// Answer is what it returned.
	Answer string
}

// SubagentResults answers one request to the subagents tool, holding each
// subagent's answer separately rather than as one block of text.
//
// The text the model reads is assembled by Render and is not stored, which is
// the point of the type. A run that spawned four subagents keeps four answers
// under four titles — in the conversation, and in a session snapshot — instead
// of a single string that would have to be parsed apart to be anything but
// displayed again.
type SubagentResults struct {
	// RequestID names the tool request this answers.
	RequestID string
	// Results are the answers, in the order the tasks were given.
	Results []SubagentResult
}

// NewSubagentResults returns the answer to a subagents request.
func NewSubagentResults(requestID string, results []SubagentResult) SubagentResults {
	return SubagentResults{RequestID: requestID, Results: results}
}

// Role implements Message. It answers a tool request, so it travels as one.
func (SubagentResults) Role() Role { return RoleTool }

func (SubagentResults) isMessage() {}

// Truncatable implements Message. Like any tool result it belongs to the
// request that asked for it, and lasts as long as that turn does.
func (SubagentResults) Truncatable() bool { return false }

// AnsweredRequest implements ToolAnswer.
func (m SubagentResults) AnsweredRequest() string { return m.RequestID }

// resultHeader marks where one subagent's answer begins.
//
// It is deliberately unlike anything a model writes. An answer is often full
// of Markdown of its own, so a heading would be one heading among many and the
// boundaries between subagents would dissolve into the text they separate. The
// rule marks it as the harness speaking, and the numbering says how many
// answers there are, so a reader can tell a missing one from a short one.
const resultHeader = "=== subagent result #%d of %d: %s ==="

// Render implements ToolAnswer: every answer under its own header, in the
// order the tasks were given. Which subagent finished first is a matter of
// scheduling, and the same run should not read differently because of it.
func (m SubagentResults) Render() string {
	sections := make([]string, 0, len(m.Results))
	for i, r := range m.Results {
		header := fmt.Sprintf(resultHeader, i+1, len(m.Results), r.Title)
		sections = append(sections, header+"\n"+r.Answer)
	}
	return strings.Join(sections, "\n\n")
}

// Validate implements Message.
func (m SubagentResults) Validate() error {
	if m.RequestID == "" {
		return fmt.Errorf("model: subagent results answer no request")
	}
	if len(m.Results) == 0 {
		return fmt.Errorf("model: subagent results for %q are empty", m.RequestID)
	}
	for i, r := range m.Results {
		if r.Title == "" {
			return fmt.Errorf("model: subagent result %d has no title", i+1)
		}
		if r.Answer == "" {
			return fmt.Errorf("model: subagent %q returned nothing", r.Title)
		}
	}
	return nil
}
