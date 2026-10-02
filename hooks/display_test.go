package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shows is a MessageDisplay hook that replaces the text it is shown.
func shows(with string) string {
	return prints(`{"hookSpecificOutput": {"hookEventName": "MessageDisplay", "displayContent": ` +
		quoted(with) + `}}`)
}

func quoted(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// The whole of what the event is for: a hook is shown the text and hands back
// different text to print.
func TestOnDisplayReplacesTheText(t *testing.T) {
	s := chain(t, `{"MessageDisplay": [{"command": "./a.sh", "args": []}]}`,
		map[string]string{"a.sh": shows("# rendered")})

	got, report := s.OnDisplay(context.Background(), Message{Content: "## findings", Agent: "main"})
	if got != "# rendered" {
		t.Errorf("OnDisplay() = %q, want the hook's text", got)
	}
	if len(report.Ran) != 1 || report.Ran[0].Decided != Rewrote {
		t.Errorf("report = %+v, want one hook recorded as having rewritten the message", report.Ran)
	}
	if report.Ran[0].Event != MessageDisplay {
		t.Errorf("event = %q, want %q", report.Ran[0].Event, MessageDisplay)
	}
}

// The hook reads the text under Claude Code's own field name, which is the
// one thing about this event that a ported script cannot survive being
// wrong.
func TestOnDisplaySendsTheTextAsContent(t *testing.T) {
	// The hook keeps what it was sent, in the folder it runs in, so the test
	// can read the payload rather than infer it from a reply.
	dir := folder(t, `{"MessageDisplay": [{"command": "./a.sh", "args": []}]}`)
	write(t, dir, "a.sh", "#!/bin/sh\ncat > payload.json\n")
	s, err := Load([]string{dir}, Config{})
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	if _, report := s.OnDisplay(context.Background(),
		Message{Content: "hello", Agent: "fork-1"}); len(report.Ran) != 1 {
		t.Fatalf("report = %+v, want one hook to have run", report.Ran)
	}

	sent, err := os.ReadFile(filepath.Join(dir, "payload.json"))
	if err != nil {
		t.Fatalf("the hook kept no payload: %v", err)
	}
	var in map[string]any
	if err := json.Unmarshal(sent, &in); err != nil {
		t.Fatalf("the payload is not JSON: %v\n%s", err, sent)
	}
	if in["delta"] != "hello" {
		t.Errorf("delta = %v, want the message", in["delta"])
	}
	// agi fires this once per finished message, which is the shape Claude
	// Code takes when nobody is watching it stream: one call, index zero,
	// final true, the whole message in delta.
	if in["index"] != float64(0) {
		t.Errorf("index = %v, want the only batch of the message", in["index"])
	}
	if in["final"] != true {
		t.Errorf("final = %v, want the message's last batch", in["final"])
	}
	if in["hook_event_name"] != string(MessageDisplay) {
		t.Errorf("hook_event_name = %v, want %q", in["hook_event_name"], MessageDisplay)
	}
	if in["agent_id"] != "fork-1" {
		t.Errorf("agent_id = %v, want the agent whose message it is", in["agent_id"])
	}
	// A message is not a call, and a payload claiming otherwise would have a
	// script branching on a tool that does not exist.
	for _, absent := range []string{"tool_name", "tool_input", "tool_response", "tool_use_id"} {
		if _, there := in[absent]; there {
			t.Errorf("%s is in the payload, want a message described as a message", absent)
		}
	}
}

// Hooks chain here the way they do at the other two events: each is shown
// what the one before it left.
func TestOnDisplayChainsOneHookIntoTheNext(t *testing.T) {
	s := chain(t, `{"MessageDisplay": [
	  {"command": "./a.sh", "args": []},
	  {"command": "./b.sh", "args": []}
	]}`, map[string]string{
		"a.sh": shows("second"),
		"b.sh": "#!/bin/sh\nseen=$(cat)\n" +
			`case "$seen" in *'"delta":"second"'*) ;; *) echo '{}' ; exit 0 ;; esac` + "\n" +
			`printf '{"hookSpecificOutput": {"displayContent": "third"}}'` + "\n",
	})

	got, _ := s.OnDisplay(context.Background(), Message{Content: "first"})
	if got != "third" {
		t.Errorf("OnDisplay() = %q, want the second hook to have seen the first one's text", got)
	}
}

// A hook that emptied the message meant it. That is why the field is read
// through a pointer.
func TestOnDisplayHonorsAnEmptyReplacement(t *testing.T) {
	s := chain(t, `{"MessageDisplay": [{"command": "./a.sh", "args": []}]}`,
		map[string]string{"a.sh": shows("")})

	got, _ := s.OnDisplay(context.Background(), Message{Content: "loud"})
	if got != "" {
		t.Errorf("OnDisplay() = %q, want the hook's empty replacement", got)
	}
}

// A hook that said nothing about the text leaves it alone, which is what an
// observing hook — one that only logs — does.
func TestOnDisplayLeavesTheTextAloneWhenNothingIsSaid(t *testing.T) {
	s := chain(t, `{"MessageDisplay": [{"command": "./a.sh", "args": []}]}`,
		map[string]string{"a.sh": "#!/bin/sh\ncat >/dev/null\n"})

	got, report := s.OnDisplay(context.Background(), Message{Content: "as written"})
	if got != "as written" {
		t.Errorf("OnDisplay() = %q, want the message untouched", got)
	}
	if len(report.Ran) != 1 || report.Ran[0].Decided != Proceeded {
		t.Errorf("report = %+v, want one hook recorded as having proceeded", report.Ran)
	}
}

// A renderer that exited badly is a renderer that did not render. The message
// is printed as the model wrote it and the user is told why.
func TestOnDisplayKeepsTheTextWhenTheHookFails(t *testing.T) {
	s := chain(t, `{"MessageDisplay": [{"command": "./a.sh", "args": []}]}`,
		map[string]string{"a.sh": "#!/bin/sh\necho 'glow: not found' >&2\nexit 127\n"})

	got, report := s.OnDisplay(context.Background(), Message{Content: "as written"})
	if got != "as written" {
		t.Errorf("OnDisplay() = %q, want the message untouched", got)
	}
	if len(report.Ran) != 1 || report.Ran[0].Decided != Failed {
		t.Errorf("report = %+v, want one hook recorded as having failed", report.Ran)
	}
	if len(report.Notes) != 1 || !strings.Contains(report.Notes[0], "127") {
		t.Errorf("notes = %q, want the user told the status", report.Notes)
	}
}

// Exit 2 refuses a call at the other two events. There is no call here, so it
// is a hook that went wrong and nothing more: agi will not let a display hook
// silence the agent.
func TestOnDisplayCannotRefuseAMessage(t *testing.T) {
	s := chain(t, `{"MessageDisplay": [{"command": "./a.sh", "args": []}]}`,
		map[string]string{"a.sh": "#!/bin/sh\necho 'no' >&2\nexit 2\n"})

	got, report := s.OnDisplay(context.Background(), Message{Content: "shown anyway"})
	if got != "shown anyway" {
		t.Errorf("OnDisplay() = %q, want the message printed", got)
	}
	if len(report.Ran) != 1 || report.Ran[0].Decided != Failed {
		t.Errorf("report = %+v, want a failure rather than a refusal", report.Ran)
	}
}

// A program that cannot be started stops the agent at a tool event, because a
// guard that is broken must never fail open. There is nothing to fail open
// into here, and a run ended because a markdown renderer is not installed
// would be agi destroying the work to protect nothing.
func TestOnDisplayDoesNotStopTheAgent(t *testing.T) {
	cases := map[string]string{
		"a program that is not there": `{"MessageDisplay": [
		  {"command": "./missing.sh", "args": []}]}`,
		"a hook asking to stop": `{"MessageDisplay": [
		  {"command": "./a.sh", "args": []}]}`,
	}
	scripts := map[string]string{
		"a hook asking to stop": prints(`{"continue": false, "stopReason": "enough"}`),
	}

	for name, manifest := range cases {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{}
			if body, has := scripts[name]; has {
				files["a.sh"] = body
			}
			s := chain(t, manifest, files)

			got, report := s.OnDisplay(context.Background(), Message{Content: "shown anyway"})
			if got != "shown anyway" {
				t.Errorf("OnDisplay() = %q, want the message printed", got)
			}
			if len(report.Notes) == 0 {
				t.Errorf("notes = %q, want the user told the hook did not do what it asked",
					report.Notes)
			}
		})
	}
}

