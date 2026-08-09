package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/session"
)

// Running agi again against the same session is the whole of the chat: the
// second run sees the first exchange and answers the new question after it.
func TestRunSessionContinuesTheConversation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chat")

	first := &scriptedClient{replies: replies("4")}
	sess, err := session.Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), first, registryWith(t), sess, "what is 2+2?", nil); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	// A second invocation, as if the user had run agi again with --session.
	second := &scriptedClient{replies: replies("12")}
	reopened, err := session.Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	out.Reset()
	if err := runSession(context.Background(), pipedOutput(&out), second, registryWith(t), reopened, "and times 3?", nil); err != nil {
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
	dir := filepath.Join(t.TempDir(), "chat")
	sess, err := session.Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	client := &scriptedClient{replies: replies(
		"User 1 is Ada.",
		`{"id": 1, "name": "Ada"}`,
	)}

	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), client, registryWith(t), sess, "who is user 1?", testSchema(t)); err != nil {
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
	dir := filepath.Join(t.TempDir(), "chat")
	sess, err := session.Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), &scriptedClient{replies: replies("4")}, registryWith(t), sess, "what is 2+2?", nil); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	failing := &scriptedClient{err: errFailed}
	if err := runSession(context.Background(), pipedOutput(&out), failing, registryWith(t), sess, "and times 3?", nil); err == nil {
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

// --rm runs the prompt and leaves nothing behind.
func TestDiscardedRunWritesNothing(t *testing.T) {
	var out bytes.Buffer
	client := &scriptedClient{replies: replies("4")}
	if err := runSession(context.Background(), pipedOutput(&out), client, registryWith(t), session.Discarded(), "what is 2+2?", nil); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}
	if got := out.String(); got != "4\n" {
		t.Errorf("output = %q, want the answer", got)
	}
}

func TestSessionFlags(t *testing.T) {
	t.Run("a fresh directory by default", func(t *testing.T) {
		s, err := sessionFlags{}.open()
		if err != nil {
			t.Fatalf("open() = %v", err)
		}
		defer os.RemoveAll(s.Dir())
		if !s.Stored() {
			t.Error("Stored() = false, want a run kept by default")
		}
		if !strings.HasPrefix(filepath.Base(s.Dir()), "agi-") {
			t.Errorf("Dir() = %q, want a directory agi named for itself", s.Dir())
		}
	})

	t.Run("--rm keeps nothing", func(t *testing.T) {
		s, err := sessionFlags{discard: true}.open()
		if err != nil {
			t.Fatalf("open() = %v", err)
		}
		if s.Stored() {
			t.Errorf("Stored() = true with --rm, want nothing kept")
		}
	})

	t.Run("--session names the directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "named")
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
