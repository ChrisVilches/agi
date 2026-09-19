package session

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/model"
)

// conversation returns a run with one of every kind of turn in it.
func conversation() *model.Conversation {
	c := model.NewConversation("you are a helpful assistant", "what is the status?")
	c.AddAssistant(model.AssistantMessage{
		Content: "let me look",
		ToolRequests: []model.ToolRequest{
			{ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{"command":"uname -a"}`)},
		},
	})
	c.AddToolResult("call-1", "Linux")
	c.AddAssistant(model.AssistantMessage{
		ToolRequests: []model.ToolRequest{{ID: "call-2", Name: "subagents"}},
	})
	c.AddSubagentResults("call-2", []model.SubagentResult{
		{Title: "First", Answer: "one"},
		{Title: "Second", Answer: "two"},
	})
	c.AddAssistant(model.AssistantMessage{
		ToolRequests: []model.ToolRequest{{ID: "call-3", Name: "nope"}},
	})
	c.AddToolFailure("call-3", errors.New(`no tool named "nope"`))
	c.AddHarnessInstruction("answer in JSON")
	c.AddAssistant(model.NewAssistantMessage("it is fine"))
	return c
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "run"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	want := conversation()
	if err := s.Save(want); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got == nil {
		t.Fatal("Load() = nil, want the stored conversation")
	}
	// The system prompt is not part of what was said, so it is not stored and
	// does not come back; Resume is where a run gets one.
	if got.System != "" {
		t.Errorf("System = %q, want none stored", got.System)
	}
	if got.Len() != want.Len() {
		t.Fatalf("Len() = %d, want %d", got.Len(), want.Len())
	}
	for i, m := range got.Messages {
		if wantMsg := want.Messages[i]; !equalMessages(m, wantMsg) {
			t.Errorf("turn %d = %#v, want %#v", i, m, wantMsg)
		}
	}
}

// equalMessages compares two turns including their type, which is the part
// that has to survive a trip through a file with no types in it.
func equalMessages(a, b model.Message) bool {
	switch x := a.(type) {
	case model.UserMessage:
		y, ok := b.(model.UserMessage)
		return ok && x == y
	case model.HarnessInstruction:
		y, ok := b.(model.HarnessInstruction)
		return ok && x == y
	case model.ToolResultMessage:
		y, ok := b.(model.ToolResultMessage)
		return ok && x == y
	case model.ToolFailure:
		y, ok := b.(model.ToolFailure)
		return ok && x == y
	case model.SubagentResults:
		y, ok := b.(model.SubagentResults)
		return ok && x.RequestID == y.RequestID && slices.Equal(x.Results, y.Results)
	case model.AssistantMessage:
		y, ok := b.(model.AssistantMessage)
		if !ok || x.Content != y.Content || len(x.ToolRequests) != len(y.ToolRequests) {
			return false
		}
		for i, r := range x.ToolRequests {
			o := y.ToolRequests[i]
			if r.ID != o.ID || r.Name != o.Name || !sameJSON(r.Arguments, o.Arguments) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// sameJSON compares two JSON documents by what they say rather than how they
// are spelled, which is all a snapshot promises to keep: arguments are stored
// as JSON and come back re-rendered, and a tool unmarshals them either way.
func sameJSON(a, b []byte) bool {
	// A request with no arguments stores nothing and comes back as nothing.
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}

	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &y); err != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// What is stored is agi's own account of the run. A harness instruction is a
// harness instruction on disk, not the user turn it becomes on the wire.
func TestSnapshotIsStoredInAgisOwnTerms(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	defer os.RemoveAll(s.Dir())

	if err := s.Save(conversation()); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir(), snapshotFile))
	if err != nil {
		t.Fatalf("reading the snapshot = %v", err)
	}
	text := string(data)

	if !strings.Contains(text, `"kind": "harness_instruction"`) {
		t.Errorf("snapshot = %s, want the instruction stored as its own kind", text)
	}
	if strings.Contains(text, "[Instructions]") {
		t.Error("snapshot carries the wire rendering, want the instruction stored unwrapped")
	}
	// Tool arguments are stored as the JSON they are, not as a string of it.
	if !strings.Contains(text, `"command": "uname -a"`) {
		t.Errorf("snapshot = %s, want readable tool arguments", text)
	}
	// Subagent answers stay apart, not flattened into the text they render to.
	if !strings.Contains(text, `"kind": "subagent_results"`) || !strings.Contains(text, `"title": "First"`) {
		t.Errorf("snapshot = %s, want each subagent answer stored on its own", text)
	}
	if strings.Contains(text, "subagent result #1") {
		t.Error("snapshot carries the rendered sections, want the answers kept apart")
	}

	// A refused call is stored as a refusal, with the reason unmarked.
	if !strings.Contains(text, `"kind": "tool_failure"`) || !strings.Contains(text, `"reason": "no tool named`) {
		t.Errorf("snapshot = %s, want the failure stored as its own kind", text)
	}
	if strings.Contains(text, "error: no tool named") {
		t.Error("snapshot carries the rendered failure, want the reason stored on its own")
	}

	// Nothing assembled from how agi was invoked belongs in the file.
	if strings.Contains(text, `"system"`) || strings.Contains(text, "helpful assistant") {
		t.Errorf("snapshot = %s, want no system prompt stored", text)
	}

	// The roles a provider uses are the gateway's business, not the file's.
	if strings.Contains(text, `"role"`) {
		t.Errorf("snapshot = %s, want no provider roles in it", text)
	}
}

// A second run against the same session continues the conversation: the stored
// turns come back and the new prompt is the next thing said.
func TestResumeContinuesTheConversation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chat")
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	first, err := s.Resume("system", "what is 2+2?")
	if err != nil {
		t.Fatalf("Resume() = %v", err)
	}
	if first.Len() != 1 {
		t.Fatalf("a fresh session resumed into %d turns, want just the prompt", first.Len())
	}
	first.AddAssistant(model.NewAssistantMessage("4"))
	if err := s.Save(first); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	// A second run, as if agi had been invoked again with the same --session.
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	second, err := reopened.Resume("system", "and times 3?")
	if err != nil {
		t.Fatalf("Resume() = %v", err)
	}

	if second.Len() != 3 {
		t.Fatalf("resumed into %d turns, want the exchange plus the new prompt: %+v", second.Len(), second.Messages)
	}
	last, ok := second.Messages[2].(model.UserMessage)
	if !ok || last.Content != "and times 3?" {
		t.Errorf("last turn = %#v, want the new prompt as a user turn", second.Messages[2])
	}
	if err := second.Validate(); err != nil {
		t.Errorf("Validate() = %v, want a usable conversation", err)
	}
}

// The system prompt comes from how this run was invoked, every time. A session
// resumed after the tools or the base prompt changed is owed the prompt its own
// arguments describe, which is why none is kept.
func TestResumeTakesTheCurrentSystemPrompt(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "chat"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	first, _ := s.Resume("old prompt", "hello")
	first.AddAssistant(model.NewAssistantMessage("hi"))
	if err := s.Save(first); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	second, err := s.Resume("new prompt", "again")
	if err != nil {
		t.Fatalf("Resume() = %v", err)
	}
	if second.System != "new prompt" {
		t.Errorf("System = %q, want the prompt this run was given", second.System)
	}

	// Loading it directly gives the turns and nothing else.
	loaded, err := s.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if loaded.System != "" {
		t.Errorf("Load().System = %q, want none: no prompt was ever stored", loaded.System)
	}
}

// --rm: the run happens and nothing is kept.
func TestDiscardedSessionStoresNothing(t *testing.T) {
	s := Discarded()
	if s.Stored() {
		t.Error("Stored() = true, want a session that keeps nothing")
	}
	if s.Dir() != "" {
		t.Errorf("Dir() = %q, want none", s.Dir())
	}
	if err := s.Save(conversation()); err != nil {
		t.Errorf("Save() = %v, want storing nothing to succeed", err)
	}

	conv, err := s.Load()
	if err != nil || conv != nil {
		t.Errorf("Load() = %v, %v, want nothing and no error", conv, err)
	}
	conv, err = s.Resume("system", "hello")
	if err != nil {
		t.Fatalf("Resume() = %v", err)
	}
	if conv.Len() != 1 {
		t.Errorf("Resume() gave %d turns, want a conversation starting fresh every time", conv.Len())
	}
}

// A session agi names for itself must be new, so nothing is ever overwritten.
func TestNewCreatesAnUnusedDirectory(t *testing.T) {
	first, err := New()
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	defer os.RemoveAll(first.Dir())
	second, err := New()
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	defer os.RemoveAll(second.Dir())

	if first.Dir() == second.Dir() {
		t.Errorf("two sessions landed on %s, want a directory each", first.Dir())
	}
	for _, s := range []*Session{first, second} {
		info, err := os.Stat(s.Dir())
		if err != nil {
			t.Fatalf("Stat(%s) = %v", s.Dir(), err)
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", s.Dir())
		}
		if perm := info.Mode().Perm(); perm != dirPerm {
			t.Errorf("%s has mode %v, want %v: a conversation is its owner's", s.Dir(), perm, dirPerm)
		}
		// Nothing has run yet, so there is nothing to load.
		if conv, err := s.Load(); conv != nil || err != nil {
			t.Errorf("Load() of a fresh session = %v, %v, want nothing and no error", conv, err)
		}
	}
}

// A snapshot holds what the machine was told and what it answered, so it is
// readable only by its owner.
func TestSnapshotIsPrivate(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "run"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if err := s.Save(conversation()); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	info, err := os.Stat(filepath.Join(s.Dir(), snapshotFile))
	if err != nil {
		t.Fatalf("Stat() = %v", err)
	}
	if perm := info.Mode().Perm(); perm != filePerm {
		t.Errorf("snapshot has mode %v, want %v", perm, filePerm)
	}
}

// Saving replaces what was there rather than adding to it, and leaves no
// working files behind.
func TestSaveReplacesTheSnapshot(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "run"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if err := s.Save(conversation()); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	shorter := model.NewConversation("system", "hello")
	shorter.AddAssistant(model.NewAssistantMessage("hi"))
	if err := s.Save(shorter); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got.Len() != 2 {
		t.Errorf("Len() = %d, want the second conversation only", got.Len())
	}

	entries, err := os.ReadDir(s.Dir())
	if err != nil {
		t.Fatalf("ReadDir() = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != snapshotFile {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("session holds %v, want only %s", names, snapshotFile)
	}
}

func TestOpen(t *testing.T) {
	root := t.TempDir()

	t.Run("creates a directory that is not there", func(t *testing.T) {
		dir := filepath.Join(root, "nested", "run")
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Open() = %v", err)
		}
		if s.Dir() != dir {
			t.Errorf("Dir() = %q, want %q", s.Dir(), dir)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("Stat(%s) = %v, want the directory created", dir, err)
		}
	})

	t.Run("refuses a path that is not a directory", func(t *testing.T) {
		file := filepath.Join(root, "afile")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile() = %v", err)
		}
		if _, err := Open(file); err == nil {
			t.Error("Open() of a file = nil, want an error")
		}
	})

	t.Run("refuses an empty path", func(t *testing.T) {
		if _, err := Open(""); err == nil {
			t.Error("Open(\"\") = nil, want an error")
		}
	})
}

// A file that has been edited into something unusable must fail loudly rather
// than confuse the model halfway through the next run.
func TestLoadRejectsAnUnusableSnapshot(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"not JSON", "{oh no", "session:"},
		{"a kind that does not exist", `{"messages": [{"kind": "oracle"}]}`, `unknown kind "oracle"`},
		{
			"a turn that is not well formed",
			`{"messages": [{"kind": "user", "content": ""}]}`,
			"not usable",
		},
		{
			"a tool result answering nothing",
			`{"messages": [{"kind": "tool_result", "request_id": "c1", "content": "out"}]}`,
			"not usable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "run"))
			if err != nil {
				t.Fatalf("Open() = %v", err)
			}
			if err := os.WriteFile(filepath.Join(s.Dir(), snapshotFile), []byte(tt.content), 0o600); err != nil {
				t.Fatalf("WriteFile() = %v", err)
			}

			conv, err := s.Load()
			if err == nil {
				t.Fatalf("Load() = %+v, want an error", conv)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Load() = %q, want it to mention %q", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), snapshotFile) {
				t.Errorf("Load() = %q, want it to name the file", err)
			}
		})
	}
}

