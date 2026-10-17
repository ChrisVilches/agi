package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/ChrisVilches/agi/eventlog"
	"github.com/ChrisVilches/agi/hooks"
	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/prompt"
	"github.com/ChrisVilches/agi/session"
	"github.com/ChrisVilches/agi/transport"
)

// recorded opens a session in a directory of its own, along with the log that
// belongs to it, and returns both plus a way to read back what was recorded.
// The log must be closed before reading: that is what waits for the last
// events to reach the file.
func recorded(t *testing.T) (*session.Session, *eventlog.Log, func() []eventlog.Event) {
	t.Helper()
	return recordedWith(t, false)
}

// recordedWith is recorded for a test that cares whether the run records the
// body of its requests, which is the one thing about a log that a flag turns
// on rather than off.
func recordedWith(t *testing.T, requestBody bool) (*session.Session, *eventlog.Log, func() []eventlog.Event) {
	t.Helper()

	sess, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	lg, err := sessionFlags{dir: sess.Dir(), requestBody: requestBody}.openLog(sess)
	if err != nil {
		t.Fatalf("openLog() = %v", err)
	}

	read := func() []eventlog.Event {
		t.Helper()
		if err := lg.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		data, err := os.ReadFile(sess.LogPath())
		if err != nil {
			t.Fatalf("ReadFile() = %v", err)
		}
		var events []eventlog.Event
		for i, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			var e eventlog.Event
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatalf("line %d is not one JSON object: %v", i+1, err)
			}
			events = append(events, e)
		}
		return events
	}
	return sess, lg, read
}

// kinds returns what happened, in order, which is what most of these tests are
// about: the log is the account of a run.
func kinds(events []eventlog.Event) []eventlog.Kind {
	out := make([]eventlog.Kind, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

// only returns the events of one kind.
func only(events []eventlog.Event, kind eventlog.Kind) []eventlog.Event {
	var out []eventlog.Event
	for _, e := range events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func assertKinds(t *testing.T, events []eventlog.Event, want ...eventlog.Kind) {
	t.Helper()
	got := kinds(events)
	if len(got) != len(want) {
		t.Fatalf("recorded %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recorded %v, want %v", got, want)
		}
	}
}

// A run that called a tool is accounted for from end to end: the prompt it was
// assembled with, every exchange with the gateway, the tool and what it said,
// the answer, and the snapshot being written.
func TestARunIsRecordedInFull(t *testing.T) {
	sess, lg, read := recorded(t)
	tool := &fakeTool{name: "bash", output: "Linux"}
	client := &scriptedClient{replies: []model.AssistantMessage{
		{ToolRequests: []model.ToolRequest{{ID: "call_1", Name: "bash", Arguments: json.RawMessage(`{"command":"uname"}`)}}},
		model.NewAssistantMessage("it is Linux"),
	}}

	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), lg, client, registryWith(t, tool), sess, prompt.Base(), "what system is this?", nil, nil, outcomeCheck{}, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	events := read()
	assertKinds(t, events,
		eventlog.SystemPrompt,
		eventlog.ModelRequest, eventlog.ModelResponse,
		eventlog.ToolCall, eventlog.ToolOutput, eventlog.ToolResult,
		eventlog.ModelRequest, eventlog.ModelResponse,
		eventlog.RunAnswer,
		eventlog.SessionSave,
	)

	// The prompt is recorded because nothing else keeps it: the snapshot
	// stores only what was said, and this is assembled from the invocation.
	if got, want := only(events, eventlog.SystemPrompt)[0].Content, systemPromptFor(prompt.Base(), registryWith(t, tool), ""); got != want {
		t.Errorf("the system prompt was recorded as %q, want %q", got, want)
	}
	call := only(events, eventlog.ToolCall)[0]
	if call.Tool != "bash" || call.RequestID != "call_1" {
		t.Errorf("tool call recorded as %+v", call)
	}
	if string(call.Arguments) != `{"command":"uname"}` {
		t.Errorf("arguments recorded as %s", call.Arguments)
	}
	if got := only(events, eventlog.ToolOutput)[0].Content; got != "Linux" {
		t.Errorf("tool output recorded as %q, want what the tool produced", got)
	}
	if got := only(events, eventlog.ToolResult)[0].Content; got != "Linux" {
		t.Errorf("tool result recorded as %q, want what the model read", got)
	}
	if got := only(events, eventlog.RunAnswer)[0].Content; got != "it is Linux" {
		t.Errorf("answer recorded as %q", got)
	}
	if got := only(events, eventlog.ModelResponse)[0].Requests; len(got) != 1 || got[0].Name != "bash" {
		t.Errorf("the reply's tool requests were recorded as %+v", got)
	}
}

// The log is a file and only a file. Progress belongs on the terminal and the
// answer belongs on stdout; a debugging stream on either would undo the care
// taken over which of them carries what.
func TestTheLogNeverReachesTheTerminal(t *testing.T) {
	sess, lg, read := recorded(t)
	client := &scriptedClient{replies: replies("4")}

	var out bytes.Buffer
	if err := runSession(context.Background(), humanOutput(&out), lg, client, registryWith(t), sess, prompt.Base(), "what is 2+2?", nil, nil, outcomeCheck{}, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}
	if events := read(); len(events) == 0 {
		t.Fatal("nothing was recorded, so this proves nothing")
	}

	for _, marker := range []string{`"seq"`, `"kind"`, string(eventlog.ModelRequest), string(eventlog.SystemPrompt)} {
		if strings.Contains(out.String(), marker) {
			t.Errorf("terminal output contains %s, and the log must never reach it: %q", marker, out.String())
		}
	}
}

// A tool call that could not be made is a failure in the log, not a result,
// the same distinction the conversation itself draws.
func TestAFailedToolCallIsRecordedAsOne(t *testing.T) {
	sess, lg, read := recorded(t)
	client := &scriptedClient{replies: []model.AssistantMessage{
		{ToolRequests: []model.ToolRequest{{ID: "call_1", Name: "nosuchtool"}}},
		model.NewAssistantMessage("never mind"),
	}}

	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), lg, client, registryWith(t), sess, prompt.Base(), "go", nil, nil, outcomeCheck{}, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	events := read()
	if len(only(events, eventlog.ToolResult)) != 0 {
		t.Error("a call that could not be made was recorded as a result")
	}
	failures := only(events, eventlog.ToolFailure)
	if len(failures) != 1 {
		t.Fatalf("recorded %v, want one tool failure", kinds(events))
	}
	if !strings.Contains(failures[0].Reason, "nosuchtool") {
		t.Errorf("failure recorded as %q, want the reason the model was given", failures[0].Reason)
	}
}

