package session

import (
	"bytes"
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
		Content:          "let me look",
		ReasoningContent: "they want the machine's status, so uname is the place to start",
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
	s, err := Open(sessionDir(t, "run"))
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
		if !ok || x.Content != y.Content || x.ReasoningContent != y.ReasoningContent ||
			len(x.ToolRequests) != len(y.ToolRequests) {
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
	dir := sessionDir(t, "chat")
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
	s, err := Open(sessionDir(t, "chat"))
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
	s, err := Open(sessionDir(t, "run"))
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
	s, err := Open(sessionDir(t, "run"))
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

	t.Run("opens a directory that is there", func(t *testing.T) {
		dir := sessionDir(t, "run")
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Open() = %v", err)
		}
		if s.Dir() != dir {
			t.Errorf("Dir() = %q, want %q", s.Dir(), dir)
		}
	})

	// Naming a session is asking for one conversation in particular. A name
	// that matches nothing is a mistake, and starting an empty session under
	// it would answer the user's question as though they had never asked the
	// ones before it.
	t.Run("refuses a directory that is not there", func(t *testing.T) {
		dir := filepath.Join(root, "nested", "run")
		_, err := Open(dir)
		if err == nil {
			t.Fatal("Open() = nil, want an error for a session that does not exist")
		}
		if !strings.Contains(err.Error(), dir) {
			t.Errorf("Open() = %v, want it to name the path", err)
		}
		if !strings.Contains(err.Error(), "mkdir") {
			t.Errorf("Open() = %v, want it to say how to make one", err)
		}
		if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
			t.Errorf("Stat(%s) = %v, want nothing created", dir, statErr)
		}
	})

	// A directory with nothing in it is a session with nothing said yet, which
	// is the ordinary state of one the user has just made.
	t.Run("opens an empty directory as an empty session", func(t *testing.T) {
		s, err := Open(sessionDir(t, "fresh"))
		if err != nil {
			t.Fatalf("Open() = %v", err)
		}
		conv, err := s.Load()
		if err != nil {
			t.Fatalf("Load() = %v", err)
		}
		if conv != nil {
			t.Errorf("Load() = %+v, want nothing stored yet", conv)
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
			s, err := Open(sessionDir(t, "run"))
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
	s, err := Open(sessionDir(t, "run"))
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

// The log belongs to the session's directory, beside the snapshot and not on
// top of it.
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

// sessionDir returns a directory for a session, made the way a user makes one:
// Open no longer creates what it cannot find, so the directory has to be there
// before it is named.
func sessionDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		t.Fatalf("MkdirAll() = %v", err)
	}
	return dir
}

// --rm: the run is stored like any other while it happens, and the whole
// directory goes when it ends. Storing it is the point — the snapshot, the
// log, and whatever a tool kept are all there while the run is using them.
func TestEphemeralSessionIsRemoved(t *testing.T) {
	s, err := Ephemeral()
	if err != nil {
		t.Fatalf("Ephemeral() = %v", err)
	}
	if s.Dir() == "" {
		t.Fatal("Dir() = \"\", want an ephemeral session to store the run while it lasts")
	}
	if err := s.Save(conversation()); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	if _, err := os.Stat(s.path()); err != nil {
		t.Fatalf("the snapshot was not written: %v", err)
	}

	dir := s.Dir()
	if err := s.Remove(); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("os.Stat(%q) = %v, want the directory to be gone", dir, err)
	}
}

// Removing a session that is already gone is what was asked for, not a
// failure: the point is that it is not there afterwards.
func TestRemoveIsIdempotent(t *testing.T) {
	s, err := Ephemeral()
	if err != nil {
		t.Fatalf("Ephemeral() = %v", err)
	}
	if err := s.Remove(); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if err := s.Remove(); err != nil {
		t.Errorf("Remove() a second time = %v, want removing nothing to succeed", err)
	}
}

