package eventlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// logPath returns a path in a directory that lasts as long as the test.
func logPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "log.jsonl")
}

// read returns the events the log at path holds, checking as it goes that
// every line is one whole JSON object, which is the format.
func read(t *testing.T, path string) []Event {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() = %v", err)
	}
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return nil
	}

	lines := strings.Split(text, "\n")
	events := make([]Event, 0, len(lines))
	for i, line := range lines {
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %d is not one JSON object: %v (%q)", i+1, err, line)
		}
		events = append(events, e)
	}
	return events
}

func TestRecordWritesOneLinePerEvent(t *testing.T) {
	path := logPath(t)
	lg, err := Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	lg.Record(Event{Kind: RunStart, Model: "deepseek-chat", Content: "what is 2+2?"})
	lg.Record(Event{Kind: RunAnswer, Content: "4"})
	if err := lg.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	events := read(t, path)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Kind != RunStart || events[1].Kind != RunAnswer {
		t.Errorf("kinds = %q, %q, want %q, %q", events[0].Kind, events[1].Kind, RunStart, RunAnswer)
	}
	if events[0].Content != "what is 2+2?" {
		t.Errorf("Content = %q, want the prompt", events[0].Content)
	}
	for i, e := range events {
		if e.Time.IsZero() {
			t.Errorf("event %d has no time", i)
		}
		if e.Agent != MainAgent {
			t.Errorf("event %d is from %q, want %q", i, e.Agent, MainAgent)
		}
	}
}

// A field that means nothing for a kind is not on the line at all, which is
// what keeps a log readable without knowing every kind.
func TestUnusedFieldsAreOmitted(t *testing.T) {
	path := logPath(t)
	lg, err := Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	lg.Record(Event{Kind: RunAnswer, Content: "4"})
	if err := lg.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() = %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}

	want := []string{"kind", "agent", "content", "time"}
	if len(fields) != len(want) {
		t.Errorf("line has fields %v, want only %v", keys(fields), want)
	}
	for _, name := range want {
		if _, ok := fields[name]; !ok {
			t.Errorf("line has no %q field", name)
		}
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The keys of a line are in the order Event declares its fields, which is a
// property of the format and not an accident of encoding/json. What a line
// says comes first and the bookkeeping for joining lines together comes last:
// kind leads because it is what a reader filters by, while the correlation id
// and the timestamp trail because neither says anything to somebody reading
// one line. Reordering the struct reorders the file, so the order is asserted
// here rather than left to whoever edits the struct next.
func TestWhatALineSaysComesBeforeItsBookkeeping(t *testing.T) {
	path := logPath(t)
	lg, err := Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	lg.Record(Event{Kind: ToolResult, Tool: "bash", RequestID: "call_1", Content: "4", Elapsed: 7})
	if err := lg.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() = %v", err)
	}
	got := lineKeys(t, data)
	if len(got) == 0 {
		t.Fatal("the line has no keys, so this proves nothing")
	}
	if got[0] != "kind" {
		t.Errorf("line begins with %q, want %q: %v", got[0], "kind", got)
	}
	if last := got[len(got)-1]; last != "time" {
		t.Errorf("line ends with %q, want %q: %v", last, "time", got)
	}
	if at, after := indexOf(t, got, "request_id"), indexOf(t, got, "content"); at < after {
		t.Errorf("request_id comes before content, and belongs after it: %v", got)
	}
}

// indexOf returns where name is among keys, failing the test if it is absent,
// since an assertion about a key that was not written proves nothing.
func indexOf(t *testing.T, keys []string, name string) int {
	t.Helper()
	for i, k := range keys {
		if k == name {
			return i
		}
	}
	t.Fatalf("the line has no %q field: %v", name, keys)
	return -1
}

// lineKeys returns the keys of a JSON object in the order the document gives
// them, which unmarshalling into a map would throw away.
func lineKeys(t *testing.T, line []byte) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(string(line)))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("line does not begin an object: %v (%q)", err, line)
	}

	var out []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("reading a key: %v (%q)", err, line)
		}
		name, ok := tok.(string)
		if !ok {
			t.Fatalf("key is %v, not a string (%q)", tok, line)
		}
		out = append(out, name)

		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			t.Fatalf("reading the value of %q: %v (%q)", name, err, line)
		}
	}
	return out
}

