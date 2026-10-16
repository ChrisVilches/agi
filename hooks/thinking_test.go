package hooks

import (
	"context"
	"strings"
	"testing"
)

// The field decides which half of what agi displays a hook renders, and an
// absent one is the half the event always fired on.
func TestLoadReadsThinking(t *testing.T) {
	cases := map[string]struct {
		declared string
		want     bool
	}{
		"absent":  {`{"MessageDisplay": [{"command": "./a.sh"}]}`, false},
		"false":   {`{"MessageDisplay": [{"thinking": false, "command": "./a.sh"}]}`, false},
		"true":    {`{"MessageDisplay": [{"thinking": true, "command": "./a.sh"}]}`, true},
		"spacing": {`{"MessageDisplay": [{"thinking":   true, "command": "./a.sh"}]}`, true},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := one(t, c.declared)
			if s.Len() != 1 {
				t.Fatalf("Len() = %d, want the hook loaded", s.Len())
			}
			if got := s.All()[0].Thinking(); got != c.want {
				t.Errorf("Thinking() = %v, want %v", got, c.want)
			}
		})
	}
}

// A tool hook prints nothing, so there is no half of the output for it to
// choose between. Writing the field there is refused rather than ignored, for
// the reason a matcher on a display hook is: a field agi accepted and dropped
// is a belief about a hook that the hook does not hold.
func TestLoadRefusesThinkingOnAToolHook(t *testing.T) {
	for _, event := range events {
		if event == MessageDisplay {
			continue
		}
		t.Run(string(event), func(t *testing.T) {
			// false as well as true, because the mistake is writing the field
			// at all and a bool cannot tell a written false from an absent
			// one — which is why the entry reads it raw.
			for _, value := range []string{"true", "false"} {
				declared := `{"` + string(event) + `": [{"thinking": ` + value + `, "command": "./a.sh"}]}`
				_, err := Load([]string{folder(t, declared)}, Config{})
				if err == nil {
					t.Fatalf("Load() = nil, want thinking refused on a %s hook", event)
				}
				if !strings.Contains(err.Error(), "thinking") {
					t.Errorf("Load() = %v, want the message to name the field", err)
				}
				if !strings.Contains(err.Error(), string(MessageDisplay)) {
					t.Errorf("Load() = %v, want the message to name the event the field belongs to", err)
				}
			}
		})
	}
}

// The value is checked strictly, because a value agi accepted and read as
// false would aim the hook at the other half of the output without a word.
// null is the one that has to be refused by hand: unmarshalling it into a
// bool is not an error, it leaves the bool alone.
func TestLoadRefusesAThinkingThatIsNotABool(t *testing.T) {
	for _, value := range []string{`null`, `"true"`, `1`, `[]`, `{}`} {
		t.Run(value, func(t *testing.T) {
			declared := `{"MessageDisplay": [{"thinking": ` + value + `, "command": "./a.sh"}]}`
			_, err := Load([]string{folder(t, declared)}, Config{})
			if err == nil {
				t.Fatalf("Load() = nil, want a thinking of %s refused", value)
			}
			if !strings.Contains(err.Error(), "thinking") {
				t.Errorf("Load() = %v, want the message to name the field", err)
			}
		})
	}
}

// The two kinds of message are two streams and not one with a flag on it: a
// hook declares which it renders and is spawned for that one alone.
func TestOnDisplayShowsEachHookOnlyItsOwnKind(t *testing.T) {
	s := chain(t, `{"MessageDisplay": [
	  {"thinking": true, "command": "./think.sh", "args": []},
	  {"command": "./say.sh", "args": []}
	]}`, map[string]string{
		"think.sh": shows("DELIBERATION"),
		"say.sh":   shows("PROSE"),
	})

	for _, tt := range []struct {
		name     string
		thinking bool
		want     string
	}{
		{"the prose the model wrote for the reader", false, "PROSE"},
		{"the deliberation behind it", true, "DELIBERATION"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, report := s.OnDisplay(context.Background(),
				Message{Content: "original", Agent: "main", Thinking: tt.thinking})
			if got != tt.want {
				t.Errorf("OnDisplay() = %q, want %q", got, tt.want)
			}
			if len(report.Ran) != 1 {
				t.Errorf("report = %+v, want only the hook written for this kind to have run", report.Ran)
			}
		})
	}
}

