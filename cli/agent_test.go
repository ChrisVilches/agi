package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/eventlog"
	"github.com/ChrisVilches/agi/hooks"
	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/prompt"
	"github.com/ChrisVilches/agi/session"
	"github.com/ChrisVilches/agi/tools"
	"github.com/ChrisVilches/agi/tools/native"
	"github.com/ChrisVilches/agi/tools/workspace"
	"github.com/ChrisVilches/agi/transport"
)

// testBackend answers the questions every transport.Backend is asked about
// itself, so a fake in these tests only has to write the part that matters to
// it: what it replies. Which model a run is on comes from the backend now, and
// a fake that could not say would not compile.
//
// Its catalogue is empty because nothing in these tests asks for one: an agent
// run never lists models, and the command that does has tests of its own.
type testBackend struct{}

func (testBackend) Name() string    { return "test-backend" }
func (testBackend) Model() string   { return "test-model" }
func (testBackend) BaseURL() string { return "http://gateway.test" }

func (testBackend) Models(context.Context) ([]transport.Model, error) { return nil, nil }

// scriptedClient replays canned responses and records what it was sent.
type scriptedClient struct {
	testBackend

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

// nolog is the log a test runs with when what is recorded is not what it is
// testing. It records nothing, which is what --no-log gives a run.
func nolog() *eventlog.Log { return eventlog.Discarded() }

// humanOutput sends both streams to buf, as if stdout were a terminal.
//
// It goes through route rather than building the value, so that a test is
// given the same output a run would have been. The two are not
// interchangeable any more: route also says whether each stream reaches
// anybody, which is what decides whether the display hooks run over a
// message.
func humanOutput(buf *bytes.Buffer) output {
	return route(true, buf, buf, false)
}

// pipedOutput drops progress, as newOutput does when stdout is not a terminal.
func pipedOutput(buf *bytes.Buffer) output {
	return route(false, buf, io.Discard, false)
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
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), client, registryWith(t), asked("question"), nil, false, defaultMaxTurns, hooks.None()); err != nil {
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
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), client, registryWith(t, tool), asked("check"), nil, false, defaultMaxTurns, hooks.None()); err != nil {
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
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), client, registryWith(t), asked("go"), nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}
	msgs := client.sent[1].Conversation.Messages
	last, ok := msgs[len(msgs)-1].(model.ToolFailure)
	if !ok {
		t.Fatalf("last message = %T, want a tool failure rather than an answer", msgs[len(msgs)-1])
	}
	if !strings.Contains(last.Reason, "no tool named") {
		t.Errorf("reason = %q, want the failure described to the model", last.Reason)
	}
	if !strings.Contains(out.String(), last.Render()) {
		t.Errorf("output = %q, want the user shown the words the model reads: %q", out.String(), last.Render())
	}
}

func TestAgentPropagatesTransportError(t *testing.T) {
	client := &scriptedClient{err: errors.New("gateway exploded")}

	var out bytes.Buffer
	err := runAgent(context.Background(), humanOutput(&out), nolog(), client, registryWith(t), asked("go"), nil, false, defaultMaxTurns, hooks.None())
	if err == nil || !strings.Contains(err.Error(), "gateway exploded") {
		t.Fatalf("runAgent() = %v, want the transport error", err)
	}
}

// A model that only ever calls tools must not loop forever, and the cap it
// stops at is the one the run was given rather than a number written here.
func TestAgentStopsAfterMaxTurns(t *testing.T) {
	const turns = minMaxTurns

	tool := &fakeTool{name: "probe", output: "again"}
	replies := make([]model.AssistantMessage, turns+1)
	for i := range replies {
		replies[i] = model.AssistantMessage{ToolRequests: []model.ToolRequest{
			{ID: "c", Name: "probe", Arguments: json.RawMessage(`{}`)},
		}}
	}
	client := &scriptedClient{replies: replies}

	var out bytes.Buffer
	err := runAgent(context.Background(), humanOutput(&out), nolog(), client, registryWith(t, tool), asked("go"), nil, false, turns, hooks.None())
	if err == nil || !strings.Contains(err.Error(), "gave up") {
		t.Fatalf("runAgent() = %v, want it to give up", err)
	}
	if len(client.sent) != turns {
		t.Errorf("requests sent = %d, want the %d-turn cap", len(client.sent), turns)
	}
}

