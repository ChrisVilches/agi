package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/prompt"
	"github.com/ChrisVilches/agi/tools"
	"github.com/ChrisVilches/agi/transport"
)

// scriptedClient replays canned responses and records what it was sent.
type scriptedClient struct {
	replies []model.AssistantMessage
	sent    []transport.Request
	err     error
}

func (c *scriptedClient) Send(_ context.Context, req transport.Request) (transport.Response, error) {
	// The conversation is a pointer that the loop keeps appending to, so record
	// a snapshot of the turns as they were for this request.
	snapshot := *req.Conversation
	snapshot.Messages = append([]model.Message(nil), req.Conversation.Messages...)
	req.Conversation = &snapshot
	c.sent = append(c.sent, req)
	if c.err != nil {
		return transport.Response{}, c.err
	}
	if len(c.replies) == 0 {
		return transport.Response{}, errors.New("scriptedClient: ran out of replies")
	}
	next := c.replies[0]
	c.replies = c.replies[1:]
	return transport.Response{Message: next}, nil
}

// errFailed is the failure a scriptedClient reports when it is set to fail.
var errFailed = errors.New("gateway exploded")

// fakeTool records its calls and returns a fixed output.
type fakeTool struct {
	name   string
	output string
	calls  []string
	prompt string // when set, the tool implements SystemPrompter
}

func (f *fakeTool) Definition() model.ToolDefinition {
	return model.ToolDefinition{Name: f.name, Description: "fake", Parameters: map[string]any{"type": "object"}}
}

func (f *fakeTool) Call(_ context.Context, args json.RawMessage) (string, error) {
	f.calls = append(f.calls, string(args))
	return f.output, nil
}

func (f *fakeTool) Announce(json.RawMessage) string { return "running " + f.name }

type promptingTool struct{ fakeTool }

func (p *promptingTool) SystemPrompt() string { return p.prompt }

// humanOutput sends both streams to buf, as if stdout were a terminal.
func humanOutput(buf *bytes.Buffer) output {
	return output{answer: buf, steps: buf}
}

// pipedOutput drops progress, as newOutput does when stdout is not a terminal.
func pipedOutput(buf *bytes.Buffer) output {
	return output{answer: buf, steps: io.Discard}
}

// asked returns the conversation a first question starts from, which is what
// the session hands the agent when there is nothing stored yet.
func asked(prompt string) *model.Conversation {
	return model.NewConversation("system", prompt)
}

func registryWith(t *testing.T, ts ...tools.Tool) *tools.Registry {
	t.Helper()
	r := tools.NewRegistry()
	if err := r.Register(ts...); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	return r
}

func TestAgentAnswersWithoutTools(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		model.NewAssistantMessage("the answer"),
	}}

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), client, registryWith(t), asked("question"), nil); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}
	if got := strings.TrimSpace(out.String()); got != "the answer" {
		t.Errorf("output = %q, want %q", got, "the answer")
	}
	if len(client.sent) != 1 {
		t.Fatalf("requests sent = %d, want 1", len(client.sent))
	}
	first, ok := client.sent[0].Conversation.Messages[0].(model.UserMessage)
	if !ok || first.Content != "question" {
		t.Errorf("first message = %+v, want the prompt as a user message", client.sent[0].Conversation.Messages[0])
	}
}

// The loop must run the requested tool, feed the result back, and answer.
func TestAgentRunsToolAndFeedsResultBack(t *testing.T) {
	tool := &fakeTool{name: "probe", output: "disk is fine"}
	client := &scriptedClient{replies: []model.AssistantMessage{
		{ToolRequests: []model.ToolRequest{
			{ID: "call-1", Name: "probe", Arguments: json.RawMessage(`{"a":1}`)},
		}},
		model.NewAssistantMessage("all good"),
	}}

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), client, registryWith(t, tool), asked("check"), nil); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}

	if len(tool.calls) != 1 || tool.calls[0] != `{"a":1}` {
		t.Errorf("tool calls = %v, want the model's arguments passed through", tool.calls)
	}
	if !strings.Contains(out.String(), "running probe") {
		t.Errorf("output = %q, want the announcement shown to the user", out.String())
	}

	// The second request must carry the assistant turn and the tool result.
	if len(client.sent) != 2 {
		t.Fatalf("requests sent = %d, want 2", len(client.sent))
	}
	msgs := client.sent[1].Conversation.Messages
	last, ok := msgs[len(msgs)-1].(model.ToolResultMessage)
	if !ok {
		t.Fatalf("last message = %T, want a tool result", msgs[len(msgs)-1])
	}
	if last.RequestID != "call-1" {
		t.Errorf("tool result id = %q, want it to answer call-1", last.RequestID)
	}
	if last.Content != "disk is fine" {
		t.Errorf("tool result content = %q, want the tool output", last.Content)
	}
}