// A run with hooks for one kind only leaves the other exactly as it was, and
// spawns nothing over it.
func TestOnDisplayLeavesTheOtherKindUntouched(t *testing.T) {
	s := chain(t, `{"MessageDisplay": [{"thinking": true, "command": "./a.sh", "args": []}]}`,
		map[string]string{"a.sh": shows("RENDERED")})

	got, report := s.OnDisplay(context.Background(), Message{Content: "the answer", Agent: "main"})
	if got != "the answer" {
		t.Errorf("OnDisplay() = %q, want the text untouched", got)
	}
	if len(report.Ran) != 0 {
		t.Errorf("report = %+v, want no hook to have run", report.Ran)
	}
}

// Several hooks of one kind still chain, each shown what the one before it
// left, and a hook of the other kind is not in the chain at all.
func TestOnDisplayChainsWithinOneKind(t *testing.T) {
	s := chain(t, `{"MessageDisplay": [
	  {"thinking": true, "command": "./first.sh", "args": []},
	  {"command": "./other.sh", "args": []},
	  {"thinking": true, "command": "./second.sh", "args": []}
	]}`, map[string]string{
		"first.sh": shows("once"),
		"other.sh": shows("WRONG"),
		// The second answers with what it was shown plus a word of its own,
		// so the test is about what reached it rather than what it decided.
		"second.sh": "#!/bin/sh\n" +
			`sed -n 's/.*"delta":"\([^"]*\)".*/{"hookSpecificOutput": {"displayContent": "\1 twice"}}/p'` + "\n",
	})

	got, report := s.OnDisplay(context.Background(),
		Message{Content: "original", Agent: "main", Thinking: true})
	if got != "once twice" {
		t.Errorf("OnDisplay() = %q, want the second hook to have been shown what the first left", got)
	}
	if len(report.Ran) != 2 {
		t.Errorf("report = %+v, want the two thinking hooks and neither of the others", report.Ran)
	}
}

// Displays is what the caller asks before laying out a message itself, so it
// has to answer for the kind asked about and not for the event.
func TestDisplaysAnswersPerKind(t *testing.T) {
	cases := map[string]struct {
		declared            string
		thinking, forReader bool
	}{
		"no hooks at all": {
			`{}`, false, false,
		},
		"a hook for the reader": {
			`{"MessageDisplay": [{"command": "./a.sh"}]}`, false, true,
		},
		"a hook for the deliberation": {
			`{"MessageDisplay": [{"thinking": true, "command": "./a.sh"}]}`, true, false,
		},
		"one of each": {
			`{"MessageDisplay": [
			  {"thinking": true, "command": "./a.sh"},
			  {"command": "./b.sh"}
			]}`, true, true,
		},
		"a tool hook is neither": {
			`{"PreToolUse": [{"command": "./a.sh"}]}`, false, false,
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := one(t, c.declared)
			if got := s.Displays(true); got != c.thinking {
				t.Errorf("Displays(true) = %v, want %v", got, c.thinking)
			}
			if got := s.Displays(false); got != c.forReader {
				t.Errorf("Displays(false) = %v, want %v", got, c.forReader)
			}
		})
	}
}

// A hook this run cannot reach has claimed nothing. The caller keeps its own
// formatting for a message no hook of this run will be shown, and a renderer
// written for a terminal has not claimed the deliberation of a piped run.
func TestDisplaysIgnoresAHookThisRunSkips(t *testing.T) {
	s := one(t, `{"MessageDisplay": [{"thinking": true, "enabled": ["tty"], "command": "./a.sh"}]}`)

	if !s.WithTTY(true).Displays(true) {
		t.Error("Displays(true) = false at a terminal, want the hook counted where it runs")
	}
	if s.WithTTY(false).Displays(true) {
		t.Error("Displays(true) = true off a terminal, want a hook this run skips to count for nothing")
	}
}

// A nil set is a run with no hooks, which every method here reads as such.
func TestDisplaysOnNoSet(t *testing.T) {
	var s *Set
	if s.Displays(true) || s.Displays(false) {
		t.Error("Displays() = true on a nil set, want a run with no hooks to display none")
	}
}