// Tools are described to the model through their definitions, so every
// registered tool must reach the request.
func TestAgentSendsToolDefinitions(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{model.NewAssistantMessage("done")}}

	var out bytes.Buffer
	reg := registryWith(t, &fakeTool{name: "probe"})
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), client, reg, asked("go"), nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}
	defs := client.sent[0].Tools
	if len(defs) != 1 || defs[0].Name != "probe" {
		t.Errorf("tool definitions sent = %+v, want the registered tool", defs)
	}
}

// The system prompt must stay minimal for work tools, and grow only for
// native tools that explain themselves.
func TestSystemPromptOnlyGrowsForNativeTools(t *testing.T) {
	plain := systemPromptFor(prompt.Base(), registryWith(t, &fakeTool{name: "probe"}), "")
	if plain != prompt.Base() {
		t.Errorf("system prompt = %q, want only the base prompt for a work tool", plain)
	}
	if strings.Contains(plain, "probe") {
		t.Error("system prompt mentions a work tool, which must be described only by its definition")
	}

	guided := &promptingTool{fakeTool: fakeTool{name: "rewind"}}
	guided.prompt = "Rewind when an approach has failed."
	withGuidance := systemPromptFor(prompt.Base(), registryWith(t, guided), "")
	if !strings.Contains(withGuidance, "Rewind when an approach has failed.") {
		t.Errorf("system prompt = %q, want the native tools' guidance included", withGuidance)
	}
	if !strings.HasPrefix(withGuidance, prompt.Base()) {
		t.Errorf("system prompt = %q, want it to start with the base prompt", withGuidance)
	}
}