// A hook that outran the timeout is the same case: the message goes out.
func TestOnDisplaySurvivesATimeout(t *testing.T) {
	dir := folder(t, `{"MessageDisplay": [{"command": "./a.sh", "args": []}]}`)
	write(t, dir, "a.sh", "#!/bin/sh\nsleep 5\n")
	s, err := Load([]string{dir}, Config{Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	got, report := s.OnDisplay(context.Background(), Message{Content: "shown anyway"})
	if got != "shown anyway" {
		t.Errorf("OnDisplay() = %q, want the message printed", got)
	}
	if len(report.Notes) == 0 {
		t.Errorf("notes = %q, want the user told the hook timed out", report.Notes)
	}
}

// A display hook cannot put anything into the conversation, and is told so
// rather than quietly heeded or quietly ignored.
func TestOnDisplayCannotAddContext(t *testing.T) {
	s := chain(t, `{"MessageDisplay": [{"command": "./a.sh", "args": []}]}`,
		map[string]string{"a.sh": prints(
			`{"hookSpecificOutput": {"additionalContext": "read this"}}`)})

	_, report := s.OnDisplay(context.Background(), Message{Content: "text"})
	if report.Context != "" {
		t.Errorf("context = %q, want nothing to reach the conversation", report.Context)
	}
	if len(report.Notes) != 1 || !strings.Contains(report.Notes[0], "ignored") {
		t.Errorf("notes = %q, want the user told the request was ignored", report.Notes)
	}
}

// The hooks of the other events are not run for a message, and this one's are
// not run for a call.
func TestEventsDoNotRunAtEachOthersMoments(t *testing.T) {
	s := chain(t, `{
	  "PreToolUse": [{"command": "./pre.sh", "args": []}],
	  "MessageDisplay": [{"command": "./show.sh", "args": []}]
	}`, map[string]string{
		"pre.sh":  prints(`{"hookSpecificOutput": {"updatedInput": {"command": "changed"}}}`),
		"show.sh": shows("rendered"),
	})

	arguments, report, err := s.Before(context.Background(),
		Call{Tool: "bash", Arguments: json.RawMessage(`{"command": "original"}`)})
	if err != nil {
		t.Fatalf("Before() = %v", err)
	}
	if !strings.Contains(string(arguments), "changed") {
		t.Errorf("arguments = %s, want the PreToolUse hook to have run", arguments)
	}
	if len(report.Ran) != 1 {
		t.Errorf("report = %+v, want only the PreToolUse hook to have run", report.Ran)
	}

	shown, report := s.OnDisplay(context.Background(), Message{Content: "text"})
	if shown != "rendered" {
		t.Errorf("OnDisplay() = %q, want the MessageDisplay hook to have run", shown)
	}
	if len(report.Ran) != 1 {
		t.Errorf("report = %+v, want only the MessageDisplay hook to have run", report.Ran)
	}
}

// A matcher and a condition are both defined over a tool, and this event has
// none. Ignoring them, which is what Claude Code does with a matcher, would
// produce exactly the dead-hook silence refusing an unfirable matcher exists
// to stop.
func TestLoadRefusesMatchingOnAMessage(t *testing.T) {
	cases := map[string]struct{ declared, says string }{
		"a matcher": {
			declared: `{"MessageDisplay": [{"matcher": "^bash$", "command": "./a.sh"}]}`,
			says:     "matcher",
		},
		"a condition": {
			declared: `{"MessageDisplay": [{"if": {"command": "^rm"}, "command": "./a.sh"}]}`,
			says:     "conditions",
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load([]string{folder(t, c.declared)}, Config{})
			if err == nil {
				t.Fatalf("Load() = nil, want %s on a %s hook refused", c.says, MessageDisplay)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("Load() = %v, want the message to name the %s", err, c.says)
			}
		})
	}
}

// A wildcard matcher is still a matcher, and writing one on this event is
// still the mistake above: a person who wrote it believes it does something.
func TestLoadAcceptsAMessageHookWithNoMatcher(t *testing.T) {
	s := one(t, `{"MessageDisplay": [{"command": "./a.sh"}]}`)
	if s.Len() != 1 {
		t.Fatalf("Len() = %d, want the hook loaded", s.Len())
	}
	if got := s.All()[0].Event(); got != MessageDisplay {
		t.Errorf("event = %q, want %q", got, MessageDisplay)
	}
}

// The event names are the format, and so is the list a mistyped one is
// refused with.
func TestLoadNamesEveryEvent(t *testing.T) {
	_, err := Load([]string{folder(t, `{"Stop": [{"command": "./a.sh"}]}`)}, Config{})
	if err == nil {
		t.Fatal("Load() = nil, want an event agi does not have refused")
	}
	for _, e := range events {
		if !strings.Contains(err.Error(), string(e)) {
			t.Errorf("Load() = %v, want %q listed", err, e)
		}
	}
}

// AboutTool is what everything written for a tool call asks before assuming
// there is one, so it has to be right for every event there is.
func TestAboutToolCoversEveryEvent(t *testing.T) {
	want := map[Event]bool{PreToolUse: true, PostToolUse: true, MessageDisplay: false}
	for _, e := range events {
		expected, known := want[e]
		if !known {
			t.Fatalf("the event %q is new and this test does not say whether it is about a "+
				"tool; say so here, and check everything that asks", e)
		}
		if e.AboutTool() != expected {
			t.Errorf("%q.AboutTool() = %v, want %v", e, e.AboutTool(), expected)
		}
	}
}
