package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// runsIn reports whether the one hook a manifest declares is reached in a run
// whose output is a terminal, and in one whose output is not.
//
// It asks forEvent rather than a field, because the list of hooks an event
// hands over is the whole of what the flag decides: a hook the run cannot
// reach is not in it.
func runsIn(t *testing.T, declared string) (tty, nonTTY bool) {
	t.Helper()
	s := one(t, declared)
	if s.Len() != 1 {
		t.Fatalf("the manifest declares %d hooks, want 1", s.Len())
	}
	return len(s.WithTTY(true).forEvent(PreToolUse)) == 1,
		len(s.WithTTY(false).forEvent(PreToolUse)) == 1
}

// The four situations two booleans say, and the three shapes the file writes
// them in.
func TestEnabledSaysWhichRunsAHookIsFor(t *testing.T) {
	cases := map[string]struct {
		enabled     string
		tty, nonTTY bool
	}{
		"absent":       {``, true, true},
		"true":         {`"enabled": true,`, true, true},
		"false":        {`"enabled": false,`, false, false},
		"tty":          {`"enabled": ["tty"],`, true, false},
		"non-tty":      {`"enabled": ["non-tty"],`, false, true},
		"both named":   {`"enabled": ["tty", "non-tty"],`, true, true},
		"neither":      {`"enabled": [],`, false, false},
		"both, listed": {`"enabled": ["non-tty", "tty"],`, true, true},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tty, nonTTY := runsIn(t, `{"PreToolUse": [{`+c.enabled+` "command": "x"}]}`)
			if tty != c.tty {
				t.Errorf("at a terminal the hook ran = %v, want %v", tty, c.tty)
			}
			if nonTTY != c.nonTTY {
				t.Errorf("away from a terminal the hook ran = %v, want %v", nonTTY, c.nonTTY)
			}
		})
	}
}

// The field is every event's, not MessageDisplay's, which is where the need
// for it was first felt. A guard that asks a question or posts to a chat room
// has the same two kinds of run to tell apart as a renderer does.
func TestEnabledIsOnEveryEvent(t *testing.T) {
	for _, e := range events {
		s := one(t, `{"`+string(e)+`": [{"enabled": ["tty"], "command": "x"}]}`)
		if got := len(s.WithTTY(true).forEvent(e)); got != 1 {
			t.Errorf("%s: %d hooks at a terminal, want the hook reached", e, got)
		}
		if got := len(s.WithTTY(false).forEvent(e)); got != 0 {
			t.Errorf("%s: %d hooks away from a terminal, want none reached", e, got)
		}
	}
}

// A set nobody told reads as a run that is not at a terminal, which is the
// safe way round: the hook written for a terminal is the one that paints
// escape sequences.
func TestEnabledDefaultsToAwayFromATerminal(t *testing.T) {
	s := one(t, `{"PreToolUse": [{"enabled": ["tty"], "command": "x"}]}`)
	if got := len(s.forEvent(PreToolUse)); got != 0 {
		t.Errorf("%d hooks reached, want a tty-only hook left alone by a set nobody told", got)
	}
}

// A value that is not one of the three shapes names all three, since the
// correction is to write one of them.
func TestEnabledRefusesAValueItCannotBe(t *testing.T) {
	for _, written := range []string{`"tty"`, `null`, `1`, `{"tty": true}`, `"yes"`} {
		_, err := Load([]string{folder(t,
			`{"PreToolUse": [{"enabled": `+written+`, "command": "x"}]}`)}, Config{})
		if err == nil {
			t.Fatalf("Load() = nil, want %s refused as an enabled", written)
		}
		for _, want := range []string{"true", "false", "tty", "non-tty"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Load() = %v, want the message to say %q can be written", err, want)
			}
		}
	}
}

// The list is strict: it holds the two names agi knows and nothing else. A
// spelling it accepted and ignored would be a hook that runs in neither kind
// of run, with nothing anywhere saying so.
func TestEnabledRefusesANameItDoesNotKnow(t *testing.T) {
	for _, written := range []string{`"ttyy"`, `"TTY"`, `"pipe"`, `""`, `3`, `null`, `["tty"]`} {
		_, err := Load([]string{folder(t,
			`{"PreToolUse": [{"enabled": [`+written+`], "command": "x"}]}`)}, Config{})
		if err == nil {
			t.Fatalf("Load() = nil, want %s refused inside an enabled", written)
		}
		if !strings.Contains(err.Error(), written) {
			t.Errorf("Load() = %v, want %s quoted back as the file wrote it", err, written)
		}
		for _, want := range []string{atTTY, notTTY} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Load() = %v, want %q named as a kind of run agi knows", err, want)
			}
		}
	}
}

