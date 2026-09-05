package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/prompt"
	"github.com/ChrisVilches/agi/session"
)

// Running agi again against the same session is the whole of the chat: the
// second run sees the first exchange and answers the new question after it.
func TestRunSessionContinuesTheConversation(t *testing.T) {
	dir := sessionDir(t, "chat")

	first := &scriptedClient{replies: replies("4")}
	sess, err := session.Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), nolog(), first, registryWith(t), sess, prompt.Base(), "what is 2+2?", nil, nil, defaultMaxTurns); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	// A second invocation, as if the user had run agi again with --session.
	second := &scriptedClient{replies: replies("12")}
	reopened, err := session.Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	out.Reset()
	if err := runSession(context.Background(), pipedOutput(&out), nolog(), second, registryWith(t), reopened, prompt.Base(), "and times 3?", nil, nil, defaultMaxTurns); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}
	if got := out.String(); got != "12\n" {
		t.Errorf("output = %q, want the second answer", got)
	}

	// The second request must carry the first exchange before the new prompt.
	sent := second.sent[0].Conversation.Messages
	if len(sent) != 3 {
		t.Fatalf("second run sent %d turns, want the exchange plus the new prompt: %+v", len(sent), sent)
	}
	if u, ok := sent[0].(model.UserMessage); !ok || u.Content != "what is 2+2?" {
		t.Errorf("turn 0 = %#v, want the first prompt", sent[0])
	}
	if a, ok := sent[1].(model.AssistantMessage); !ok || a.Content != "4" {
		t.Errorf("turn 1 = %#v, want the first answer", sent[1])
	}
	if u, ok := sent[2].(model.UserMessage); !ok || u.Content != "and times 3?" {
		t.Errorf("turn 2 = %#v, want the new prompt", sent[2])
	}

	// And the session now holds both exchanges.
	stored, err := reopened.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if stored.Len() != 4 {
		t.Errorf("stored %d turns, want both exchanges: %+v", stored.Len(), stored.Messages)
	}
}

// The scaffolding of a schema run is dropped before the run ends, so it is
// never stored: a resumed session ends on the prose answer.
func TestSchemaScaffoldingIsNotStored(t *testing.T) {
	dir := sessionDir(t, "chat")
	sess, err := session.Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	client := &scriptedClient{replies: replies(
		"User 1 is Ada.",
		`{"id": 1, "name": "Ada"}`,
	)}

	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), nolog(), client, registryWith(t), sess, prompt.Base(), "who is user 1?", testSchema(t), nil, defaultMaxTurns); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	stored, err := sess.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if stored.Len() != 2 {
		t.Fatalf("stored %d turns, want the prompt and the prose answer: %+v", stored.Len(), stored.Messages)
	}
	answer, done := stored.FinalAnswer()
	if !done || answer != "User 1 is Ada." {
		t.Errorf("stored answer = %q, want the prose the run reasoned to", answer)
	}
	for i, m := range stored.Messages {
		if m.Truncatable() {
			t.Errorf("turn %d is a %T, want no scaffolding stored", i, m)
		}
	}
}

// A run that failed leaves the session as it was, so the next one resumes from
// the last exchange that actually finished rather than from a dangling
// question.
func TestAFailedRunStoresNothing(t *testing.T) {
	dir := sessionDir(t, "chat")
	sess, err := session.Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), nolog(), &scriptedClient{replies: replies("4")}, registryWith(t), sess, prompt.Base(), "what is 2+2?", nil, nil, defaultMaxTurns); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	failing := &scriptedClient{err: errFailed}
	if err := runSession(context.Background(), pipedOutput(&out), nolog(), failing, registryWith(t), sess, prompt.Base(), "and times 3?", nil, nil, defaultMaxTurns); err == nil {
		t.Fatal("runSession() = nil, want the transport error")
	}

	stored, err := sess.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if stored.Len() != 2 {
		t.Errorf("stored %d turns, want only the exchange that finished: %+v", stored.Len(), stored.Messages)
	}
}

// sessionDir returns a directory for a session, made the way a user makes one:
// a named session has to exist before it is named.
func sessionDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll() = %v", err)
	}
	return dir
}

func TestSessionFlags(t *testing.T) {
	t.Run("a fresh directory by default", func(t *testing.T) {
		s, err := sessionFlags{}.open()
		if err != nil {
			t.Fatalf("open() = %v", err)
		}
		defer os.RemoveAll(s.Dir())
		if s.Dir() == "" {
			t.Error("Dir() = \"\", want a run kept by default")
		}
		if !strings.HasPrefix(filepath.Base(s.Dir()), "agi-") {
			t.Errorf("Dir() = %q, want a directory agi named for itself", s.Dir())
		}
	})

	// A shell argument carries whatever space the user left around it, and
	// none of it is part of the path they meant. Taken literally a leading
	// space makes an absolute path relative: "   /tmp/chat" is a directory
	// called "   " in the working directory, with tmp/chat inside it, which is
	// a perfectly good path that nothing further down would have questioned.
	t.Run("--session is trimmed", func(t *testing.T) {
		dir := sessionDir(t, "named")
		s, err := sessionFlags{dir: "  " + dir + "  "}.open()
		if err != nil {
			t.Fatalf("open() = %v", err)
		}
		if s.Dir() != dir {
			t.Errorf("Dir() = %q, want the path without the spaces around it", s.Dir())
		}
	})

	// A --session of nothing but spaces is refused by the flag itself while
	// the command line is read, which TestPathFlagsRefuseABlankValue covers
	// along with every other flag that names a path. What is left here is the
	// trimming, and the rules open() makes that no Validator could.

	// A named session that is not there is a mistake — a typo, a path from
	// another machine — and starting an empty one under the name would answer
	// the user as though the conversation they asked for never happened.
	t.Run("--session that does not exist is refused", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "never-made")
		if _, err := (sessionFlags{dir: dir}).open(); err == nil {
			t.Errorf("open() = nil, want %s refused", dir)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("Stat(%s) = %v, want nothing created", dir, err)
		}
	})

	// --rm is a session that exists for the length of the run and is deleted
	// afterwards, rather than no session at all. While the run lasts it is an
	// ordinary one, so its tools have somewhere to keep a copy of a file they
	// are about to change; what the flag means happens at the end.
	t.Run("--rm stores the run and then removes it", func(t *testing.T) {
		s, err := sessionFlags{discard: true}.open()
		if err != nil {
			t.Fatalf("open() = %v", err)
		}
		if s.Dir() == "" {
			t.Fatal("Dir() = \"\" with --rm, want the run kept while it happens")
		}

		dir := s.Dir()
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("Stat(%s) = %v, want a directory for the run to use", dir, err)
		}
		if err := s.Remove(); err != nil {
			t.Fatalf("Remove() = %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("Stat(%s) = %v, want the directory gone once the run has ended", dir, err)
		}
	})

	t.Run("--session names the directory", func(t *testing.T) {
		dir := sessionDir(t, "named")
		s, err := sessionFlags{dir: dir}.open()
		if err != nil {
			t.Fatalf("open() = %v", err)
		}
		if s.Dir() != dir {
			t.Errorf("Dir() = %q, want %q", s.Dir(), dir)
		}
	})

	t.Run("the two together are a contradiction", func(t *testing.T) {
		_, err := sessionFlags{dir: "somewhere", discard: true}.open()
		if err == nil {
			t.Fatal("open() = nil, want --rm and --session refused together")
		}
		if !strings.Contains(err.Error(), "--rm") || !strings.Contains(err.Error(), "--session") {
			t.Errorf("open() = %q, want it to name both flags", err)
		}
	})
}
