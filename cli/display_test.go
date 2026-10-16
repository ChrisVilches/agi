package cli

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/eventlog"
	"github.com/ChrisVilches/agi/hooks"
	"github.com/ChrisVilches/agi/model"
)

// renders is a MessageDisplay hook that stands in for a markdown renderer:
// it reads the message and prints a replacement built from it.
const renders = "#!/bin/sh\n" +
	`printf '{"hookSpecificOutput": {"hookEventName": "MessageDisplay", ` +
	`"displayContent": "RENDERED"}}'` + "\n"

// keeps is a MessageDisplay hook that records every message it was shown, one
// per line, in the file the test reads afterwards.
func keeps(where string) string {
	return "#!/bin/sh\n" +
		`seen=$(sed -n 's/.*"delta":"\([^"]*\)".*/\1/p')` + "\n" +
		`printf '%s\n' "$seen" >> ` + where + "\n"
}

// displayHooks arms a run at a terminal with MessageDisplay hooks alone. It is
// told it is at one because a display hook fires nowhere else; a test of a
// piped run says WithTTY(false) itself, as runRoot would.
func displayHooks(t *testing.T, scripts map[string]string) *hooks.Set {
	t.Helper()
	var entries []string
	for name := range scripts {
		entries = append(entries, `{"command": "./`+name+`", "args": []}`)
	}
	return armWith(t, `{"MessageDisplay": [`+strings.Join(entries, ",")+`]}`, scripts).WithTTY(true)
}

// The whole point of the event: what the user reads is the hook's text, and
// the conversation and the log keep the model's own words. Hooking at the
// print site is what makes that true by construction — the text has been
// stored by all three before a hook ever sees it.
func TestADisplayHookChangesWhatIsPrintedAndNothingElse(t *testing.T) {
	armed := displayHooks(t, map[string]string{"render.sh": renders})
	client := &scriptedClient{replies: []model.AssistantMessage{
		model.NewAssistantMessage("## findings"),
	}}
	conv := asked("check")

	_, lg, read := recorded(t)

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), lg, client,
		registryWith(t), conv, nil, false, defaultMaxTurns, armed); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}

	if got := strings.TrimSpace(out.String()); got != "RENDERED" {
		t.Errorf("output = %q, want the hook's text", got)
	}
	if !strings.Contains(renderAll(conv), "## findings") {
		t.Error("the conversation does not hold the model's own words")
	}

	events := read()
	var answered bool
	for _, e := range events {
		if e.Kind == eventlog.RunAnswer {
			answered = true
			if e.Content != "## findings" {
				t.Errorf("the log holds %q, want the model's own words", e.Content)
			}
		}
	}
	if !answered {
		t.Error("the run's answer was not recorded")
	}
}

// Prose the model writes alongside its tool calls is markdown as much as the
// answer is, and goes through the same hooks.
func TestADisplayHookSeesTheProseAlongsideAToolCall(t *testing.T) {
	kept := t.TempDir() + "/seen"
	armed := displayHooks(t, map[string]string{"keep.sh": keeps(kept)})

	tool := &fakeTool{name: "probe", output: "fine"}
	client := callsProbe(`{"a":1}`)
	client.replies[0].Content = "thinking out loud"

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), client,
		registryWith(t, tool), asked("check"), nil, false, defaultMaxTurns, armed); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}

	seen := readSeen(t, kept)
	for _, want := range []string{"thinking out loud", "done"} {
		if !contains(seen, want) {
			t.Errorf("the hook saw %q, want %q among them", seen, want)
		}
	}
}