// Arguments are stored as the JSON object they are, not as a string holding
// one, so a reader can work with them without unquoting anything.
func TestArgumentsStayJSON(t *testing.T) {
	path := logPath(t)
	lg, err := Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	lg.Record(Event{
		Kind:      ToolCall,
		Tool:      "bash",
		RequestID: "call_1",
		Arguments: json.RawMessage(`{"command":"uname -a"}`),
	})
	if err := lg.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	var line struct {
		Arguments struct {
			Command string `json:"command"`
		} `json:"arguments"`
	}
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &line); err != nil {
		t.Fatalf("arguments were not stored as an object: %v", err)
	}
	if line.Arguments.Command != "uname -a" {
		t.Errorf("command = %q, want %q", line.Arguments.Command, "uname -a")
	}
}

func TestDiscardedLogRecordsNothing(t *testing.T) {
	lg := Discarded()
	if lg.Recording() {
		t.Error("Recording() = true, want false")
	}

	// Every method has to work, since the point of a discarded log is that no
	// caller has to ask whether there is one.
	lg.Record(Event{Kind: RunStart})
	sub := lg.Agent("subagent")
	sub.Record(Event{Kind: SubagentStart})
	if sub.Recording() {
		t.Error("a subagent of a discarded log records")
	}
	if err := lg.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

// --no-log writes no file at all, rather than an empty one.
func TestOpenWithNoPathCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	lg, err := Open("")
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	lg.Record(Event{Kind: RunStart})
	if err := lg.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a discarded log left %d files behind", len(entries))
	}
}

// Every agent writes to the one file, and every line says which agent it came
// from: that is what makes a run that fanned out readable afterwards.
func TestAgentsAreNamedApart(t *testing.T) {
	path := logPath(t)
	lg, err := Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	first := lg.Agent("subagent-1.1")
	second := lg.Agent("subagent-1.2")
	if first.Name() != "subagent-1.1" {
		t.Errorf("Name() = %q, want the name it was given", first.Name())
	}
	if lg.Name() != MainAgent {
		t.Errorf("Name() = %q, want %q", lg.Name(), MainAgent)
	}

	lg.Record(Event{Kind: RunStart})
	first.Record(Event{Kind: SubagentStart, Title: "Read the README"})
	second.Record(Event{Kind: SubagentStart, Title: "Check the tests"})
	if err := lg.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	agents := make(map[string]string)
	for _, e := range read(t, path) {
		agents[e.Agent] = e.Title
	}
	if len(agents) != 3 {
		t.Fatalf("got events from %d agents, want 3: %v", len(agents), agents)
	}
	if agents[first.Name()] != "Read the README" {
		t.Errorf("%s recorded %q", first.Name(), agents[first.Name()])
	}
}

// Subagents record while the run that spawned them does, so nothing may be
// lost or interleaved halfway through a line.
func TestConcurrentAgentsAllReachTheFile(t *testing.T) {
	const agents, each = 8, 50

	path := logPath(t)
	lg, err := Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < agents; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sub := lg.Agent(fmt.Sprintf("subagent-1.%d", i+1))
			for j := 0; j < each; j++ {
				sub.Record(Event{Kind: RunAnswer, Content: sub.Name(), Attempt: j + 1})
			}
		}()
	}
	wg.Wait()
	if err := lg.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	events := read(t, path)
	if len(events) != agents*each {
		t.Fatalf("got %d events, want %d", len(events), agents*each)
	}

	// Every line is whole and attributed, and every agent's own lines are in
	// the order it recorded them: one writer means the file cannot tear or
	// reorder what it is given.
	last := make(map[string]int, agents)
	for _, e := range events {
		if e.Agent != e.Content {
			t.Fatalf("an event from %q says %q, so a line was written by two agents at once", e.Agent, e.Content)
		}
		if e.Attempt != last[e.Agent]+1 {
			t.Fatalf("%s recorded %d after %d, so the file is out of order", e.Agent, e.Attempt, last[e.Agent])
		}
		last[e.Agent] = e.Attempt
	}
	if len(last) != agents {
		t.Fatalf("got events from %d agents, want %d", len(last), agents)
	}
}

// Resuming a session runs agi again against the same directory, and the second
// run's events continue the log rather than replacing it.
func TestASecondRunAppends(t *testing.T) {
	path := logPath(t)

	for _, prompt := range []string{"what is 2+2?", "and times 3?"} {
		lg, err := Open(path)
		if err != nil {
			t.Fatalf("Open() = %v", err)
		}
		lg.Record(Event{Kind: RunStart, Content: prompt})
		if err := lg.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
	}

	events := read(t, path)
	if len(events) != 2 {
		t.Fatalf("got %d events, want both runs", len(events))
	}
	if events[0].Content != "what is 2+2?" || events[1].Content != "and times 3?" {
		t.Errorf("got %q then %q, want the first run's events kept", events[0].Content, events[1].Content)
	}
}