// The directory the user named is never removed, whatever else happens. It is
// the case this check exists for: --rm and --session are refused together, but
// the rule that keeps a user's conversation safe belongs next to the line that
// would delete it, not in the command line that happens to call it today.
func TestRemoveLeavesSessionsItDidNotCreate(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if err := s.Remove(); err != nil {
		t.Fatalf("Remove() = %v, want a session it did not create to be left alone", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("os.Stat(%q) = %v, want the user's directory untouched", dir, err)
	}
}

// The second lock: a session marked ephemeral but sitting somewhere agi did
// not create is refused rather than deleted. Nothing produces that state
// today, which is the point — it is what catches the change that would.
func TestRemoveRefusesADirectoryAgiDidNotName(t *testing.T) {
	dir := t.TempDir()
	s := &Session{dir: dir, ephemeral: true}

	err := s.Remove()
	if err == nil {
		t.Fatal("Remove() = nil, want a refusal for a directory outside agi's own naming")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("Remove() = %v, want the refusal to name the directory", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("os.Stat(%q) = %v, want the directory untouched", dir, err)
	}
}

// Where copies of changed files go. The session names the place and creates
// nothing, exactly as it does for the log.
func TestBackupDir(t *testing.T) {
	dir := t.TempDir()
	sess, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	want := filepath.Join(dir, backupDirName)
	if got := sess.BackupDir(); got != want {
		t.Errorf("BackupDir() = %q, want %q", got, want)
	}
	if _, err := os.Stat(want); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("os.Stat(%q) = %v, want the session to name the directory and create nothing", want, err)
	}
}

// Reasoning is stored because a resumed run has to send it back: an endpoint
// that wants the reasoning of every historical assistant turn wants it on the
// turns that happened in an earlier run too, so a file that dropped it would
// be a file no run could be resumed from.
func TestReasoningSurvivesASnapshot(t *testing.T) {
	s, err := Open(sessionDir(t, "run"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	conv := model.NewConversation("sys", "how's the weather")
	conv.AddAssistant(model.AssistantMessage{
		Content:          "it is cloudy",
		ReasoningContent: "the tool answered, so I can tell them",
	})
	if err := s.Save(conv); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	// It is in the file under the name the domain gives it, so a person who
	// opens a session can read what the model was thinking.
	stored, err := os.ReadFile(filepath.Join(s.Dir(), snapshotFile))
	if err != nil {
		t.Fatalf("reading the snapshot: %v", err)
	}
	if !strings.Contains(string(stored), `"reasoning_content"`) {
		t.Errorf("snapshot = %s, want the reasoning stored", stored)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	last, ok := got.LastAssistant()
	if !ok {
		t.Fatal("Load() did not end with an assistant turn")
	}
	if last.ReasoningContent != "the tool answered, so I can tell them" {
		t.Errorf("reasoning = %q, want what was stored", last.ReasoningContent)
	}
}

// The shapes agi no longer writes still survive a trip through the file. They
// are never sent — written with no origin beside them, they name no endpoint
// that could read them back — but a session is not quietly emptied of them by
// the first run that resumes it, which is what a reader opening the file after
// such a run would otherwise find.
func TestTheReasoningReplaySurvivesASnapshot(t *testing.T) {
	const blocks = `[{"type":"thinking","thinking":"a long think","signature":"sig/abc+123=="}]`

	s, err := Open(sessionDir(t, "run"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	conv := model.NewConversation("sys", "how's the weather")
	conv.AddAssistant(model.AssistantMessage{
		Content: "it is cloudy",
		Replay:  model.NewStoredReplay(model.Origin{}, nil, nil, json.RawMessage(blocks)),
	})
	if err := s.Save(conv); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	stored, err := os.ReadFile(filepath.Join(s.Dir(), snapshotFile))
	if err != nil {
		t.Fatalf("reading the snapshot: %v", err)
	}
	if !strings.Contains(string(stored), `"signature": "sig/abc+123=="`) {
		t.Errorf("snapshot = %s, want the signed blocks stored", stored)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	last, ok := got.LastAssistant()
	if !ok {
		t.Fatal("Load() did not end with an assistant turn")
	}
	// The value and not the spelling: a snapshot is indented for a reader, so
	// encoding/json lays the blocks out over several lines on the way in. What
	// has to survive is what Anthropic signed, which is the thinking text.
	var compact bytes.Buffer
	if err := json.Compact(&compact, last.Replay.ThinkingBlocks); err != nil {
		t.Fatalf("compacting what was loaded: %v", err)
	}
	if compact.String() != blocks {
		t.Errorf("blocks = %s, want %s", compact.String(), blocks)
	}
}

// A turn is stored as the reply it arrived as, and with it the endpoint that
// produced it. Both halves are needed on the way back: the items are what a
// second turn hands over, and the origin is what says whether this run may
// hand them over at all.
func TestTheWholeReplyAndItsOriginSurviveASnapshot(t *testing.T) {
	const items = `[{"type":"reasoning","id":"rs_1","encrypted_content":"gAAAAAB+/x=="},` +
		`{"type":"message","id":"msg_1","role":"assistant",` +
		`"content":[{"type":"output_text","text":"it is cloudy"}]}]`
	origin := model.Origin{Backend: "openai", Model: "gpt-5"}

	s, err := Open(sessionDir(t, "run"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	conv := model.NewConversation("sys", "how's the weather")
	conv.AddAssistant(model.AssistantMessage{
		Content: "it is cloudy",
		Replay:  model.NewTurnReplay(origin, json.RawMessage(items)),
	})
	if err := s.Save(conv); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	last, ok := got.LastAssistant()
	if !ok {
		t.Fatal("Load() did not end with an assistant turn")
	}
	if last.Replay.Origin != origin {
		t.Errorf("origin = %+v, want %+v", last.Replay.Origin, origin)
	}
	// The value and not the spelling: a snapshot is indented for a reader.
	var compact bytes.Buffer
	if err := json.Compact(&compact, last.Replay.OutputFor(origin)); err != nil {
		t.Fatalf("compacting what was loaded: %v", err)
	}
	if compact.String() != items {
		t.Errorf("items = %s, want %s", compact.String(), items)
	}
	if last.Replay.OutputFor(model.Origin{Backend: "openai", Model: "gpt-5-mini"}) != nil {
		t.Error("a resumed run on another model was handed the first one's items")
	}
}

// A session file is something a person may open, and so something a person may
// edit. One that says a turn's replay is null means the same as one that never
// mentioned it, rather than four bytes to hand an endpoint that never asked.
func TestAStoredNullReplayReadsBackAsNone(t *testing.T) {
	s, err := Open(sessionDir(t, "run"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	const file = `{"messages":[{"kind":"user","content":"hi"},` +
		`{"kind":"assistant","content":"done","thinking_blocks":null,"reasoning_items":null,"output_items":null}]}`
	if err := os.WriteFile(filepath.Join(s.Dir(), snapshotFile), []byte(file), 0o600); err != nil {
		t.Fatalf("writing the snapshot: %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	last, _ := got.LastAssistant()
	if !last.Replay.Empty() {
		t.Errorf("replay = %+v, want nothing to hand back", last.Replay)
	}
}
