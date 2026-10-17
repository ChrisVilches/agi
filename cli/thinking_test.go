package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/eventlog"
	"github.com/ChrisVilches/agi/hooks"
	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/tools"
	"github.com/fatih/color"
)

// thinkingHooks arms a run with MessageDisplay hooks over the deliberation
// alone, which is the half of the event displayHooks does not cover. It is at
// a terminal for the reason displayHooks is.
func thinkingHooks(t *testing.T, scripts map[string]string) *hooks.Set {
	t.Helper()
	var entries []string
	for name := range scripts {
		entries = append(entries, `{"thinking": true, "command": "./`+name+`", "args": []}`)
	}
	return armWith(t, `{"MessageDisplay": [`+strings.Join(entries, ",")+`]}`, scripts).WithTTY(true)
}

// thinks is the model answering after one turn of deliberation it wrote prose
// alongside, which is every piece of text this file is about in one reply.
func thinks() *scriptedClient {
	return &scriptedClient{replies: []model.AssistantMessage{
		{
			Content:          "thinking out loud",
			ReasoningContent: deliberation,
			ToolRequests: []model.ToolRequest{
				{ID: "call-1", Name: "probe", Arguments: json.RawMessage(`{}`)},
			},
		},
		model.NewAssistantMessage("the disk is fine"),
	}}
}

// withColor turns the styling on for a test that is about what agi drew
// rather than about what it said. It is off by default, because the package
// that does the drawing disables itself when nothing is watching.
func withColor(t *testing.T) {
	t.Helper()
	previous := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = previous })
}

// The whole point of the field: a hook asks for the deliberation and lays it
// out, and what the user reads is the hook's text.
func TestAThinkingHookRendersTheDeliberation(t *testing.T) {
	armed := thinkingHooks(t, map[string]string{"render.sh": renders})

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), thinks(),
		registryWith(t, &fakeTool{name: "probe", output: "fine"}), asked("check"),
		nil, outcomeCheck{}, defaultMaxTurns, armed); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}

	text := out.String()
	if !strings.Contains(text, "RENDERED") {
		t.Errorf("output = %q, want the hook's text in place of the deliberation", text)
	}
	if strings.Contains(text, deliberation) {
		t.Errorf("output = %q, want the model's own deliberation replaced", text)
	}
	// The other half of the event is untouched, which is what declaring one
	// of the two is for: the prose and the answer are not what this hook
	// asked to render.
	for _, want := range []string{"thinking out loud", "the disk is fine"} {
		if !strings.Contains(text, want) {
			t.Errorf("output = %q, want %q printed as the model wrote it", text, want)
		}
	}
}

// The label is agi's own chrome and stays whoever laid the text out. A
// renderer is given the model's words and never the frame agi puts them in,
// which is the rule everything agi prints follows.
func TestTheThinkingLabelSurvivesAHook(t *testing.T) {
	armed := thinkingHooks(t, map[string]string{"render.sh": renders})

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), thinks(),
		registryWith(t, &fakeTool{name: "probe", output: "fine"}), asked("check"),
		nil, outcomeCheck{}, defaultMaxTurns, armed); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}

	if !before(t, out.String(), tools.Thinking(), "RENDERED") {
		t.Errorf("output = %q, want the label above the text the hook handed back", out.String())
	}
}