// A run that fails is recorded up to the point it failed, and the snapshot
// that was never written is not claimed to have been.
func TestAFailedRunIsStillRecorded(t *testing.T) {
	sess, lg, read := recorded(t)
	client := &scriptedClient{err: errFailed}

	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), lg, client, registryWith(t), sess, prompt.Base(), "go", nil, nil, outcomeCheck{}, defaultMaxTurns, hooks.None()); err == nil {
		t.Fatal("runSession() = nil, want the transport error")
	}

	events := read()
	assertKinds(t, events, eventlog.SystemPrompt, eventlog.ModelRequest, eventlog.ModelResponse)
	if got := only(events, eventlog.ModelResponse)[0].Reason; !strings.Contains(got, errFailed.Error()) {
		t.Errorf("the failed request was recorded as %q", got)
	}
}

// The point of the log: what the run took back out of the conversation is in
// it, and in the snapshot it is not. A schema run drops every turn of its
// restatement phase, so the log is the only place that phase survives.
func TestWhatTheSnapshotDropsTheLogKeeps(t *testing.T) {
	sess, lg, read := recorded(t)
	const document = `{"id": 1, "name": "Ada"}`
	client := &scriptedClient{replies: replies("user 1 is Ada", document)}

	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), lg, client, registryWith(t), sess, prompt.Base(), "who is user 1?", testSchema(t), nil, outcomeCheck{}, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	events := read()
	assertKinds(t, events,
		eventlog.SystemPrompt,
		eventlog.ModelRequest, eventlog.ModelResponse, eventlog.RunAnswer,
		eventlog.SchemaRestate, eventlog.HarnessInstruction,
		eventlog.ModelRequest, eventlog.ModelResponse,
		eventlog.SchemaAttempt, eventlog.SchemaAccepted,
		eventlog.Drop,
		eventlog.SessionSave,
	)
	if got := only(events, eventlog.SchemaAccepted)[0].Content; got != document {
		t.Errorf("the accepted document was recorded as %q", got)
	}

	// The drop says how much went, since that is all the snapshot will not.
	drop := only(events, eventlog.Drop)[0]
	if drop.Dropped != 2 {
		t.Errorf("Dropped = %d, want the instruction and the document that answered it", drop.Dropped)
	}
	if drop.From != 2 {
		t.Errorf("From = %d, want the turn the instruction went in at", drop.From)
	}

	// What was dropped is only readable because the instruction that drove it
	// was recorded in full. The two lines divide the phase between them: the
	// restatement carries the shape asked for, the instruction the words.
	if got := only(events, eventlog.SchemaRestate)[0]; got.Content != "" {
		t.Errorf("schema.restate carries %q, want the instruction left to its own line", got.Content)
	}
	if got := only(events, eventlog.HarnessInstruction)[0].Content; got != schemaInstruction(testSchema(t)) {
		t.Errorf("the instruction was recorded as %q, want the words the model read", got)
	}

	stored, err := sess.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if stored.Len() != 2 {
		t.Fatalf("the snapshot holds %d turns, want the question and the prose answer", stored.Len())
	}
}