// A piped run hooks nothing, not even the answer it does print. The answer is
// for another program, and a renderer wanted over it belongs in the pipe.
func TestAPipedRunRunsNoDisplayHook(t *testing.T) {
	kept := t.TempDir() + "/seen"
	armed := displayHooks(t, map[string]string{"keep.sh": keeps(kept)}).WithTTY(false)

	tool := &fakeTool{name: "probe", output: "fine"}
	client := callsProbe(`{"a":1}`)
	client.replies[0].Content = "thinking out loud"

	var out bytes.Buffer
	if err := runAgent(context.Background(), pipedOutput(&out), nolog(), client,
		registryWith(t, tool), asked("check"), nil, false, defaultMaxTurns, armed); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}

	if _, err := os.Stat(kept); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the hook was run (stat = %v), want no display hook in a piped run", err)
	}
	if got := strings.TrimSpace(out.String()); got != "done" {
		t.Errorf("output = %q, want the answer as the model wrote it", got)
	}
}

// A renderer that is not installed leaves the message as the model wrote it,
// and does not end the run: there is nothing here for a broken guard to fail
// open into.
func TestADisplayHookThatFailsLeavesTheMessageAlone(t *testing.T) {
	armed := displayHooks(t, map[string]string{
		"render.sh": "#!/bin/sh\necho 'glow: not found' >&2\nexit 127\n"})
	client := &scriptedClient{replies: []model.AssistantMessage{
		model.NewAssistantMessage("## findings"),
	}}

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), client,
		registryWith(t), asked("check"), nil, false, defaultMaxTurns, armed); err != nil {
		t.Fatalf("runAgent() = %v, want the run to carry on", err)
	}
	if !strings.Contains(out.String(), "## findings") {
		t.Errorf("output = %q, want the model's own words printed", out.String())
	}
	if !strings.Contains(out.String(), "127") {
		t.Errorf("output = %q, want the user told the hook failed", out.String())
	}
}

// A run given no hooks prints what it always printed.
func TestNoDisplayHookLeavesEveryMessageAlone(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		model.NewAssistantMessage("## findings"),
	}}

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), client,
		registryWith(t), asked("check"), nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "## findings" {
		t.Errorf("output = %q, want the answer as the model wrote it", got)
	}
}

// What a display hook did is recorded like any other hook: which one ran, at
// which moment, and what it came to. It claims nothing about a tool, because
// it guarded none.
func TestADisplayHookIsRecorded(t *testing.T) {
	armed := displayHooks(t, map[string]string{"render.sh": renders})
	client := &scriptedClient{replies: []model.AssistantMessage{
		model.NewAssistantMessage("## findings"),
	}}

	_, lg, read := recorded(t)

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), lg, client,
		registryWith(t), asked("check"), nil, false, defaultMaxTurns, armed); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}

	var found bool
	for _, e := range read() {
		if e.Moment != string(hooks.MessageDisplay) {
			continue
		}
		found = true
		if e.Kind != eventlog.HookResult {
			t.Errorf("kind = %q, want %q", e.Kind, eventlog.HookResult)
		}
		if e.Decided != string(hooks.Rewrote) {
			t.Errorf("decided = %q, want %q", e.Decided, hooks.Rewrote)
		}
		if e.Tool != "" || e.RequestID != "" {
			t.Errorf("the line names a tool %q and a request %q, want neither", e.Tool, e.RequestID)
		}
	}
	if !found {
		t.Error("nothing was recorded for the display hook")
	}
}

// A run given a schema prints two things the model wrote — the prose answer,
// which is the reasoning there, and the JSON document — and both go through
// the hooks. A hook that renders the document with something other than jq
// leaves the pipe carrying what is no longer JSON, which is the hook
// author's business and is why the README says so beside --schema.
func TestADisplayHookSeesBothHalvesOfASchemaRun(t *testing.T) {
	kept := t.TempDir() + "/seen"
	armed := displayHooks(t, map[string]string{"keep.sh": keeps(kept)})

	client := &scriptedClient{replies: []model.AssistantMessage{
		model.NewAssistantMessage("user 1 is Ada"),
		model.NewAssistantMessage(`{"id": 1, "name": "Ada"}`),
	}}

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), client,
		registryWith(t), asked("who is user 1?"), testSchema(t), false, defaultMaxTurns,
		armed); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}

	seen := readSeen(t, kept)
	if !contains(seen, "user 1 is Ada") {
		t.Errorf("the hook saw %q, want the prose answer among them", seen)
	}
	// The document's own quotation marks do not survive the hook's crude
	// reading of the payload, so it is enough that something JSON-shaped
	// reached it after the prose.
	if len(seen) != 2 {
		t.Errorf("the hook saw %q, want the prose answer and the document", seen)
	}
}

