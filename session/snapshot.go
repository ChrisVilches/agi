package session

import (
	"encoding/json"
	"fmt"

	"github.com/ChrisVilches/agi/model"
)

// The types below are the on-disk form of a conversation. They are kept apart
// from the domain types in package model for the same reason the wire types in
// transport/litellm are: the file format is this package's concern, and it
// should be possible to change one without disturbing the other.
//
// What is stored is agi's own account of the run, not a provider's. A harness
// instruction is stored as a harness instruction, because that is what it is;
// that it reaches a gateway disguised as a user turn is the gateway's business
// and no part of the record.

// The kind of each stored turn. These strings are the format: renaming one
// makes every snapshot that used it unreadable.
const (
	kindUser               = "user"
	kindAssistant          = "assistant"
	kindToolResult         = "tool_result"
	kindToolFailure        = "tool_failure"
	kindSubagentResults    = "subagent_results"
	kindHarnessInstruction = "harness_instruction"
)

// A snapshot carries no version. agi is run from a fresh install against
// sessions that install made, so there is no older format to recognize, and a
// number in every file would be noise standing in for a problem nobody has. A
// file this build cannot read still fails loudly: an unknown kind is refused
// rather than guessed at.
//
// It carries no system prompt either, and that is a decision rather than an
// omission. A system prompt is not something anyone said; it is assembled from
// how the run was invoked — the base prompt, the tools registered, and in time
// whatever else the arguments bring in. A run resumed with different arguments
// would be given a different prompt whatever the file said, so storing one
// would mean keeping a copy that is right only until the next invocation
// differs, and that nothing reads even then. The invocation is the source of
// truth, and the snapshot holds only what was said.
type snapshot struct {
	Messages []storedMessage `json:"messages"`
}

// storedMessage is every kind of turn in one struct, since a file has no
// closed set of types to switch on. Kind says which fields mean anything.
type storedMessage struct {
	Kind         string              `json:"kind"`
	Content      string              `json:"content,omitempty"`
	ToolRequests []storedToolRequest `json:"tool_requests,omitempty"`
	RequestID    string              `json:"request_id,omitempty"`
	// Reason is why a call could not be made, stored as the reason rather
	// than as the text it renders to.
	Reason string `json:"reason,omitempty"`
	// Results are the subagent answers, kept apart as the message keeps them
	// rather than as the text they render to.
	Results []storedSubagentResult `json:"results,omitempty"`
	// ReasoningContent is the deliberation behind an assistant turn, stored
	// because a resumed run has to send it back: an endpoint that wants
	// reasoning on every historical assistant turn wants it on the ones that
	// happened in an earlier run too, and a file that dropped it would be a
	// file no run could be resumed from. It is spelled as the domain spells
	// it rather than as any endpoint does; that the two agree here is a
	// coincidence of DeepSeek having chosen a sensible name.
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// OutputItems is the whole of an assistant turn in the shape the endpoint
	// reads it back in: every item the reply carried, in the order it carried
	// them, stored for the same reason as the words above and kept as the raw
	// JSON it arrived as. ThinkingBlocks and ReasoningItems are two earlier
	// answers to the same question, written by versions of agi that kept the
	// deliberation alone; nothing writes either any more and both are read
	// back so that the sessions holding them still resume.
	//
	// These three are spelled as the endpoint spells them, which is the
	// opposite of the rule above and deliberate. The field above holds words,
	// and the words are agi's to name; these hold an endpoint's own bytes,
	// which agi stores and never looks inside, so naming them anything else
	// would invite a reader to think the shape was agi's to change.
	//
	// Backend and Model are who produced them, and they are the reason this
	// is no longer worth a warning. What is stored suits the endpoint that
	// produced it and nothing else: deliberation does not cross model
	// families, and a session resumed against another route used to hand that
	// route another's reasoning and die of it — Anthropic answering
	// "messages.N.content.M: Invalid `signature` in `thinking` block", OpenAI
	// refusing an item encrypted to a model it is not. Storing the origin is
	// what lets a resumed run tell: model.TurnReplay.OutputFor hands the items
	// back only to the endpoint named here, and a run that resumed somewhere
	// else sends none and loses nothing but the thinking. A turn written
	// before agi recorded an origin has none, matches nothing, and is read
	// back for the same reason — so that resuming it still works, rather than
	// so that it can be sent.
	OutputItems    json.RawMessage `json:"output_items,omitempty"`
	Backend        string          `json:"backend,omitempty"`
	Model          string          `json:"model,omitempty"`
	ThinkingBlocks json.RawMessage `json:"thinking_blocks,omitempty"`
	ReasoningItems json.RawMessage `json:"reasoning_items,omitempty"`
}

// storedSubagentResult is one subagent's answer, kept under its own title.
//
// It is the one thing stored apart rather than as the text it renders to, and
// the reason is that the structure belongs to the turn: a subagents call
// answers with it through tools.Resulter, the conversation holds it, and
// Render assembles the sections at the moment of sending. Text a model reads
// is never stored assembled, so what is kept apart here is kept apart there.
//
// What a program wrote is the instructive contrast and is deliberately not
// stored this way. Its two streams live in a tools.Execution that belongs to
// the call rather than to the turn, and that is gone once the hooks around the
// call have been shown it; a tool result reaches here as the one string the
// model read — woven, cut to length, under agi's own sentence about how the
// program ended, which is in neither stream. There is no pair of streams that
// renders back to that string, so storing one would mean storing the whole
// execution, and a resumed run would go on sending the model the string
// anyway: structure kept and never read. A snapshot exists to restore a
// conversation rather than to explain one, and which stream carried which
// bytes is explanation. The log is where a question of that kind is asked, and
// section 14.11 of docs/hooks-specification.md says how far it can be answered
// there.
type storedSubagentResult struct {
	Title  string `json:"title"`
	Answer string `json:"answer"`
}