// A log holds the prompt, the answers, and everything a tool printed, in a
// directory the whole machine can read.
func TestLogIsPrivate(t *testing.T) {
	path := logPath(t)
	lg, err := Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	lg.Record(Event{Kind: RunStart})
	if err := lg.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() = %v", err)
	}
	if got := info.Mode().Perm(); got != filePerm {
		t.Errorf("mode = %v, want %v", got, filePerm)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	lg, err := Open(logPath(t))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if err := lg.Close(); err != nil {
		t.Errorf("second Close() = %v, want nil", err)
	}
}

// Recording after the log has closed is a mistake, but it must not hang the
// run that makes it.
func TestRecordAfterCloseIsDropped(t *testing.T) {
	path := logPath(t)
	lg, err := Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	lg.Record(Event{Kind: RunStart})
	if err := lg.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < buffer*2; i++ {
			lg.Record(Event{Kind: RunAnswer})
		}
	}()
	<-done

	if got := len(read(t, path)); got != 1 {
		t.Errorf("got %d events, want only the one recorded before Close", got)
	}
}

// failingWriter is a file that cannot be written to.
type failingWriter struct{ closed bool }

var errDiskFull = errors.New("no space left on device")

func (w *failingWriter) Write([]byte) (int, error) { return 0, errDiskFull }
func (w *failingWriter) Close() error              { w.closed = true; return nil }

// A log that cannot be written must not take the run down with it: the failure
// is reported once, by Close, and the run goes on producing the answer.
func TestCloseReportsTheFirstWriteFailure(t *testing.T) {
	w := &failingWriter{}
	lg := newLog(w)

	lg.Record(Event{Kind: RunStart})
	lg.Record(Event{Kind: RunAnswer})

	err := lg.Close()
	if !errors.Is(err, errDiskFull) {
		t.Errorf("Close() = %v, want it to report %v", err, errDiskFull)
	}
	if !w.closed {
		t.Error("the file was not closed")
	}
}

func TestOpenReportsAnUnusablePath(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "missing", "log.jsonl")); err == nil {
		t.Error("Open() = nil, want an error for a path that cannot be created")
	}
}

// A request line says where it went, and the backend leads the three fields
// that say it: it is the coarsest of them, and the one a reader can act on,
// since which backend a run used decides which environment variables are
// worth looking at.
func TestARequestLineSaysWhereItWent(t *testing.T) {
	path := logPath(t)
	lg, err := Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	lg.Record(Event{
		Kind:    ModelRequest,
		Backend: "litellm",
		Model:   "deepseek-chat",
		Gateway: "http://localhost:4000",
		Turn:    1,
	})
	if err := lg.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() = %v", err)
	}
	var line Event
	if err := json.Unmarshal(data, &line); err != nil {
		t.Fatalf("the line is not one JSON object: %v", err)
	}
	if line.Backend != "litellm" || line.Gateway != "http://localhost:4000" {
		t.Errorf("the line does not say where the request went: %+v", line)
	}

	keys := lineKeys(t, data)
	if at, before := indexOf(t, keys, "backend"), indexOf(t, keys, "model"); at > before {
		t.Errorf("backend comes after model, and is the coarser of the two: %v", keys)
	}
	if at, before := indexOf(t, keys, "model"), indexOf(t, keys, "gateway"); at > before {
		t.Errorf("model comes after gateway, and the two were always in that order: %v", keys)
	}
}

// A log records request bodies only when it was asked to, and the asking is
// the handle's rather than the caller's: everything that records a request
// already holds a log, so nothing between the command line and a request has
// to carry a flag.
func TestARequestBodyIsRecordedOnlyWhenAskedFor(t *testing.T) {
	lg, err := Open(logPath(t))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer lg.Close()

	if lg.RecordsRequestBody() {
		t.Error("a log records request bodies without being asked")
	}
	if !lg.WithRequestBody(true).RecordsRequestBody() {
		t.Error("a log asked for request bodies does not record them")
	}
	if lg.WithRequestBody(true).WithRequestBody(false).RecordsRequestBody() {
		t.Error("a log cannot be told to stop recording request bodies")
	}
	// The original handle is untouched, as it is by every other With method:
	// a copy is returned rather than the receiver changed.
	if lg.RecordsRequestBody() {
		t.Error("WithRequestBody changed the log it was called on")
	}
}

