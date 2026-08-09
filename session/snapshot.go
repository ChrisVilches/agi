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

// The kind of each stored turn. These strings are the format, so they may not
// be renamed without a new snapshotVersion.
const (
	kindUser               = "user"
	kindAssistant          = "assistant"
	kindToolResult         = "tool_result"
	kindHarnessInstruction = "harness_instruction"
)

// snapshotVersion is written into every file. A build that meets a version it
// does not know refuses to guess at the contents.
const snapshotVersion = 1

type snapshot struct {
	Version  int             `json:"version"`
	System   string          `json:"system,omitempty"`
	Messages []storedMessage `json:"messages"`
}

// storedMessage is every kind of turn in one struct, since a file has no
// closed set of types to switch on. Kind says which fields mean anything.
type storedMessage struct {
	Kind         string              `json:"kind"`
	Content      string              `json:"content,omitempty"`
	ToolRequests []storedToolRequest `json:"tool_requests,omitempty"`
	RequestID    string              `json:"request_id,omitempty"`
	Failed       bool                `json:"failed,omitempty"`
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
	s := snapshot{
		Version:  snapshotVersion,
		System:   conv.System,
		Messages: make([]storedMessage, 0, conv.Len()),
	}

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
				Failed:    msg.Failed,
			})
		case model.AssistantMessage:
			stored := storedMessage{Kind: kindAssistant, Content: msg.Content}
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
	if s.Version != snapshotVersion {
		return nil, fmt.Errorf("session: snapshot version %d, want %d", s.Version, snapshotVersion)
	}

	conv := &model.Conversation{System: s.System}
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
				Failed:    m.Failed,
			})
		case kindAssistant:
			msg := model.AssistantMessage{Content: m.Content}
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