// readSeen returns the messages a keeps hook was shown. A file that is not
// there is a hook that never ran, which is one of the answers a test here is
// looking for rather than a failure.
func readSeen(t *testing.T, where string) []string {
	t.Helper()
	data, err := os.ReadFile(where)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		t.Fatalf("reading %s: %v", where, err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func contains(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

// colors is a MessageDisplay hook that stands in for a renderer painting the
// terminal: the text it hands back carries escape sequences, which is what
// such a hook is for. They are written as JSON escapes and printed with a
// format of %s, so the shell never reads a backslash as its own.
const colors = "#!/bin/sh\n" +
	"printf '%s' '{\"hookSpecificOutput\": {\"hookEventName\": \"MessageDisplay\", " +
	"\"displayContent\": \"\\u001b[1mfindings\\u001b[0m\"}}'\n"

// A display hook's line carries the text it printed, and carries it byte for
// byte. Nothing else in the log holds it — the conversation, the snapshot and
// the log all keep the model's own words — so this line is the only account
// of what the user actually saw. The control codes are kept because a record
// with them taken out would not be that account, and the encoder escapes
// them, so the line is still one JSON object.
func TestADisplayHookRecordsTheTextItPrinted(t *testing.T) {
	armed := displayHooks(t, map[string]string{"color.sh": colors})
	client := &scriptedClient{replies: []model.AssistantMessage{
		model.NewAssistantMessage("findings"),
	}}

	_, lg, read := recorded(t)

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), lg, client,
		registryWith(t), asked("check"), nil, false, defaultMaxTurns, armed); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}

	const painted = "\x1b[1mfindings\x1b[0m"
	if got := strings.TrimSpace(out.String()); got != painted {
		t.Errorf("output = %q, want the hook's text", got)
	}

	var found bool
	for _, e := range read() {
		if e.Moment != string(hooks.MessageDisplay) {
			continue
		}
		found = true
		if e.Content != painted {
			t.Errorf("content = %q, want the text the hook printed, escape codes and all", e.Content)
		}
	}
	if !found {
		t.Fatal("nothing was recorded for the display hook")
	}
}

// The two halves of a schema run are hooked separately, so each gets a line
// of its own saying what was printed in its place. The document's line is the
// one to read when a piped answer came out looking like something other than
// JSON.
func TestEachHalfOfASchemaRunRecordsWhatWasPrinted(t *testing.T) {
	armed := displayHooks(t, map[string]string{"render.sh": renders})
	client := &scriptedClient{replies: []model.AssistantMessage{
		model.NewAssistantMessage("user 1 is Ada"),
		model.NewAssistantMessage(`{"id": 1, "name": "Ada"}`),
	}}

	_, lg, read := recorded(t)

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), lg, client,
		registryWith(t), asked("who is user 1?"), testSchema(t), false, defaultMaxTurns,
		armed); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}

	var printed []string
	for _, e := range read() {
		if e.Moment == string(hooks.MessageDisplay) {
			printed = append(printed, e.Content)
		}
	}
	if len(printed) != 2 {
		t.Fatalf("%d display lines, want one for the prose and one for the document", len(printed))
	}
	for i, got := range printed {
		if got != "RENDERED" {
			t.Errorf("line %d printed %q, want the hook's text", i+1, got)
		}
	}
}