// A refusal says which entry of which event is wrong, because a manifest
// holding a dozen hooks leaves the user to find it otherwise.
func TestEnabledRefusalNamesTheEntry(t *testing.T) {
	_, err := Load([]string{folder(t, `{"PostToolUse": [
		{"command": "a"},
		{"enabled": "sometimes", "command": "b"}
	]}`)}, Config{})
	if err == nil {
		t.Fatal("Load() = nil, want the second entry refused")
	}
	for _, want := range []string{string(PostToolUse), "hook 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load() = %v, want the message to say %q", err, want)
		}
	}
}

// The flag decides whether the program is run at all, which is the whole
// point of it: a renderer that would paint a piped answer is never started.
func TestEnabledKeepsTheProgramFromRunning(t *testing.T) {
	declared := `{"MessageDisplay": [{"enabled": ["tty"], "command": "./a.sh", "args": []}]}`
	s := chain(t, declared, map[string]string{"a.sh": shows("# rendered")})

	got, report := s.WithTTY(false).OnDisplay(context.Background(),
		Message{Content: "## findings", Agent: "main"})
	if got != "## findings" {
		t.Errorf("OnDisplay() = %q, want the message as it stood", got)
	}
	if len(report.Ran) != 0 {
		t.Errorf("report = %+v, want nothing recorded for a hook that was not run", report.Ran)
	}

	if got, _ := s.WithTTY(true).OnDisplay(context.Background(),
		Message{Content: "## findings", Agent: "main"}); got != "# rendered" {
		t.Errorf("OnDisplay() = %q, want the hook's text at a terminal", got)
	}
}

// The same, around a tool call, where what the flag decides is whether a
// guard refuses it.
func TestEnabledKeepsAGuardFromRefusing(t *testing.T) {
	declared := `{"PreToolUse": [{"enabled": ["non-tty"], "command": "./a.sh", "args": []}]}`
	s := chain(t, declared, map[string]string{"a.sh": prints(
		`{"hookSpecificOutput": {"permissionDecision": "deny", "permissionDecisionReason": "no"}}`)})
	c := Call{Tool: "bash", Arguments: json.RawMessage(`{"command": "git push --force"}`)}

	if _, _, err := s.WithTTY(true).Before(context.Background(), c); err != nil {
		t.Errorf("Before() = %v, want the call through where the guard does not run", err)
	}
	if _, _, err := s.WithTTY(false).Before(context.Background(), c); err == nil {
		t.Error("Before() = nil, want the guard to refuse the call in the run it is for")
	}
}

// Skips is what the verbose listing marks a hook with, so it has to answer
// for the run it is asked of rather than for the hook alone.
func TestSkipsAnswersForThisRun(t *testing.T) {
	s := one(t, `{"PreToolUse": [{"enabled": ["tty"], "command": "x"}, {"command": "y"}]}`)
	gated, plain := s.All()[0], s.All()[1]

	if s.WithTTY(true).Skips(gated) {
		t.Error("Skips() said a tty hook is off in a run at a terminal")
	}
	if !s.WithTTY(false).Skips(gated) {
		t.Error("Skips() said a tty hook is on in a run that is not at a terminal")
	}
	for _, in := range []bool{true, false} {
		if s.WithTTY(in).Skips(plain) {
			t.Errorf("Skips() said a hook that named no kind of run is off (tty = %v)", in)
		}
	}
}

// A set a run has no hooks for is asked this question too, through the
// listing, and answers rather than panicking.
func TestSkipsOnNoSet(t *testing.T) {
	var s *Set
	if s.Skips(nil) {
		t.Error("Skips() on a run with no hooks = true, want false")
	}
	if s.WithTTY(true) != nil {
		t.Error("WithTTY() on a run with no hooks returned a set")
	}
}
