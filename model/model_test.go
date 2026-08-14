package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// The closed set of message types is the point of the package.
func TestMessageTypesAndRoles(t *testing.T) {
	tests := []struct {
		msg  Message
		want Role
	}{
		{NewUserMessage("hi"), RoleUser},
		{NewAssistantMessage("hello"), RoleAssistant},
		{NewToolResult("call-1", "output"), RoleTool},
	}
	for _, tt := range tests {
		if got := tt.msg.Role(); got != tt.want {
			t.Errorf("%T.Role() = %q, want %q", tt.msg, got, tt.want)
		}
	}
}

func TestUserMessageValidate(t *testing.T) {
	if err := NewUserMessage("hi").Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
	if err := (UserMessage{}).Validate(); err == nil {
		t.Error("Validate() of an empty user message = nil, want an error")
	}
}

// An assistant turn may carry prose, tool requests, or both — but not neither.
func TestAssistantMessageValidate(t *testing.T) {
	tests := []struct {
		name    string
		msg     AssistantMessage
		wantErr string
	}{
		{"prose only", NewAssistantMessage("hello"), ""},
		{
			"requests only",
			AssistantMessage{ToolRequests: []ToolRequest{{ID: "c1", Name: "bash"}}},
			"",
		},
		{
			"both",
			AssistantMessage{Content: "checking", ToolRequests: []ToolRequest{{ID: "c1", Name: "bash"}}},
			"",
		},
		{"neither", AssistantMessage{}, "is empty"},
		{
			"request without an id",
			AssistantMessage{ToolRequests: []ToolRequest{{Name: "bash"}}},
			"has no id",
		},
		{
			"request without a name",
			AssistantMessage{ToolRequests: []ToolRequest{{ID: "c1"}}},
			"names no tool",
		},
		{
			"duplicate request ids",
			AssistantMessage{ToolRequests: []ToolRequest{
				{ID: "c1", Name: "bash"},
				{ID: "c1", Name: "bash"},
			}},
			"duplicate tool request id",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.msg.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("Validate() = nil, want an error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("Validate() = %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestWantsTools(t *testing.T) {
	if NewAssistantMessage("hi").WantsTools() {
		t.Error("WantsTools() = true for a prose-only reply, want false")
	}
	m := AssistantMessage{ToolRequests: []ToolRequest{{ID: "c1", Name: "bash"}}}
	if !m.WantsTools() {
		t.Error("WantsTools() = false with a request pending, want true")
	}
}

// Empty tool output must not produce an empty message.
func TestNewToolResultReplacesEmptyOutput(t *testing.T) {
	got := NewToolResult("c1", "")
	if got.Content != "(no output)" {
		t.Errorf("Content = %q, want a placeholder", got.Content)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestNewToolFailureIsMarked(t *testing.T) {
	got := NewToolFailure("c1", errNotFound{})
	if !got.Failed {
		t.Error("Failed = false, want true")
	}
	if !strings.Contains(got.Content, "not found") {
		t.Errorf("Content = %q, want the error described for the model", got.Content)
	}
}

type errNotFound struct{}

func (errNotFound) Error() string { return "tool not found" }

func TestToolResultValidate(t *testing.T) {
	if err := (ToolResultMessage{Content: "x"}).Validate(); err == nil {
		t.Error("Validate() without a request id = nil, want an error")
	}
	if err := (ToolResultMessage{RequestID: "c1"}).Validate(); err == nil {
		t.Error("Validate() without content = nil, want an error")
	}
}

func TestToolRequestKeepsRawArguments(t *testing.T) {
	raw := json.RawMessage(`{"command":"ls"}`)
	r := ToolRequest{ID: "c1", Name: "bash", Arguments: raw}
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if string(r.Arguments) != `{"command":"ls"}` {
		t.Errorf("Arguments = %s, want the raw JSON preserved", r.Arguments)
	}
}

// A harness instruction is agi speaking, not the user, and it has to be
// recognizable as such once it reaches the model.
func TestHarnessInstruction(t *testing.T) {
	m := NewHarnessInstruction("answer in JSON")

	if got := m.Role(); got != RoleUser {
		t.Errorf("Role() = %q, want %q: a provider has no role for the harness", got, RoleUser)
	}
	if got := m.Content; got != "answer in JSON" {
		t.Errorf("Content = %q, want the instruction stored unwrapped", got)
	}

	const want = "[Instructions] Follow these instructions: answer in JSON"
	if got := m.Render(); got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}

	if err := m.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
	if err := (HarnessInstruction{}).Validate(); err == nil {
		t.Error("Validate() of an empty instruction = nil, want an error")
	}
}

// Only what agi wrote itself may be taken back out of the conversation.
func TestTruncatable(t *testing.T) {
	tests := []struct {
		msg  Message
		want bool
	}{
		{NewUserMessage("hi"), false},
		{NewAssistantMessage("hello"), false},
		{NewToolResult("call-1", "output"), false},
		{NewHarnessInstruction("answer in JSON"), true},
	}
	for _, tt := range tests {
		if got := tt.msg.Truncatable(); got != tt.want {
			t.Errorf("%T.Truncatable() = %v, want %v", tt.msg, got, tt.want)
		}
	}
}

// Subagent answers are kept apart and assembled only when they are sent.
func TestSubagentResults(t *testing.T) {
	m := NewSubagentResults("call-1", []SubagentResult{
		{Title: "Read the README", Answer: "it is a CLI"},
		{Title: "Count the tests", Answer: "forty"},
	})

	if got := m.Role(); got != RoleTool {
		t.Errorf("Role() = %q, want %q: it answers a tool request", got, RoleTool)
	}
	if got := m.AnsweredRequest(); got != "call-1" {
		t.Errorf("AnsweredRequest() = %q, want %q", got, "call-1")
	}
	if m.Truncatable() {
		t.Error("Truncatable() = true, want a tool result to last as long as the request")
	}

	const want = "## Read the README\nit is a CLI\n\n## Count the tests\nforty"
	if got := m.Render(); got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestSubagentResultsValidate(t *testing.T) {
	one := []SubagentResult{{Title: "T", Answer: "A"}}
	tests := []struct {
		name    string
		msg     SubagentResults
		wantErr string
	}{
		{"answering nothing", SubagentResults{Results: one}, "answer no request"},
		{"no results", SubagentResults{RequestID: "c1"}, "are empty"},
		{
			"a result with no title",
			SubagentResults{RequestID: "c1", Results: []SubagentResult{{Answer: "A"}}},
			"no title",
		},
		{
			"a result with no answer",
			SubagentResults{RequestID: "c1", Results: []SubagentResult{{Title: "T"}}},
			"returned nothing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.msg.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() = %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// Everything that answers a tool request goes through one interface, so a new
// kind of answer does not have to be found in every switch.
func TestToolAnswers(t *testing.T) {
	answers := []ToolAnswer{
		NewToolResult("call-1", "output"),
		NewSubagentResults("call-1", []SubagentResult{{Title: "T", Answer: "A"}}),
	}
	for _, a := range answers {
		if a.AnsweredRequest() != "call-1" {
			t.Errorf("%T.AnsweredRequest() = %q, want it to name the request", a, a.AnsweredRequest())
		}
		if a.Render() == "" {
			t.Errorf("%T.Render() is empty, so the model would read nothing", a)
		}
	}

	// A turn that does not answer a request must not satisfy it.
	var notAnAnswer any = NewUserMessage("hi")
	if _, ok := notAnAnswer.(ToolAnswer); ok {
		t.Error("a user turn satisfies ToolAnswer, so Validate would treat it as a tool result")
	}
}

// Results from a subagents call must satisfy the request they answer, the same
// as any other tool result.
func TestConversationValidatesSubagentResults(t *testing.T) {
	c := NewConversation("system", "split this up")
	c.AddAssistant(AssistantMessage{ToolRequests: []ToolRequest{
		{ID: "call-1", Name: "subagents"},
		{ID: "call-2", Name: "bash"},
	}})
	c.Append(NewSubagentResults("call-1", []SubagentResult{{Title: "T", Answer: "A"}}))
	// The result for the other call must still be recognized after it.
	c.AddToolResult("call-2", "output")

	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}

	stray := NewConversation("system", "go")
	stray.AddAssistant(NewAssistantMessage("done"))
	stray.Append(NewSubagentResults("call-9", []SubagentResult{{Title: "T", Answer: "A"}}))
	if err := stray.Validate(); err == nil {
		t.Error("Validate() = nil, want results answering no request refused")
	}
}