// A rejected document and the reason it was rejected are both recorded, which
// is how a run that spent its attempts can be understood afterwards.
func TestRejectedAttemptsAreRecorded(t *testing.T) {
	sess, lg, read := recorded(t)
	const document = `{"id": 1, "name": "Ada"}`
	client := &scriptedClient{replies: replies(
		"user 1 is Ada",
		`{"id": "one", "name": "Ada"}`,
		document,
	)}

	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), lg, client, registryWith(t), sess, prompt.Base(), "who is user 1?", testSchema(t), nil, outcomeCheck{}, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	events := read()
	attempts := only(events, eventlog.SchemaAttempt)
	if len(attempts) != 2 {
		t.Fatalf("recorded %d attempts, want both", len(attempts))
	}
	rejected := only(events, eventlog.SchemaRejected)
	if len(rejected) != 1 {
		t.Fatalf("recorded %d rejections, want one", len(rejected))
	}
	if rejected[0].Attempt != 1 || !strings.Contains(rejected[0].Reason, "id") {
		t.Errorf("rejection recorded as %+v, want the first attempt and why it failed", rejected[0])
	}
}

// fanoutClient answers the first request with a call to the subagents tool and
// every request after it — the parent's and the subagents' alike — with a
// plain answer. It is safe to call from several goroutines, because subagents
// run at the same time.
type fanoutClient struct {
	testBackend
	mu    sync.Mutex
	calls int
}

func (c *fanoutClient) Send(_ context.Context, _ transport.Request) (transport.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls > 1 {
		return transport.Response{Message: model.NewAssistantMessage("done")}, nil
	}
	return transport.Response{Message: model.AssistantMessage{
		ToolRequests: []model.ToolRequest{{
			ID:        "call_1",
			Name:      "subagents",
			Arguments: json.RawMessage(`{"tasks":[{"title":"Read the README","body":"read it"},{"title":"Check the tests","body":"run them"}]}`),
		}},
	}}, nil
}

// Subagents are silent on the terminal because their steps would interleave
// into nonsense, but every line of a log says which agent wrote it, so they
// record everything an ordinary run records.
func TestSubagentsRecordUnderTheirOwnNames(t *testing.T) {
	sess, lg, read := recorded(t)
	client := &fanoutClient{}
	registry, err := defaultRegistry(wiring{out: quiet(), client: client, log: lg, backups: "", user: nil, found: nil, maxTurns: defaultMaxTurns, maxSubagents: defaultMaxSubagents})
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}

	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), lg, client, registry, sess, prompt.Base(), "look around", nil, nil, outcomeCheck{}, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	events := read()
	started := only(events, eventlog.SubagentStart)
	if len(started) != 2 {
		t.Fatalf("recorded %d subagents, want 2: %v", len(started), kinds(events))
	}

	// The names follow the order the tasks were written, not the order the
	// goroutines got going, so the same call files the same agent under the
	// same name every time.
	titles := make(map[string]string, 2)
	for _, e := range started {
		if e.Agent == "main" {
			t.Errorf("a subagent recorded as the main run")
		}
		titles[e.Agent] = e.Title
	}
	if titles["subagent-1"] != "Read the README" || titles["subagent-2"] != "Check the tests" {
		t.Errorf("subagents recorded as %v, want each named after its place in the call", titles)
	}
	if len(only(events, eventlog.SubagentFinish)) != 2 {
		t.Error("a subagent did not record how it ended")
	}

	// A subagent is an ordinary run, so its own exchanges are in the log too,
	// under its name rather than the main run's.
	for _, e := range events {
		if e.Kind == eventlog.ModelRequest && e.Agent != "main" {
			return
		}
	}
	t.Error("no subagent recorded a request of its own")
}