// agi dims the deliberation because nothing else was going to, and that is a
// default rather than chrome. A run that armed a hook over it armed something
// to decide how it looks, so the dimming is dropped whole — a renderer laying
// text out over text agi had already styled would be laying it out over
// escape sequences.
func TestTheDimmingIsAgisOnlyUntilAHookClaimsIt(t *testing.T) {
	for _, tt := range []struct {
		name   string
		armed  *hooks.Set
		dimmed bool
	}{
		{"no hook over the deliberation", hooks.None(), true},
		{"a hook over the prose instead", displayHooks(t, map[string]string{"r.sh": renders}), true},
		{"a hook over the deliberation", thinkingHooks(t, map[string]string{"k.sh": keeps(t.TempDir() + "/seen")}), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withColor(t)

			var out bytes.Buffer
			if err := runAgent(context.Background(), humanOutput(&out), nolog(),
				&scriptedClient{replies: []model.AssistantMessage{
					{Content: "done", ReasoningContent: deliberation},
				}},
				registryWith(t), asked("check"), nil, outcomeCheck{}, defaultMaxTurns, tt.armed); err != nil {
				t.Fatalf("runAgent() = %v, want nil", err)
			}

			if got := strings.Contains(out.String(), tools.Detail(deliberation)); got != tt.dimmed {
				t.Errorf("the deliberation was dimmed = %v, want %v", got, tt.dimmed)
			}
			// It is printed either way. This is about how it was drawn, not
			// about whether the run still says it.
			if !strings.Contains(out.String(), deliberation) {
				t.Errorf("output = %q, want the deliberation printed", out.String())
			}
		})
	}
}

// A hook declares one of the two kinds of message and is spawned for that one
// alone. This is the half TestAnOrdinaryDisplayHookNeverSeesTheDeliberation
// asserts from the other side.
func TestAThinkingHookSeesOnlyTheDeliberation(t *testing.T) {
	kept := t.TempDir() + "/seen"
	armed := thinkingHooks(t, map[string]string{"keep.sh": keeps(kept)})

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), thinks(),
		registryWith(t, &fakeTool{name: "probe", output: "fine"}), asked("check"),
		nil, outcomeCheck{}, defaultMaxTurns, armed); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}

	seen := readSeen(t, kept)
	if !contains(seen, deliberation) {
		t.Errorf("the hook saw %q, want the deliberation among them", seen)
	}
	for _, unwanted := range []string{"thinking out loud", "the disk is fine"} {
		if contains(seen, unwanted) {
			t.Errorf("the hook saw %q, want nothing the model wrote for the reader", seen)
		}
	}
}

// A hook that printed nothing prints nothing here either, label included. A
// bare label over an empty block would mark the turn as having deliberated
// and then show none of it.
func TestAThinkingHookThatPrintsNothingPrintsNoLabel(t *testing.T) {
	const silent = "#!/bin/sh\n" +
		`printf '{"hookSpecificOutput": {"hookEventName": "MessageDisplay", "displayContent": ""}}'` + "\n"
	armed := thinkingHooks(t, map[string]string{"hide.sh": silent})

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), nolog(),
		&scriptedClient{replies: []model.AssistantMessage{
			{Content: "the disk is fine", ReasoningContent: deliberation},
		}},
		registryWith(t), asked("check"), nil, outcomeCheck{}, defaultMaxTurns, armed); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}

	if strings.Contains(out.String(), "[thinking]") {
		t.Errorf("output = %q, want no label where the hook printed nothing", out.String())
	}
	if strings.TrimSpace(out.String()) != "the disk is fine" {
		t.Errorf("output = %q, want the answer alone", out.String())
	}
}

// The deliberation is progress, and progress is dropped when nobody is
// watching. agi does not spawn a renderer for a message it is about to throw
// away, which is the rule the rest of the event already follows.
func TestAThinkingHookIsNotSpawnedForAMessageNobodyReads(t *testing.T) {
	kept := t.TempDir() + "/seen"
	armed := thinkingHooks(t, map[string]string{"keep.sh": keeps(kept)})

	var out bytes.Buffer
	if err := runAgent(context.Background(), pipedOutput(&out), nolog(),
		&scriptedClient{replies: []model.AssistantMessage{
			{Content: "the disk is fine", ReasoningContent: deliberation},
		}},
		registryWith(t), asked("check"), nil, outcomeCheck{}, defaultMaxTurns, armed); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}

	if seen := readSeen(t, kept); len(seen) != 0 {
		t.Errorf("the hook saw %q, want nothing in a run that displays no progress", seen)
	}
	if strings.TrimSpace(out.String()) != "the disk is fine" {
		t.Errorf("output = %q, want the answer alone", out.String())
	}
}