// The two prompts differ only in their base: whichever one a run starts from,
// the native tools' guidance is appended to it.
func TestSubagentPromptReplacesOnlyTheBase(t *testing.T) {
	guided := &promptingTool{fakeTool: fakeTool{name: "spawn"}}
	guided.prompt = "Spawn when the work divides."
	reg := registryWith(t, guided)

	sub := subagentSystemPrompt(reg, nil)
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

// A real run gets the work tools and the native tools, and only the
// second kind may show up in the system prompt.
func TestDefaultRegistry(t *testing.T) {
	r, err := defaultRegistry(wiring{out: quiet(), client: &scriptedClient{}, log: nolog(), backups: "", user: nil, found: nil, maxTurns: defaultMaxTurns, maxSubagents: defaultMaxSubagents})
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	for _, name := range []string{"bash", "mktemp", "patchfile", "writefile", "subagents", native.ForkTool} {
		if _, ok := r.Lookup(name); !ok {
			t.Errorf("defaultRegistry() has no %s tool", name)
		}
	}

	// Every native tool that can explain itself does, and each explanation reaches
	// the prompt on its own account rather than as part of one document.
	got := systemPromptFor(prompt.Base(), r, "")
	for _, guidance := range []string{prompt.Subagents(), prompt.Fork()} {
		if !strings.Contains(got, guidance) {
			t.Errorf("system prompt = %q, want the native tools' guidance in it", got)
		}
	}
	if strings.Contains(got, "bash") {
		t.Errorf("system prompt = %q, want bash described only by its definition", got)
	}
}

// A subagent is an ordinary run with the native tools taken away, so the
// two sets may differ by those and nothing else. Anything added to the work
// tools has to reach a subagent too, without anyone remembering to add it
// twice.
//
// loadskill is withheld like the other two, and is the one that comes back:
// a task that names skills gets it, which is tested where that is decided.
// What matters here is that it is not automatic, so a run's own skills do not
// leak into an agent whose brief said nothing about them.
func TestASubagentGetsEveryWorkToolAndNoNativeOnes(t *testing.T) {
	full, err := defaultRegistry(wiring{out: quiet(), client: &scriptedClient{}, log: nolog(), backups: "", user: nil, found: oneSkill(t), maxTurns: defaultMaxTurns, maxSubagents: defaultMaxSubagents})
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	sub, err := workspaceRegistry("", eventlog.MainAgent, nil, nil)
	if err != nil {
		t.Fatalf("workspaceRegistry() = %v", err)
	}

	withheld := make(map[string]bool)
	for _, d := range full.Definitions() {
		withheld[d.Name] = true
	}
	for _, d := range sub.Definitions() {
		if !withheld[d.Name] {
			t.Errorf("a subagent has %q, which an ordinary run does not", d.Name)
		}
		delete(withheld, d.Name)
	}

	if len(withheld) != 3 || !withheld[native.SubagentsTool] ||
		!withheld[native.ForkTool] || !withheld[native.LoadSkillTool] {
		var names []string
		for name := range withheld {
			names = append(names, name)
		}
		t.Errorf("a subagent is missing %v, want only the native tools withheld", names)
	}

	// Nothing forbids it in words; it simply has no such tool.
	if got := subagentSystemPrompt(sub, nil); got != prompt.Subagent() {
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
	if err := runAgent(context.Background(), pipedOutput(&out), nolog(), client, registryWith(t, tool), asked("check"), nil, false, defaultMaxTurns, hooks.None()); err != nil {
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
	if err := runAgent(context.Background(), pipedOutput(&out), nolog(), client, registryWith(t), asked("what distro"), nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Errorf("output = %q, want no escape codes", out.String())
	}
}

// The system prompt is assembled on every run and never stored, so the
// invocation is the only thing that decides it. That works only if the same
// arguments always produce the same prompt.
func TestSystemPromptIsTheSameForTheSameArguments(t *testing.T) {
	first, err := defaultRegistry(wiring{out: quiet(), client: &scriptedClient{}, log: nolog(), backups: "", user: nil, found: nil, maxTurns: defaultMaxTurns, maxSubagents: defaultMaxSubagents})
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	second, err := defaultRegistry(wiring{out: quiet(), client: &scriptedClient{}, log: nolog(), backups: "", user: nil, found: nil, maxTurns: defaultMaxTurns, maxSubagents: defaultMaxSubagents})
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}

	if systemPromptFor(prompt.Base(), first, "") != systemPromptFor(prompt.Base(), second, "") {
		t.Error("two runs with the same tools assembled different system prompts")
	}
	if systemPromptFor(prompt.Base(), first, "") != systemPromptFor(prompt.Base(), first, "") {
		t.Error("the same registry assembled different system prompts twice")
	}

	// Different tools, different prompt: that is the whole reason not to store
	// one, so it had better be true.
	if systemPromptFor(prompt.Base(), first, "") == subagentSystemPrompt(first, nil) {
		t.Error("a different base produced the same prompt")
	}
}

// A tool that keeps a copy of what it changes is told where to put it and
// which agent is asking, and both come from the caller. The directory is the
// session's, so the copies sit beside the snapshot and the log and go when
// they go.
func TestWorkToolsKeepTheirCopiesInTheSession(t *testing.T) {
	sess, err := session.New()
	if err != nil {
		t.Fatalf("session.New() = %v", err)
	}
	defer os.RemoveAll(sess.Dir())

	r, err := defaultRegistry(wiring{out: quiet(), client: &scriptedClient{}, log: nolog(), backups: sess.BackupDir(), user: nil, found: nil, maxTurns: defaultMaxTurns, maxSubagents: defaultMaxSubagents})
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}

	p := patchTool(t, r)
	if p.Backups != sess.BackupDir() {
		t.Errorf("patchfile keeps copies in %q, want the session's %q", p.Backups, sess.BackupDir())
	}
	if p.Agent != eventlog.MainAgent {
		t.Errorf("patchfile files copies under %q, want %q, which is the name the run records under",
			p.Agent, eventlog.MainAgent)
	}
}

// A subagent's copies go in the same place under its own name, and the name is
// the one its log lines carry so the two can be read together.
//
// It comes from the brief and not from the log, which is what makes this hold
// under --no-log: a log that records nothing has no name to give, and every
// subagent's copies would land under the same one.
func TestASubagentsCopiesCarryItsOwnName(t *testing.T) {
	for _, lg := range []*eventlog.Log{nolog(), eventlog.Discarded()} {
		name := subagentName(native.Brief{Task: native.Task{Title: "second"}, Number: 2})
		r, err := workspaceRegistry("/somewhere/backups", name, nil, nil)
		if err != nil {
			t.Fatalf("workspaceRegistry() = %v", err)
		}

		if got := patchTool(t, r).Agent; got != "subagent-2" {
			t.Errorf("a subagent's copies are filed under %q, want %q", got, "subagent-2")
		}
		if got := lg.Agent(name).Name(); got != "" && got != "subagent-2" {
			t.Errorf("its log lines are filed under %q, want the same name", got)
		}
	}
}

// patchTool digs the tool that keeps copies out of a registry.
func patchTool(t *testing.T, r *tools.Registry) *workspace.PatchFile {
	t.Helper()
	found, ok := r.Lookup("patchfile")
	if !ok {
		t.Fatal("the registry has no patchfile")
	}
	p, ok := found.(*workspace.PatchFile)
	if !ok {
		t.Fatalf("patchfile is a %T, want the tool that keeps copies", found)
	}
	return p
}

// The base a run was given is what the model reads, and the native tools'
// guidance is appended to it exactly as it is to the built-in one: --sys swaps
// the half that is meant to be swapped and nothing else.
func TestACustomBaseReachesTheModel(t *testing.T) {
	const custom = "You are a Zettelkasten librarian."
	guided := &promptingTool{fakeTool: fakeTool{name: "rewind"}}
	guided.prompt = "Rewind when an approach has failed."
	reg := registryWith(t, guided)

	sess, err := session.New()
	if err != nil {
		t.Fatalf("session.New() = %v", err)
	}
	defer os.RemoveAll(sess.Dir())

	client := &scriptedClient{replies: replies("noted")}
	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), nolog(), client, reg, sess, custom, "file this note", nil, nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	got := client.sent[0].Conversation.System
	if !strings.HasPrefix(got, custom) {
		t.Errorf("system prompt = %q, want it to start with the base the run was given", got)
	}
	if strings.Contains(got, prompt.Base()) {
		t.Error("the built-in base prompt was sent as well as the one the run was given")
	}
	if !strings.Contains(got, "Rewind when an approach has failed.") {
		t.Errorf("system prompt = %q, want the native tools' guidance appended to the custom base", got)
	}
}

