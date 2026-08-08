package litellm

import (
	"encoding/json"
	"fmt"

	"github.com/ChrisVilches/agi/model"
)

// The types below mirror the OpenAI-compatible chat completions schema that
// LiteLLM exposes. They are deliberately kept separate from the domain types
// in package model: the wire format is the gateway's concern, and it should be
// possible to change one without disturbing the other.

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
func encodeRequest(modelName, system string, conv model.Conversation, defs []model.ToolDefinition) wireRequest {
	msgs := make([]wireMessage, 0, conv.Len()+1)
	if system != "" {
		msgs = append(msgs, wireMessage{Role: string(model.RoleSystem), Content: system})
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

func encodeMessage(m model.Message) wireMessage {
	out := wireMessage{
		Role:       string(m.Role),
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
	}
	for _, c := range m.ToolCalls {
		args := string(c.Arguments)
		if args == "" {
			args = "{}"
		}
		out.ToolCalls = append(out.ToolCalls, wireToolCall{
			ID:       c.ID,
			Type:     "function",
			Function: wireToolCallFunc{Name: c.Name, Arguments: args},
		})
	}
	return out
}

// decodeMessage maps a wire message back onto the domain type.
func decodeMessage(w wireMessage) model.Message {
	out := model.Message{
		Role:       model.Role(w.Role),
		Content:    w.Content,
		ToolCallID: w.ToolCallID,
	}
	if out.Role == "" {
		out.Role = model.RoleAssistant
	}
	for _, c := range w.ToolCalls {
		args := json.RawMessage(c.Function.Arguments)
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		out.ToolCalls = append(out.ToolCalls, model.ToolCall{
			ID:        c.ID,
			Name:      c.Function.Name,
			Arguments: args,
		})
	}
	return out
}
