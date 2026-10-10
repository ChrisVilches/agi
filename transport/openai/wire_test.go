package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/model"
)

func TestEncodeRequestPutsSystemFirst(t *testing.T) {
	got := encodeRequest("deepseek-chat", model.NewConversation("be helpful", "hi"), nil, carriesReasoning)

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
	got := encodeRequest("m", model.NewConversation("", "hi"), nil, carriesReasoning)
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
	got := encodeRequest("m", &model.Conversation{}, defs, carriesReasoning)

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
	encoded, err := json.Marshal(encodeMessage(m, carriesReasoning))
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if !strings.Contains(string(encoded), `"arguments":"{\"command\":\"ls\"}"`) {
		t.Errorf("encoded = %s, want arguments as an escaped string", encoded)
	}
}

func TestEncodeToolCallWithoutArgumentsSendsEmptyObject(t *testing.T) {
	m := model.AssistantMessage{ToolRequests: []model.ToolRequest{{ID: "c1", Name: "x"}}}
	if got := encodeMessage(m, carriesReasoning).ToolCalls[0].Function.Arguments; got != "{}" {
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
	got := decodeMessage(w, carriesReasoning)

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
	if got := decodeMessage(wireMessage{Content: "hello"}, carriesReasoning); got.Role() != model.RoleAssistant {
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

	got := encodeRequest("m", conv, nil, carriesReasoning)
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
	w := encodeMessage(model.NewHarnessInstruction("answer in JSON"), carriesReasoning)

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
	}), carriesReasoning)

	if w.Role != string(model.RoleTool) {
		t.Errorf("role = %q, want %q", w.Role, model.RoleTool)
	}
	if w.ToolCallID != "call-1" {
		t.Errorf("tool_call_id = %q, want it to answer call-1", w.ToolCallID)
	}
	const want = "=== subagent result #1 of 2: First ===\none\n\n" +
		"=== subagent result #2 of 2: Second ===\ntwo"
	if w.Content != want {
		t.Errorf("content = %q, want %q", w.Content, want)
	}
}

// A reasoning model returns its deliberation beside the content, and it goes
// back on the turn it belongs to.
func TestDecodeMessageReadsReasoning(t *testing.T) {
	got := decodeMessage(wireMessage{
		Role:             "assistant",
		Content:          "9.11 is greater",
		ReasoningContent: "comparing the decimal parts",
	}, carriesReasoning)

	if got.ReasoningContent != "comparing the decimal parts" {
		t.Errorf("reasoning = %q, want the deliberation the endpoint returned", got.ReasoningContent)
	}
	if got.Content != "9.11 is greater" {
		t.Errorf("content = %q, want the answer alongside it", got.Content)
	}
}

// An endpoint agi does not hand reasoning back to is one agi does not read it
// from: the two directions are one decision, so a gateway that returns the
// field is answered as though it had not.
func TestDecodeMessageIgnoresReasoningWhenItIsNotCarried(t *testing.T) {
	got := decodeMessage(wireMessage{
		Content:          "done",
		ReasoningContent: "a long think",
	}, noReasoning)

	if got.ReasoningContent != "" {
		t.Errorf("reasoning = %q, want none kept for an endpoint it cannot be sent back to", got.ReasoningContent)
	}
	if got.Content != "done" {
		t.Errorf("content = %q, want the reply itself untouched", got.Content)
	}
}

func TestEncodeAssistantSendsReasoningBack(t *testing.T) {
	w := encodeMessage(model.AssistantMessage{
		Content:          "checking",
		ReasoningContent: "the user wants the weather, so I need the date first",
		ToolRequests:     []model.ToolRequest{{ID: "c1", Name: "get_date"}},
	}, carriesReasoning)

	if w.ReasoningContent != "the user wants the weather, so I need the date first" {
		t.Errorf("reasoning = %q, want the turn's own deliberation", w.ReasoningContent)
	}
}

// DeepSeek wants the reasoning of every earlier assistant turn when a request
// carries tools, the turns that asked for no tool included, and refuses the
// request when one is missing. So no turn is exempt.
func TestEncodeSendsReasoningOnATurnThatAskedForNoTool(t *testing.T) {
	conv := model.NewConversation("sys", "how's the weather")
	conv.AddAssistant(model.AssistantMessage{
		Content:          "it is cloudy",
		ReasoningContent: "the tool answered, so I can tell them",
	})

	got := encodeRequest("deepseek-chat", conv, nil, carriesReasoning)
	if got.Messages[2].ReasoningContent != "the tool answered, so I can tell them" {
		t.Errorf("reasoning sent = %q, want it on a turn with no tool calls too",
			got.Messages[2].ReasoningContent)
	}
}

func TestEncodeDropsReasoningForAnEndpointThatDoesNotCarryIt(t *testing.T) {
	w := encodeMessage(model.AssistantMessage{
		Content:          "done",
		ReasoningContent: "a long think",
	}, noReasoning)

	if w.ReasoningContent != "" {
		t.Errorf("reasoning = %q, want none sent to an endpoint that does not carry it", w.ReasoningContent)
	}
}

// The key is left out rather than sent empty, which is what lets one struct
// serve every endpoint: one that has never heard of the field never sees it.
func TestEncodeOmitsTheReasoningKeyWhenThereIsNone(t *testing.T) {
	for _, tt := range []struct {
		name  string
		msg   model.AssistantMessage
		carry reasoning
	}{
		{"model reasoned but the endpoint does not carry it",
			model.AssistantMessage{Content: "done", ReasoningContent: "a think"}, noReasoning},
		{"endpoint carries it but the model returned none",
			model.AssistantMessage{Content: "done"}, carriesReasoning},
	} {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := json.Marshal(encodeMessage(tt.msg, tt.carry))
			if err != nil {
				t.Fatalf("marshalling: %v", err)
			}
			if strings.Contains(string(encoded), "reasoning_content") {
				t.Errorf("encoded = %s, want no reasoning key at all", encoded)
			}
		})
	}
}