// A tool that cannot be called must be reported to the model, not abort the run.
func TestAgentReportsToolFailureToModel(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		{ToolRequests: []model.ToolRequest{
			{ID: "call-1", Name: "missing", Arguments: json.RawMessage(`{}`)},
		}},
		model.NewAssistantMessage("recovered"),
	}}

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), client, registryWith(t), asked("go"), nil); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}
	msgs := client.sent[1].Conversation.Messages
	last, ok := msgs[len(msgs)-1].(model.ToolResultMessage)
	if !ok {
		t.Fatalf("last message = %T, want a tool result", msgs[len(msgs)-1])
	}
	if !strings.Contains(last.Content, "no tool named") {
		t.Errorf("tool result = %q, want the failure described to the model", last.Content)
	}
	if !last.Failed {
		t.Error("Failed = false, want the result marked as a harness failure")
	}
}

func TestAgentPropagatesTransportError(t *testing.T) {
	client := &scriptedClient{err: errors.New("gateway exploded")}

	var out bytes.Buffer
	err := runAgent(context.Background(), humanOutput(&out), client, registryWith(t), asked("go"), nil)
	if err == nil || !strings.Contains(err.Error(), "gateway exploded") {
		t.Fatalf("runAgent() = %v, want the transport error", err)
	}
}

// A model that only ever calls tools must not loop forever.
func TestAgentStopsAfterMaxTurns(t *testing.T) {
	tool := &fakeTool{name: "probe", output: "again"}
	replies := make([]model.AssistantMessage, maxTurns+1)
	for i := range replies {
		replies[i] = model.AssistantMessage{ToolRequests: []model.ToolRequest{
			{ID: "c", Name: "probe", Arguments: json.RawMessage(`{}`)},
		}}
	}
	client := &scriptedClient{replies: replies}

	var out bytes.Buffer
	err := runAgent(context.Background(), humanOutput(&out), client, registryWith(t, tool), asked("go"), nil)
	if err == nil || !strings.Contains(err.Error(), "gave up") {
		t.Fatalf("runAgent() = %v, want it to give up", err)
	}
	if len(client.sent) != maxTurns {
		t.Errorf("requests sent = %d, want the %d-turn cap", len(client.sent), maxTurns)
	}
}

// Tools are described to the model through their definitions, so every
// registered tool must reach the request.
func TestAgentSendsToolDefinitions(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{model.NewAssistantMessage("done")}}

	var out bytes.Buffer
	reg := registryWith(t, &fakeTool{name: "probe"})
	if err := runAgent(context.Background(), humanOutput(&out), client, reg, asked("go"), nil); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}
	defs := client.sent[0].Tools
	if len(defs) != 1 || defs[0].Name != "probe" {
		t.Errorf("tool definitions sent = %+v, want the registered tool", defs)
	}
}

// The system prompt must stay minimal for work tools, and grow only for
// control-flow tools that explain themselves.
func TestSystemPromptOnlyGrowsForControlFlowTools(t *testing.T) {
	plain := systemPrompt(registryWith(t, &fakeTool{name: "probe"}))
	if plain != prompt.Base() {
		t.Errorf("system prompt = %q, want only the base prompt for a work tool", plain)
	}
	if strings.Contains(plain, "probe") {
		t.Error("system prompt mentions a work tool, which must be described only by its definition")
	}

	guided := &promptingTool{fakeTool: fakeTool{name: "rewind"}}
	guided.prompt = "Rewind when an approach has failed."
	withGuidance := systemPrompt(registryWith(t, guided))
	if !strings.Contains(withGuidance, "Rewind when an approach has failed.") {
		t.Errorf("system prompt = %q, want the control-flow guidance included", withGuidance)
	}
	if !strings.HasPrefix(withGuidance, prompt.Base()) {
		t.Errorf("system prompt = %q, want it to start with the base prompt", withGuidance)
	}
}