// --no-log makes the stream a pass-through: the run happens and the directory
// it is kept in holds the snapshot alone.
func TestNoLogRecordsNothing(t *testing.T) {
	sess, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	lg, err := sessionFlags{dir: sess.Dir(), noLog: true}.openLog(sess)
	if err != nil {
		t.Fatalf("openLog() = %v", err)
	}
	if lg.Recording() {
		t.Error("--no-log returned a log that records")
	}

	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), lg, &scriptedClient{replies: replies("4")},
		registryWith(t), sess, prompt.Base(), "what is 2+2?", nil, nil, outcomeCheck{}, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	if _, err := os.Stat(sess.LogPath()); !os.IsNotExist(err) {
		t.Errorf("Stat(%s) = %v, want no log at all", sess.LogPath(), err)
	}
	if _, err := sess.Load(); err != nil {
		t.Errorf("the run was still stored: Load() = %v", err)
	}
}

// --rm records like any other run. It gets a directory of its own and writes
// the log into it, and the file goes when the directory does — which is what
// leaves the log readable while the run it describes is still happening.
// Asking for no log at all is --no-log, which is a separate thing.
func TestARemovedRunStillRecords(t *testing.T) {
	sess, err := sessionFlags{discard: true}.open()
	if err != nil {
		t.Fatalf("open() = %v", err)
	}
	defer sess.Remove()

	rm, err := sessionFlags{discard: true}.openLog(sess)
	if err != nil {
		t.Fatalf("openLog() = %v", err)
	}
	if !rm.Recording() {
		t.Error("a --rm run was not given a log, want one that lasts as long as the run")
	}
	if err := rm.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

// The log belongs to the session, so it lands beside the snapshot and a second
// run against the same session continues it.
func TestTheLogLivesInTheSessionDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, question := range []string{"what is 2+2?", "and times 3?"} {
		sess, err := session.Open(dir)
		if err != nil {
			t.Fatalf("Open() = %v", err)
		}
		lg, err := sessionFlags{dir: dir}.openLog(sess)
		if err != nil {
			t.Fatalf("openLog() = %v", err)
		}
		var out bytes.Buffer
		if err := runSession(context.Background(), pipedOutput(&out), lg, &scriptedClient{replies: replies("ok")},
			registryWith(t), sess, prompt.Base(), question, nil, nil, outcomeCheck{}, defaultMaxTurns, hooks.None()); err != nil {
			t.Fatalf("runSession() = %v, want nil", err)
		}
		if err := lg.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
	}

	data, err := os.ReadFile(sessionLogPath(t, dir))
	if err != nil {
		t.Fatalf("ReadFile() = %v", err)
	}
	if !strings.Contains(string(data), string(eventlog.SessionResume)) {
		t.Error("the second run did not record that it resumed the session")
	}
	if strings.Count(string(data), string(eventlog.SessionSave)) != 2 {
		t.Error("the second run replaced the first run's log instead of continuing it")
	}
}

func sessionLogPath(t *testing.T, dir string) string {
	t.Helper()
	sess, err := session.Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	return sess.LogPath()
}