// Nothing else in the log holds what a display hook printed — the
// conversation, the snapshot and the log all keep the model's own words — so
// the hook's own line is the only account of what the user read, and it has
// to be written for this half of the event as it is for the other.
func TestAThinkingHookRecordsWhatItPrinted(t *testing.T) {
	armed := thinkingHooks(t, map[string]string{"render.sh": renders})
	_, lg, read := recorded(t)

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), lg,
		&scriptedClient{replies: []model.AssistantMessage{
			{Content: "the disk is fine", ReasoningContent: deliberation},
		}},
		registryWith(t), asked("check"), nil, outcomeCheck{}, defaultMaxTurns, armed); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}

	var lines int
	for _, e := range read() {
		if e.Kind != eventlog.HookResult || e.Moment != string(hooks.MessageDisplay) {
			continue
		}
		lines++
		if e.Content != "RENDERED" {
			t.Errorf("the hook's line holds %q, want the text it printed", e.Content)
		}
		// It guarded no call, so it claims none.
		if e.Tool != "" || e.RequestID != "" {
			t.Errorf("the hook's line names tool %q and request %q, want neither", e.Tool, e.RequestID)
		}
	}
	if lines != 1 {
		t.Errorf("display hook lines = %d, want the one run over the deliberation", lines)
	}

	// The record keeps the model's own words whatever was printed.
	var reasoned int
	for _, e := range read() {
		if e.Kind == eventlog.ModelResponse && e.ReasoningContent == deliberation {
			reasoned++
		}
	}
	if reasoned != 1 {
		t.Errorf("replies holding the model's own deliberation = %d, want 1", reasoned)
	}
}

// Two display hooks are otherwise one line written twice, and they fire on
// different text. A listing that did not say which would claim a renderer is
// armed over the message it is not armed over, which is the lie the listing
// exists to avoid.
func TestHookListSaysWhichDisplayHookRendersTheDeliberation(t *testing.T) {
	armed := armWith(t, `{"MessageDisplay": [
	  {"thinking": true, "command": "./think.sh", "args": []},
	  {"command": "./say.sh", "args": []}
	]}`, map[string]string{"think.sh": "#!/bin/sh\n", "say.sh": "#!/bin/sh\n"})

	list := hookList(armed, []string{"bash"})
	for _, line := range strings.Split(strings.TrimSpace(list), "\n") {
		if strings.Contains(line, "think.sh") != strings.Contains(line, "thinking") {
			t.Errorf("hookList() line = %q, want the deliberation's renderer marked and only it", line)
		}
	}
	// A display hook has no tool and declined to name none, so the wildcard a
	// matcher-less tool hook is shown with has no business here.
	if strings.Contains(list, "*") {
		t.Errorf("hookList() = %q, want no matcher shown for a hook that guards no tool", list)
	}
}

// The phases after the loop are assistant turns like any other, and the
// deliberation behind each is shown above it. They were the two call sites
// that had an output and a log but no hooks, which is why they now take the
// agent's hooked instead.
func TestTheSchemaPhaseRendersItsDeliberation(t *testing.T) {
	kept := t.TempDir() + "/seen"
	armed := thinkingHooks(t, map[string]string{"keep.sh": keeps(kept)})

	client := &scriptedClient{replies: []model.AssistantMessage{
		model.NewAssistantMessage("User 1 is Ada."),
		{Content: `{"id": 1, "name": "Ada"}`, ReasoningContent: "it wants an id and a name"},
	}}

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), client,
		registryWith(t), asked("who is user 1?"), testSchema(t), outcomeCheck{}, defaultMaxTurns, armed); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}

	if seen := readSeen(t, kept); !contains(seen, "it wants an id and a name") {
		t.Errorf("the hook saw %q, want the restatement's deliberation among them", seen)
	}
}