// The two prompts differ only in their base: whichever one a run starts from,
// the control-flow guidance is appended to it.
func TestSubagentPromptReplacesOnlyTheBase(t *testing.T) {
	guided := &promptingTool{fakeTool: fakeTool{name: "spawn"}}
	guided.prompt = "Spawn when the work divides."
	reg := registryWith(t, guided)

	sub := subagentSystemPrompt(reg)
	if !strings.HasPrefix(sub, prompt.Subagent()) {
		t.Errorf("subagent prompt = %q, want it to start with the subagent instruction", sub)
	}
	if strings.Contains(sub, prompt.Base()) {
		t.Error("subagent prompt carries the ordinary base prompt, want its own in place of it")
	}
	if !strings.Contains(sub, "Spawn when the work divides.") {
		t.Errorf("subagent prompt = %q, want the tool guidance appended to it too", sub)
	}
}

func TestModelNameDefaultAndOverride(t *testing.T) {
	t.Setenv(EnvModel, "")
	if got := modelName(); got != DefaultModel {
		t.Errorf("modelName() = %q, want the default %q", got, DefaultModel)
	}
	t.Setenv(EnvModel, "some-other-model")
	if got := modelName(); got != "some-other-model" {
		t.Errorf("modelName() = %q, want the override", got)
	}
}

// A real run gets the work tools and the control-flow tools, and only the
// second kind may show up in the system prompt.
func TestDefaultRegistry(t *testing.T) {
	r, err := defaultRegistry(&scriptedClient{})
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	for _, name := range []string{"bash", "subagents"} {
		if _, ok := r.Lookup(name); !ok {
			t.Errorf("defaultRegistry() has no %s tool", name)
		}
	}

	got := systemPrompt(r)
	if !strings.Contains(got, prompt.ControlFlow()) {
		t.Errorf("system prompt = %q, want the control-flow guidance in it", got)
	}
	if strings.Contains(got, "bash") {
		t.Errorf("system prompt = %q, want bash described only by its definition", got)
	}
}

// A subagent gets the work tools but no control-flow tools: one that could
// spawn subagents would leave a run with no bottom.
func TestSubagentRegistryCannotSpawnMore(t *testing.T) {
	r, err := subagentRegistry()
	if err != nil {
		t.Fatalf("subagentRegistry() = %v", err)
	}
	if _, ok := r.Lookup("bash"); !ok {
		t.Error("subagentRegistry() has no bash tool")
	}
	if _, ok := r.Lookup("subagents"); ok {
		t.Error("subagentRegistry() can spawn subagents, so a run could recurse without end")
	}
	if got := subagentSystemPrompt(r); got != prompt.Subagent() {
		t.Errorf("subagent system prompt = %q, want the subagent instruction alone", got)
	}
}

// When stdout is piped the run must put the answer there and nothing else, so
// the next program in the pipeline reads clean data.
func TestAgentPipedPrintsOnlyTheAnswer(t *testing.T) {
	tool := &fakeTool{name: "probe", output: "disk is fine"}
	client := &scriptedClient{replies: []model.AssistantMessage{
		{Content: "let me look", ToolRequests: []model.ToolRequest{
			{ID: "call-1", Name: "probe", Arguments: json.RawMessage(`{}`)},
		}},
		model.NewAssistantMessage("all good"),
	}}

	var out bytes.Buffer
	if err := runAgent(context.Background(), pipedOutput(&out), client, registryWith(t, tool), asked("check"), nil); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}

	if got := out.String(); got != "all good\n" {
		t.Errorf("output = %q, want only the final answer", got)
	}
	// The tool still ran; only the reporting of it was suppressed.
	if len(tool.calls) != 1 {
		t.Errorf("tool calls = %d, want the tool to have run anyway", len(tool.calls))
	}
}

// The answer must be plain text when piped, with no escape codes.
func TestAgentPipedAnswerIsPlain(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		model.NewAssistantMessage("Arch Linux"),
	}}

	var out bytes.Buffer
	if err := runAgent(context.Background(), pipedOutput(&out), client, registryWith(t), asked("what distro"), nil); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Errorf("output = %q, want no escape codes", out.String())
	}
}