// A missing snapshot is how every session starts, not a failure.
func TestLoadOfAnEmptySessionIsNotAnError(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "run"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	conv, err := s.Load()
	if conv != nil || err != nil {
		t.Errorf("Load() = %v, %v, want nothing and no error", conv, err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), snapshotFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat() = %v, want no snapshot written by a load", err)
	}
}

// The log belongs to the session's directory, and a session that stores
// nothing has nowhere to put one.
func TestLogPath(t *testing.T) {
	dir := t.TempDir()
	sess, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	want := filepath.Join(dir, logFile)
	if got := sess.LogPath(); got != want {
		t.Errorf("LogPath() = %q, want %q", got, want)
	}
	if got := sess.LogPath(); got == sess.path() {
		t.Errorf("the log and the snapshot are the same file (%q)", got)
	}
	if got := Discarded().LogPath(); got != "" {
		t.Errorf("Discarded().LogPath() = %q, want nowhere", got)
	}
}

// Saving a run must not disturb the log sitting beside it: the two files are
// written by different things, for different reasons.
func TestSaveLeavesTheLogAlone(t *testing.T) {
	dir := t.TempDir()
	sess, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if err := os.WriteFile(sess.LogPath(), []byte("{\"kind\":\"run.start\"}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}

	if err := sess.Save(model.NewConversation("system", "hello")); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	data, err := os.ReadFile(sess.LogPath())
	if err != nil {
		t.Fatalf("ReadFile() = %v", err)
	}
	if string(data) != "{\"kind\":\"run.start\"}\n" {
		t.Errorf("the log now reads %q", data)
	}
}
