package litellm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/model"
)

func TestEncodeRequestPutsSystemFirst(t *testing.T) {
	got := encodeRequest("deepseek-chat", model.NewConversation("be helpful", "hi"), nil)

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
	got := encodeRequest("m", model.NewConversation("", "hi"), nil)
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
	got := encodeRequest("m", &model.Conversation{}, defs)

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
	m := model.AssistantMessage{ToolRequests: []model.ToolRequest{
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
	m := model.AssistantMessage{ToolRequests: []model.ToolRequest{{ID: "c1", Name: "x"}}}
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

	if got.Role() != model.RoleAssistant {
		t.Errorf("role = %q, want assistant", got.Role())
	}
	if len(got.ToolRequests) != 1 {
		t.Fatalf("tool requests = %d, want 1", len(got.ToolRequests))
	}
	if got.ToolRequests[0].ID != "call-7" || got.ToolRequests[0].Name != "bash" {
		t.Errorf("tool request = %+v, want id call-7 and name bash", got.ToolRequests[0])
	}
	if string(got.ToolRequests[0].Arguments) != `{"command":"df -h"}` {
		t.Errorf("arguments = %s, want the raw JSON object", got.ToolRequests[0].Arguments)
	}
}

// A reply is an assistant turn whatever the gateway labels it.
func TestDecodeMessageIsAlwaysAssistant(t *testing.T) {
	if got := decodeMessage(wireMessage{Content: "hello"}); got.Role() != model.RoleAssistant {
		t.Errorf("role = %q, want assistant", got.Role())
	}
}

// Every message type must survive the trip to the wire schema.
func TestEncodeMessageCoversEveryType(t *testing.T) {
	conv := model.NewConversation("sys", "do it")
	conv.AddAssistant(model.AssistantMessage{
		Content:      "checking",
		ToolRequests: []model.ToolRequest{{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{}`)}},
	})
	conv.AddToolResult("c1", "output")

	got := encodeRequest("m", conv, nil)
	if len(got.Messages) != 4 {
		t.Fatalf("messages = %d, want system, user, assistant and tool", len(got.Messages))
	}
	roles := []string{got.Messages[0].Role, got.Messages[1].Role, got.Messages[2].Role, got.Messages[3].Role}
	want := []string{"system", "user", "assistant", "tool"}
	for i := range want {
		if roles[i] != want[i] {
			t.Errorf("message %d role = %q, want %q", i, roles[i], want[i])
		}
	}
	if got.Messages[3].ToolCallID != "c1" {
		t.Errorf("tool result id = %q, want c1", got.Messages[3].ToolCallID)
	}
	if len(got.Messages[2].ToolCalls) != 1 {
		t.Errorf("assistant tool calls = %d, want 1", len(got.Messages[2].ToolCalls))
	}
}

// A harness instruction has no role of its own on the wire: it travels as a
// user turn, wrapped so the model can see it came from the program.
func TestEncodeHarnessInstruction(t *testing.T) {
	w := encodeMessage(model.NewHarnessInstruction("answer in JSON"))

	if w.Role != string(model.RoleUser) {
		t.Errorf("role = %q, want %q", w.Role, model.RoleUser)
	}
	const want = "[Instructions] Follow these instructions: answer in JSON"
	if w.Content != want {
		t.Errorf("content = %q, want %q", w.Content, want)
	}
	if len(w.ToolCalls) != 0 || w.ToolCallID != "" {
		t.Errorf("message = %+v, want no tool fields", w)
	}
}

// Subagent results reach the gateway as one tool message: the parts are
// assembled at the last moment, not stored assembled.
func TestEncodeSubagentResults(t *testing.T) {
	w := encodeMessage(model.NewSubagentResults("call-1", []model.SubagentResult{
		{Title: "First", Answer: "one"},
		{Title: "Second", Answer: "two"},
	}))

	if w.Role != string(model.RoleTool) {
		t.Errorf("role = %q, want %q", w.Role, model.RoleTool)
	}
	if w.ToolCallID != "call-1" {
		t.Errorf("tool_call_id = %q, want it to answer call-1", w.ToolCallID)
	}
	const want = "## First\none\n\n## Second\ntwo"
	if w.Content != want {
		t.Errorf("content = %q, want %q", w.Content, want)
	}
}
