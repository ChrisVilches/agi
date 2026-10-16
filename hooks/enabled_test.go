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
// hands over is the whole of what decides it: a hook the run cannot reach is
// not in it.
func runsIn(t *testing.T, declared string, event Event) (tty, nonTTY bool) {
	t.Helper()
	s := one(t, declared)
	if s.Len() != 1 {
		t.Fatalf("the manifest declares %d hooks, want 1", s.Len())
	}
	return len(s.WithTTY(true).forEvent(event)) == 1,
		len(s.WithTTY(false).forEvent(event)) == 1
}

// enabled is a yes or a no, and a tool hook it says yes to runs wherever the
// run is.
func TestEnabledSaysWhetherAHookRuns(t *testing.T) {
	cases := map[string]struct {
		enabled string
		runs    bool
	}{
		"absent": {``, true},
		"true":   {`"enabled": true,`, true},
		"false":  {`"enabled": false,`, false},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			for _, e := range []Event{PreToolUse, PostToolUse} {
				tty, nonTTY := runsIn(t, `{"`+string(e)+`": [{`+c.enabled+` "command": "x"}]}`, e)
				if tty != c.runs || nonTTY != c.runs {
					t.Errorf("%s: ran at a terminal = %v and away from one = %v, want %v for both",
						e, tty, nonTTY, c.runs)
				}
			}
		})
	}
}

// A display hook runs only where agi's output is a terminal, and enabled can
// only take that away.
func TestADisplayHookRunsOnlyAtATerminal(t *testing.T) {
	for _, thinking := range []string{``, `"thinking": true,`} {
		tty, nonTTY := runsIn(t, `{"MessageDisplay": [{`+thinking+` "command": "x"}]}`, MessageDisplay)
		if !tty || nonTTY {
			t.Errorf("%q: ran at a terminal = %v and away from one = %v, want only at one",
				thinking, tty, nonTTY)
		}
		tty, nonTTY = runsIn(t, `{"MessageDisplay": [{`+thinking+` "enabled": false, "command": "x"}]}`,
			MessageDisplay)
		if tty || nonTTY {
			t.Errorf("%q: a hook that is not enabled ran (tty %v, non-tty %v)", thinking, tty, nonTTY)
		}
	}
}

// A set nobody told reads as a run that is not at a terminal, which is the
// safe way round: a display hook is the one that paints escape sequences.
func TestADisplayHookDefaultsToAwayFromATerminal(t *testing.T) {
	s := one(t, `{"MessageDisplay": [{"command": "x"}]}`)
	if got := len(s.forEvent(MessageDisplay)); got != 0 {
		t.Errorf("%d hooks reached, want a display hook left alone by a set nobody told", got)
	}
}

// A value that is not true or false is refused, the list that once named the
// kinds of run among them, and the message says what can be written.
func TestEnabledRefusesAValueItCannotBe(t *testing.T) {
	for _, written := range []string{`["tty"]`, `[]`, `"tty"`, `null`, `1`, `{"tty": true}`, `"yes"`} {
		_, err := Load([]string{folder(t,
			`{"PreToolUse": [{"enabled": `+written+`, "command": "x"}]}`)}, Config{})
		if err == nil {
			t.Fatalf("Load() = nil, want %s refused as an enabled", written)
		}
		for _, want := range []string{written, "true", "false"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Load() = %v, want the message to say %q", err, want)
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

// Away from a terminal the renderer is never started, so it cannot paint a
// piped answer.
func TestAPipedRunKeepsTheRendererFromRunning(t *testing.T) {
	declared := `{"MessageDisplay": [{"command": "./a.sh", "args": []}]}`
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

// A guard is not a display hook: it refuses the call in a piped run as it
// does at a terminal, and only enabled can switch it off.
func TestAGuardRunsInEveryKindOfRun(t *testing.T) {
	deny := prints(`{"hookSpecificOutput": {"permissionDecision": "deny", "permissionDecisionReason": "no"}}`)
	c := Call{Tool: "bash", Arguments: json.RawMessage(`{"command": "git push --force"}`)}

	s := chain(t, `{"PreToolUse": [{"command": "./a.sh", "args": []}]}`, map[string]string{"a.sh": deny})
	for _, tty := range []bool{true, false} {
		if _, _, err := s.WithTTY(tty).Before(context.Background(), c); err == nil {
			t.Errorf("Before() = nil (tty = %v), want the guard to refuse the call", tty)
		}
	}

	off := chain(t, `{"PreToolUse": [{"enabled": false, "command": "./a.sh", "args": []}]}`,
		map[string]string{"a.sh": deny})
	if _, _, err := off.WithTTY(true).Before(context.Background(), c); err != nil {
		t.Errorf("Before() = %v, want the call through where the guard is not enabled", err)
	}
}

// Skips is what the verbose listing marks a hook with, so it has to answer
// for the run it is asked of rather than for the hook alone.
func TestSkipsAnswersForThisRun(t *testing.T) {
	s := one(t, `{
		"MessageDisplay": [{"command": "x"}],
		"PreToolUse": [{"enabled": false, "command": "y"}, {"command": "z"}]
	}`)
	var display, off, plain *Hook
	for _, h := range s.All() {
		switch {
		case h.Event() == MessageDisplay:
			display = h
		case !h.enabled:
			off = h
		default:
			plain = h
		}
	}

	if s.WithTTY(true).Skips(display) {
		t.Error("Skips() said a display hook is off in a run at a terminal")
	}
	if !s.WithTTY(false).Skips(display) {
		t.Error("Skips() said a display hook is on in a run that is not at a terminal")
	}
	for _, in := range []bool{true, false} {
		if !s.WithTTY(in).Skips(off) {
			t.Errorf("Skips() said a hook that is not enabled is on (tty = %v)", in)
		}
		if s.WithTTY(in).Skips(plain) {
			t.Errorf("Skips() said a guard that is enabled is off (tty = %v)", in)
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
