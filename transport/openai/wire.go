package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/transport"
)

// The types below mirror the OpenAI chat completions schema, which is what
// every endpoint agi talks to accepts — OpenAI itself, DeepSeek, and a LiteLLM
// gateway fronting either. They are deliberately kept separate from the domain
// types in package model: the wire format is the endpoint's concern, and it
// should be possible to change one without disturbing the other.
//
// They also mark the ceiling AGENTS.md sets. Nothing here asks for structured
// output or prefills an assistant turn, and nothing should be added that does:
// the schema is the intersection of what providers agree on, written out.

type wireRequest struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
	Tools    []wireTool    `json:"tools,omitempty"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireToolCallFunc `json:"function"`
}

type wireToolCallFunc struct {
	Name string `json:"name"`
	// Arguments is a JSON object encoded as a string, per the protocol.
	Arguments string `json:"arguments"`
}

type wireTool struct {
	Type     string       `json:"type"`
	Function wireToolFunc `json:"function"`
}

type wireToolFunc struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type wireResponse struct {
	Choices []struct {
		Message      wireMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Error *wireError `json:"error"`
}

// wireModels is the catalogue an endpoint returns, which is a list under an
// envelope both a gateway and a provider spell the same way.
type wireModels struct {
	Data  []wireModel `json:"data"`
	Error *wireError  `json:"error"`
}

// wireModel is one entry of that catalogue.
//
// It reads two names for one number, and that is the whole of where the
// endpoints disagree: a LiteLLM gateway reports a model's context as
// `max_input_tokens`, and DeepSeek reports the same thing as `context_window`.
// Reading both here is what lets everything above this file see one shape —
// the alternative is a listing whose columns depend on which backend was
// configured, which is the thing transport.Model exists to prevent. A field
// the endpoint did not send decodes to zero, which is what that type reads as
// "it did not say".
type wireModel struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by"`
	// MaxInputTokens is a gateway's name for the context window.
	MaxInputTokens int `json:"max_input_tokens"`
	// ContextWindow is the provider's name for the same number.
	ContextWindow   int `json:"context_window"`
	MaxOutputTokens int `json:"max_output_tokens"`
}

type wireError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

func (e *wireError) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("%s: %s", e.Type, e.Message)
	}
	return e.Message
}

// encodeRequest maps a transport request onto the wire schema.
func encodeRequest(modelName string, conv *model.Conversation, defs []model.ToolDefinition) wireRequest {
	msgs := make([]wireMessage, 0, conv.Len()+1)
	if conv.System != "" {
		msgs = append(msgs, wireMessage{Role: string(model.RoleSystem), Content: conv.System})
	}
	for _, m := range conv.Messages {
		msgs = append(msgs, encodeMessage(m))
	}

	tools := make([]wireTool, 0, len(defs))
	for _, d := range defs {
		tools = append(tools, wireTool{
			Type: "function",
			Function: wireToolFunc{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  d.Parameters,
			},
		})
	}

	return wireRequest{Model: modelName, Messages: msgs, Tools: tools}
}

// encodeMessage maps one domain message onto the wire schema. The switch is
// exhaustive over the closed set of message types.
func encodeMessage(m model.Message) wireMessage {
	out := wireMessage{Role: string(m.Role())}

	switch msg := m.(type) {
	case model.UserMessage:
		out.Content = msg.Content
	case model.HarnessInstruction:
		// The harness has no role of its own on the wire, so the instruction
		// travels as a user turn with delimiters marking where it came from.
		out.Content = msg.Render()
	case model.AssistantMessage:
		out.Content = msg.Content
		for _, r := range msg.ToolRequests {
			args := string(r.Arguments)
			if args == "" {
				args = "{}"
			}
			out.ToolCalls = append(out.ToolCalls, wireToolCall{
				ID:       r.ID,
				Type:     "function",
				Function: wireToolCallFunc{Name: r.Name, Arguments: args},
			})
		}
	case model.ToolAnswer:
		// Both kinds of answer render to the text the model reads, and a
		// message that kept its parts separate assembles them here, at the
		// last moment before they are sent.
		out.Content = msg.Render()
		out.ToolCallID = msg.AnsweredRequest()
	}
	return out
}

// decodeModel maps one listed model onto the domain type, settling the two
// names for the context window in favor of whichever the endpoint sent. An
// endpoint sends one or the other and never both, so there is no case here of
// two numbers disagreeing: the first one found is the answer.
func decodeModel(w wireModel) transport.Model {
	window := w.ContextWindow
	if window == 0 {
		window = w.MaxInputTokens
	}
	return transport.Model{
		ID:              strings.TrimSpace(w.ID),
		OwnedBy:         strings.TrimSpace(w.OwnedBy),
		ContextWindow:   window,
		MaxOutputTokens: w.MaxOutputTokens,
	}
}

// decodeMessage maps a reply back onto the domain type. A reply is always an
// assistant turn, whatever role the gateway labels it with.
func decodeMessage(w wireMessage) model.AssistantMessage {
	out := model.AssistantMessage{Content: w.Content}
	for _, c := range w.ToolCalls {
		args := json.RawMessage(c.Function.Arguments)
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		out.ToolRequests = append(out.ToolRequests, model.ToolRequest{
			ID:        c.ID,
			Name:      c.Function.Name,
			Arguments: args,
		})
	}
	return out
}
