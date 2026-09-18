package litellm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/model"
)

func TestEncodeRequestPutsSystemFirst(t *testing.T) {
	conv := model.Conversation{Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}}}
	got := encodeRequest("deepseek-chat", "be helpful", conv, nil)

	if len(got.Messages) != 2 {
		t.Fatalf("messages = %d, want the system prompt plus the turn", len(got.Messages))
	}
	if got.Messages[0].Role != "system" || got.Messages[0].Content != "be helpful" {
		t.Errorf("first message = %+v, want the system prompt", got.Messages[0])
	}
	if got.Model != "deepseek-chat" {
		t.Errorf("model = %q, want %q", got.Model, "deepseek-chat")
	}
}

func TestEncodeRequestOmitsEmptySystem(t *testing.T) {
	conv := model.Conversation{Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}}}
	got := encodeRequest("m", "", conv, nil)
	if len(got.Messages) != 1 || got.Messages[0].Role != "user" {
		t.Errorf("messages = %+v, want only the user turn", got.Messages)
	}
}

func TestEncodeToolDefinitions(t *testing.T) {
	defs := []model.ToolDefinition{{
		Name:        "bash",
		Description: "Run a command",
		Parameters:  map[string]any{"type": "object"},
	}}
	got := encodeRequest("m", "", model.Conversation{}, defs)

	if len(got.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(got.Tools))
	}
	if got.Tools[0].Type != "function" {
		t.Errorf("tool type = %q, want %q", got.Tools[0].Type, "function")
	}
	if got.Tools[0].Function.Name != "bash" {
		t.Errorf("tool name = %q, want %q", got.Tools[0].Function.Name, "bash")
	}
}

// Arguments travel as a JSON-encoded string, not as an object.
func TestEncodeToolCallArgumentsAreAString(t *testing.T) {
	m := model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{
		{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)},
	}}
	encoded, err := json.Marshal(encodeMessage(m))
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if !strings.Contains(string(encoded), `"arguments":"{\"command\":\"ls\"}"`) {
		t.Errorf("encoded = %s, want arguments as an escaped string", encoded)
	}
}

func TestEncodeToolCallWithoutArgumentsSendsEmptyObject(t *testing.T) {
	m := model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "c1", Name: "x"}}}
	if got := encodeMessage(m).ToolCalls[0].Function.Arguments; got != "{}" {
		t.Errorf("arguments = %q, want %q", got, "{}")
	}
}

func TestDecodeMessageRoundTripsToolCalls(t *testing.T) {
	w := wireMessage{
		Role: "assistant",
		ToolCalls: []wireToolCall{{
			ID:       "call-7",
			Type:     "function",
			Function: wireToolCallFunc{Name: "bash", Arguments: `{"command":"df -h"}`},
		}},
	}
	got := decodeMessage(w)

	if got.Role != model.RoleAssistant {
		t.Errorf("role = %q, want assistant", got.Role)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(got.ToolCalls))
	}
	if got.ToolCalls[0].ID != "call-7" || got.ToolCalls[0].Name != "bash" {
		t.Errorf("tool call = %+v, want id call-7 and name bash", got.ToolCalls[0])
	}
	if string(got.ToolCalls[0].Arguments) != `{"command":"df -h"}` {
		t.Errorf("arguments = %s, want the raw JSON object", got.ToolCalls[0].Arguments)
	}
}

// Some gateways omit the role on a response message.
func TestDecodeMessageDefaultsToAssistant(t *testing.T) {
	if got := decodeMessage(wireMessage{Content: "hello"}); got.Role != model.RoleAssistant {
		t.Errorf("role = %q, want assistant", got.Role)
	}
}