// A subagent's prompt is not a character but the shape its answer has to take,
// so replacing the run's base leaves it alone.
func TestACustomBaseDoesNotReachASubagent(t *testing.T) {
	reg := registryWith(t, &fakeTool{name: "probe"})
	if got := subagentSystemPrompt(reg, nil); !strings.HasPrefix(got, prompt.Subagent()) {
		t.Errorf("subagent prompt = %q, want the instruction written for a spawned run", got)
	}
}

// The fan-out cap the command line was given is the one the tool advertises,
// and a fork is held to the same number. Both come out of one wiring, so the
// test asks each registry what its subagents tool would accept.
func TestTheFanOutCapReachesEveryAgentThatCanSpawn(t *testing.T) {
	const most = 3

	w := wiring{out: quiet(), client: &scriptedClient{}, log: nolog(), maxTurns: defaultMaxTurns, maxSubagents: most, agents: &native.Counter{}}
	run, err := defaultRegistry(w)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	fork, err := forkRegistry(w, "fork-1", nil)
	if err != nil {
		t.Fatalf("forkRegistry() = %v", err)
	}

	for name, r := range map[string]*tools.Registry{"the run": run, "a fork": fork} {
		spawner, ok := r.Lookup(native.SubagentsTool)
		if !ok {
			t.Fatalf("%s has no %s tool", name, native.SubagentsTool)
		}
		tasks := spawner.Definition().Parameters["properties"].(map[string]any)["tasks"].(map[string]any)
		if tasks["maxItems"] != most {
			t.Errorf("%s offers maxItems = %v, want the cap the run was given, %d", name, tasks["maxItems"], most)
		}
	}
}