// A discarded log records nothing, request bodies included, and says so rather
// than claiming it would record one. That is what --no-log gives a run, and it
// is why the two flags contradict each other.
func TestADiscardedLogRecordsNoRequestBody(t *testing.T) {
	if Discarded().RecordsRequestBody() {
		t.Error("a discarded log claims it records request bodies")
	}
	if Discarded().WithRequestBody(true).RecordsRequestBody() {
		t.Error("a discarded log can be talked into recording request bodies")
	}
	var nilLog *Log
	if nilLog.RecordsRequestBody() {
		t.Error("a nil log claims it records request bodies")
	}
}

// Whether request bodies are recorded belongs to the run's log rather than to
// one agent of it, so a nested run inherits it the way it inherits the fork
// mark: without anything having to remember to pass it on.
func TestNestedRunsInheritWhetherBodiesAreRecorded(t *testing.T) {
	lg, err := Open(logPath(t))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer lg.Close()

	asked := lg.WithRequestBody(true)
	if !asked.Agent("agent-1").RecordsRequestBody() {
		t.Error("a subagent does not inherit that bodies are recorded")
	}
	if !asked.Fork("fork-1").RecordsRequestBody() {
		t.Error("a fork does not inherit that bodies are recorded")
	}
	// A subagent of a fork is two derivations deep, which is where a field
	// that is carried over by one method and dropped by the other shows up.
	if !asked.Fork("fork-1").Agent("fork-1.agent-1").RecordsRequestBody() {
		t.Error("a fork's subagent does not inherit that bodies are recorded")
	}
	// The fork mark still travels alongside it, which is the rule this one
	// was written to match.
	if !asked.Fork("fork-1").Agent("fork-1.agent-1").Forked() {
		t.Error("the fork mark stopped being carried over")
	}

	quiet := lg.WithRequestBody(false)
	if quiet.Agent("agent-1").RecordsRequestBody() {
		t.Error("a subagent records bodies its run did not ask for")
	}
}

// A request body is written as the object it is rather than a string holding
// one, so `.request_body.input` is a question a reader can ask of the file.
// It also sits ahead of the bookkeeping and behind every other field that
// says something, because it is larger than all of them together.
func TestARequestBodyIsEmbeddedAsAnObject(t *testing.T) {
	path := logPath(t)
	lg, err := Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	lg.Record(Event{
		Kind:        ModelRequest,
		Backend:     "litellm",
		Model:       "deepseek-chat",
		Gateway:     "http://localhost:4000",
		Turn:        1,
		RequestBody: json.RawMessage(`{"model":"deepseek-chat","input":[{"role":"user"}]}`),
	})
	if err := lg.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() = %v", err)
	}

	// Embedded rather than quoted: the body's own keys are reachable, which a
	// string holding JSON would have put behind an unquoting step.
	var line struct {
		Backend     string `json:"backend"`
		Gateway     string `json:"gateway"`
		RequestBody struct {
			Model string `json:"model"`
			Input []struct {
				Role string `json:"role"`
			} `json:"input"`
		} `json:"request_body"`
	}
	if err := json.Unmarshal(data, &line); err != nil {
		t.Fatalf("the line is not one JSON object with a nested body: %v", err)
	}
	if line.RequestBody.Model != "deepseek-chat" {
		t.Errorf("the body's model reads %q, want it reachable without unquoting", line.RequestBody.Model)
	}
	if len(line.RequestBody.Input) != 1 || line.RequestBody.Input[0].Role != "user" {
		t.Errorf("the body's input did not survive: %+v", line.RequestBody.Input)
	}
	if line.Backend != "litellm" || line.Gateway != "http://localhost:4000" {
		t.Errorf("the line does not say where the request went: %+v", line)
	}

	keys := lineKeys(t, data)
	if at, before := indexOf(t, keys, "request_body"), indexOf(t, keys, "turn"); at < before {
		t.Errorf("request_body comes before turn, and belongs after it: %v", keys)
	}
	if at, after := indexOf(t, keys, "request_body"), indexOf(t, keys, "time"); at > after {
		t.Errorf("request_body comes after time, and belongs before it: %v", keys)
	}
	if at, before := indexOf(t, keys, "backend"), indexOf(t, keys, "model"); at > before {
		t.Errorf("backend comes after model, and is the coarser of the two: %v", keys)
	}
}