// The same rule for the --exit-code phase: the question, every attempt and the
// verdict are taken back out of the conversation, so the log is the only place
// the grading of a run survives.
func TestTheVerdictIsOnlyInTheLog(t *testing.T) {
	sess, lg, read := recorded(t)
	client := &scriptedClient{replies: replies(
		"There is no config.toml here.",
		`{"success": "no"}`,
		`{"success": false, "reason": "This repository has no config.toml."}`,
	)}

	var out bytes.Buffer
	err := runSession(context.Background(), pipedOutput(&out), lg, client, registryWith(t), sess, prompt.Base(), "summarize config.toml", nil, nil, outcomeCheck{asked: true}, defaultMaxTurns, hooks.None())
	var short *incomplete
	if !errors.As(err, &short) {
		t.Fatalf("runSession() = %v, want an incomplete task", err)
	}

	events := read()
	assertKinds(t, events,
		eventlog.SystemPrompt,
		eventlog.ModelRequest, eventlog.ModelResponse, eventlog.RunAnswer,
		eventlog.OutcomeAsk, eventlog.HarnessInstruction,
		eventlog.ModelRequest, eventlog.ModelResponse,
		eventlog.OutcomeAttempt, eventlog.OutcomeRejected, eventlog.HarnessInstruction,
		eventlog.ModelRequest, eventlog.ModelResponse,
		eventlog.OutcomeAttempt, eventlog.OutcomeIncomplete,
		eventlog.Drop,
		eventlog.SessionSave,
	)

	// Which of the two the run came to is the kind rather than a field, so a
	// reader filtering the log groups by it like everything else.
	if len(only(events, eventlog.OutcomeCompleted)) != 0 {
		t.Error("a run that reported the task incomplete also recorded a completed outcome")
	}
	verdict := only(events, eventlog.OutcomeIncomplete)[0]
	if verdict.Reason != "This repository has no config.toml." {
		t.Errorf("the reason was recorded as %q", verdict.Reason)
	}
	if verdict.Attempt != 2 {
		t.Errorf("Attempt = %d, want the attempt the verdict was accepted on", verdict.Attempt)
	}

	// The two lines divide the phase between them, as the schema phase's do:
	// outcome.ask says the question was put, the instruction carries the words.
	if got := only(events, eventlog.OutcomeAsk)[0]; got.Content != "" {
		t.Errorf("outcome.ask carries %q, want the instruction left to its own line", got.Content)
	}
	if got := only(events, eventlog.HarnessInstruction)[0].Content; got != outcomeInstruction("") {
		t.Errorf("the instruction was recorded as %q, want the words the model read", got)
	}
	if got := only(events, eventlog.OutcomeRejected)[0].Reason; !strings.Contains(got, `field "success"`) {
		t.Errorf("the rejection was recorded as %q", got)
	}

	drop := only(events, eventlog.Drop)[0]
	if drop.Dropped != 4 {
		t.Errorf("Dropped = %d, want the question, the rejected attempt, the correction and the verdict", drop.Dropped)
	}

	stored, err := sess.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if stored.Len() != 2 {
		t.Fatalf("the snapshot holds %d turns, want the question and the answer", stored.Len())
	}
}

// With both flags the two phases are one tail and are taken out in one cut:
// the run drops everything after the answer once, so the log carries a single
// drop covering both phases rather than one per phase.
func TestBothPhasesAreDroppedTogether(t *testing.T) {
	sess, lg, read := recorded(t)
	const document = `{"id": 1, "name": "Ada"}`
	client := &scriptedClient{replies: replies("user 1 is Ada", document, `{"success": true}`)}

	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), lg, client, registryWith(t), sess, prompt.Base(), "who is user 1?", testSchema(t), nil, outcomeCheck{asked: true}, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	events := read()
	drops := only(events, eventlog.Drop)
	if len(drops) != 1 {
		t.Fatalf("recorded %d drops, want one for both phases", len(drops))
	}
	if drops[0].From != 2 || drops[0].Dropped != 4 {
		t.Errorf("drop = from %d, %d turns, want from 2, the restatement, the document, the question and the verdict", drops[0].From, drops[0].Dropped)
	}
	if last := events[len(events)-2].Kind; last != eventlog.Drop {
		t.Errorf("event before the save = %s, want the drop to come after both phases", last)
	}

	stored, err := sess.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if stored.Len() != 2 {
		t.Fatalf("the snapshot holds %d turns, want the question and the answer", stored.Len())
	}
}