type storedToolRequest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Arguments is stored as the JSON object it is rather than as a string
	// holding one, so the file stays readable. What comes back is therefore
	// the same document but not necessarily the same bytes — it is re-rendered
	// with the rest of the file — which is all a tool needs, since it
	// unmarshals the arguments anyway.
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// encode renders a conversation as the bytes to store. The switch is
// exhaustive over the closed set of message types, and a type it does not know
// is an error rather than a silently dropped turn.
func encode(conv *model.Conversation) ([]byte, error) {
	s := snapshot{Messages: make([]storedMessage, 0, conv.Len())}

	for i, m := range conv.Messages {
		switch msg := m.(type) {
		case model.UserMessage:
			s.Messages = append(s.Messages, storedMessage{Kind: kindUser, Content: msg.Content})
		case model.HarnessInstruction:
			s.Messages = append(s.Messages, storedMessage{Kind: kindHarnessInstruction, Content: msg.Content})
		case model.ToolResultMessage:
			s.Messages = append(s.Messages, storedMessage{
				Kind:      kindToolResult,
				Content:   msg.Content,
				RequestID: msg.RequestID,
			})
		case model.ToolFailure:
			s.Messages = append(s.Messages, storedMessage{
				Kind:      kindToolFailure,
				RequestID: msg.RequestID,
				Reason:    msg.Reason,
			})
		case model.SubagentResults:
			stored := storedMessage{Kind: kindSubagentResults, RequestID: msg.RequestID}
			for _, r := range msg.Results {
				stored.Results = append(stored.Results, storedSubagentResult{
					Title:  r.Title,
					Answer: r.Answer,
				})
			}
			s.Messages = append(s.Messages, stored)
		case model.AssistantMessage:
			stored := storedMessage{
				Kind:             kindAssistant,
				Content:          msg.Content,
				ReasoningContent: msg.ReasoningContent,
				ThinkingBlocks:   msg.Replay.ThinkingBlocks,
				ReasoningItems:   msg.Replay.ReasoningItems,
				OutputItems:      msg.Replay.OutputItems,
				Backend:          msg.Replay.Origin.Backend,
				Model:            msg.Replay.Origin.Model,
			}
			for _, r := range msg.ToolRequests {
				stored.ToolRequests = append(stored.ToolRequests, storedToolRequest{
					ID:        r.ID,
					Name:      r.Name,
					Arguments: r.Arguments,
				})
			}
			s.Messages = append(s.Messages, stored)
		default:
			return nil, fmt.Errorf("session: turn %d is a %T, which cannot be stored", i, m)
		}
	}

	// Indented, because a session file is something a person may open.
	return json.MarshalIndent(s, "", "  ")
}

// decode reads a stored conversation back. Everything it returns has been
// checked: a file that has been edited into nonsense fails here rather than
// confusing the model halfway through the next run.
func decode(data []byte) (*model.Conversation, error) {
	var s snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	// No system prompt: the run that resumes this assembles its own.
	conv := &model.Conversation{}
	for i, m := range s.Messages {
		switch m.Kind {
		case kindUser:
			conv.Append(model.UserMessage{Content: m.Content})
		case kindHarnessInstruction:
			conv.Append(model.HarnessInstruction{Content: m.Content})
		case kindToolResult:
			conv.Append(model.ToolResultMessage{
				RequestID: m.RequestID,
				Content:   m.Content,
			})
		case kindToolFailure:
			conv.Append(model.ToolFailure{
				RequestID: m.RequestID,
				Reason:    m.Reason,
			})
		case kindSubagentResults:
			msg := model.SubagentResults{RequestID: m.RequestID}
			for _, r := range m.Results {
				msg.Results = append(msg.Results, model.SubagentResult{
					Title:  r.Title,
					Answer: r.Answer,
				})
			}
			conv.Append(msg)
		case kindAssistant:
			msg := model.AssistantMessage{
				Content:          m.Content,
				ReasoningContent: m.ReasoningContent,
				// Through the constructor, so that a file holding an explicit
				// null — which a hand-edited one may — reads back as the
				// nothing it means rather than as four bytes to send on.
				Replay: model.NewStoredReplay(
					model.Origin{Backend: m.Backend, Model: m.Model},
					m.OutputItems, m.ReasoningItems, m.ThinkingBlocks,
				),
			}
			for _, r := range m.ToolRequests {
				msg.ToolRequests = append(msg.ToolRequests, model.ToolRequest{
					ID:        r.ID,
					Name:      r.Name,
					Arguments: r.Arguments,
				})
			}
			conv.Append(msg)
		default:
			return nil, fmt.Errorf("session: turn %d has unknown kind %q", i, m.Kind)
		}
	}

	if err := conv.Validate(); err != nil {
		return nil, fmt.Errorf("session: stored conversation is not usable: %w", err)
	}
	return conv, nil
}
