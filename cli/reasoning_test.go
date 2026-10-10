package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/eventlog"
	"github.com/ChrisVilches/agi/hooks"
	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/session"
	"github.com/ChrisVilches/agi/tools"
)

// The reasoning a thinking model returns, used throughout so that a test
// asserting where it appears is not also asserting what it says.
const deliberation = "they asked about the disk, so probe is the tool for it"

// before reports whether the first string appears ahead of the second, and
// fails the test if either is missing. Where reasoning is printed is the whole
// of what these tests are about, so "it appeared" is never enough on its own.
func before(t *testing.T, text, first, second string) bool {
	t.Helper()
	i, j := strings.Index(text, first), strings.Index(text, second)
	if i < 0 {
		t.Errorf("output = %q, want it to contain %q", text, first)
		return false
	}
	if j < 0 {
		t.Errorf("output = %q, want it to contain %q", text, second)
		return false
	}
	return i < j
}

// Reasoning goes above the turn it belongs to, and the turn here is prose
// written alongside a tool call.
func TestReasoningIsShownAboveAnIntermediateTurn(t *testing.T) {
	tool := &fakeTool{name: "probe", output: "disk is fine"}
	client := &scriptedClient{replies: []model.AssistantMessage{
		{
			Content:          "let me look",
			ReasoningContent: deliberation,
			ToolRequests: []model.ToolRequest{
				{ID: "call-1", Name: "probe", Arguments: json.RawMessage(`{}`)},
			},
		},
		model.NewAssistantMessage("all good"),
	}}

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), client,
		registryWith(t, tool), asked("check the disk"), nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}

	text := out.String()
	if !strings.Contains(text, tools.Thinking()) {
		t.Errorf("output = %q, want the [thinking] label", text)
	}
	if !before(t, text, deliberation, "let me look") {
		t.Errorf("output = %q, want the reasoning above the prose it produced", text)
	}
	if !before(t, text, deliberation, "running probe") {
		t.Errorf("output = %q, want the reasoning above the call it led to", text)
	}
}

// The final answer is an assistant turn like any other, and the reasoning
// behind it is printed above it — from the loop, before the answer the caller
// prints once the loop has ended.
func TestReasoningIsShownAboveTheFinalAnswer(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		{Content: "the disk is fine", ReasoningContent: deliberation},
	}}

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), client,
		registryWith(t), asked("check the disk"), nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}

	text := out.String()
	if !before(t, text, deliberation, "the disk is fine") {
		t.Errorf("output = %q, want the reasoning above the answer", text)
	}
}

// Most turns come back with no reasoning at all — every turn from a model that
// does not think, and every turn through a gateway — and nothing is printed
// for them. A bare label over nothing would mark every turn of an ordinary run.
func TestNoLabelIsPrintedForATurnWithoutReasoning(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		model.NewAssistantMessage("the answer"),
	}}

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), client,
		registryWith(t), asked("question"), nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}

	if strings.Contains(out.String(), "[thinking]") {
		t.Errorf("output = %q, want no label where there was no reasoning", out.String())
	}
}

// Piped, the answer is all that goes anywhere: another program is reading it.
func TestReasoningIsNeverPiped(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		{Content: "the disk is fine", ReasoningContent: deliberation},
	}}

	var out bytes.Buffer
	if err := runAgent(context.Background(), pipedOutput(&out), nolog(), client,
		registryWith(t), asked("check the disk"), nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}

	if strings.Contains(out.String(), deliberation) || strings.Contains(out.String(), "[thinking]") {
		t.Errorf("output = %q, want no reasoning on a pipe", out.String())
	}
	if strings.TrimSpace(out.String()) != "the disk is fine" {
		t.Errorf("output = %q, want the answer alone", out.String())
	}
}

// Reasoning is progress and goes where the rest of the progress goes. A piped
// run with --verbose asked for everything the run can say about itself, so it
// gets the reasoning too — on standard error, never on the stream the other
// program is reading.
func TestReasoningGoesToStderrInAPipedVerboseRun(t *testing.T) {
	tool := &fakeTool{name: "probe", output: "disk is fine"}
	client := &scriptedClient{replies: []model.AssistantMessage{
		{
			Content:          "let me look",
			ReasoningContent: deliberation,
			ToolRequests: []model.ToolRequest{
				{ID: "call-1", Name: "probe", Arguments: json.RawMessage(`{}`)},
			},
		},
		model.NewAssistantMessage("all good"),
	}}

	var stdout, stderr bytes.Buffer
	out := route(false, &stdout, &stderr, true)
	if err := runAgent(context.Background(), out, nolog(), client,
		registryWith(t, tool), asked("check the disk"), nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}

	// The progress is there, which is what --verbose was asked for, and the
	// reasoning is among it.
	if !strings.Contains(stderr.String(), "running probe") {
		t.Errorf("stderr = %q, want the progress a verbose run asks for", stderr.String())
	}
	if !strings.Contains(stderr.String(), deliberation) {
		t.Errorf("stderr = %q, want the reasoning a verbose run asks for", stderr.String())
	}
	if !before(t, stderr.String(), deliberation, "let me look") {
		t.Errorf("stderr = %q, want the reasoning above the prose it produced", stderr.String())
	}

	// Standard output is the channel another program is reading, and progress
	// is never on it.
	if strings.Contains(stdout.String(), deliberation) || strings.Contains(stdout.String(), "[thinking]") {
		t.Errorf("stdout = %q, want no reasoning on the data channel", stdout.String())
	}
	if strings.TrimSpace(stdout.String()) != "all good" {
		t.Errorf("stdout = %q, want the answer alone", stdout.String())
	}
}

// Reasoning is the one thing the model wrote that no hook is shown. It is
// printed exactly as it came back, so a renderer armed to lay out the answer
// is never handed the model talking to itself.
func TestReasoningNeverReachesTheDisplayHook(t *testing.T) {
	kept := t.TempDir() + "/seen"
	armed := displayHooks(t, map[string]string{"keep.sh": keeps(kept)})

	tool := &fakeTool{name: "probe", output: "fine"}
	client := &scriptedClient{replies: []model.AssistantMessage{
		{
			Content:          "thinking out loud",
			ReasoningContent: deliberation,
			ToolRequests: []model.ToolRequest{
				{ID: "call-1", Name: "probe", Arguments: json.RawMessage(`{}`)},
			},
		},
		{Content: "done", ReasoningContent: "the probe answered, so I can say so"},
	}}

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), client,
		registryWith(t, tool), asked("check"), nil, false, defaultMaxTurns, armed); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}

	// It was printed, so this is about what the hook was shown rather than
	// about whether the text existed to show.
	if !strings.Contains(out.String(), deliberation) {
		t.Fatalf("output = %q, want the reasoning printed", out.String())
	}

	seen := readSeen(t, kept)
	for _, unwanted := range []string{deliberation, "the probe answered, so I can say so"} {
		if contains(seen, unwanted) {
			t.Errorf("the hook saw %q, want no reasoning among them", seen)
		}
	}
	// The turns themselves are hooked exactly as they were before.
	for _, want := range []string{"thinking out loud", "done"} {
		if !contains(seen, want) {
			t.Errorf("the hook saw %q, want %q among them", seen, want)
		}
	}
}

// Reasoning goes exactly where the rest of the progress goes, so there is one
// gate and not two: a fork is watched and shows it, a subagent is silent and
// does not, and a run nobody reads shows nothing at all.
func TestReasoningFollowsTheProgressStream(t *testing.T) {
	var buf bytes.Buffer
	for _, tt := range []struct {
		name string
		out  output
		want bool
	}{
		{"at a terminal", humanOutput(&buf), true},
		{"piped", route(false, &buf, io.Discard, false), false},
		{"piped with --verbose", route(false, &buf, io.Discard, true), true},
		{"a fork of a watched run", humanOutput(&buf).nested(), true},
		{"a fork of a piped run", route(false, &buf, io.Discard, false).nested(), false},
		{"a subagent", quiet(), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var printed bytes.Buffer
			o := tt.out
			o.steps = &printed
			o.reason(deliberation)

			if got := strings.Contains(printed.String(), deliberation); got != tt.want {
				t.Errorf("reasoning printed = %v, want %v — it goes where the rest of the progress goes", got, tt.want)
			}
		})
	}
}

// The log is the record and is read afterwards, so it keeps a turn's reasoning
// whatever the run was printing at the time. A record that held it only when
// somebody happened to be at a terminal would be a record of the terminal
// rather than of the run.
func TestTheLogKeepsReasoningEvenWhenItIsNotPrinted(t *testing.T) {
	for _, tt := range []struct {
		name string
		out  func(*bytes.Buffer) output
	}{
		{"at a terminal", humanOutput},
		{"piped", pipedOutput},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &scriptedClient{replies: []model.AssistantMessage{
				{Content: "the disk is fine", ReasoningContent: deliberation},
			}}
			_, lg, read := recorded(t)

			var out bytes.Buffer
			if err := runAgent(context.Background(), tt.out(&out), lg, client,
				registryWith(t), asked("check the disk"), nil, false, defaultMaxTurns, hooks.None()); err != nil {
				t.Fatalf("runAgent() = %v, want nil", err)
			}

			var replies int
			for _, e := range read() {
				if e.Kind != eventlog.ModelResponse {
					// It belongs to the line recording the reply and to no other.
					if e.ReasoningContent != "" {
						t.Errorf("a %s line carries reasoning, want it only on the reply", e.Kind)
					}
					continue
				}
				replies++
				if e.ReasoningContent != deliberation {
					t.Errorf("logged reasoning = %q, want %q", e.ReasoningContent, deliberation)
				}
				// The two arrived together and stay apart: what the model said
				// is one field and what it worked through is another.
				if e.Content != "the disk is fine" {
					t.Errorf("logged content = %q, want the reply alone", e.Content)
				}
			}
			if replies != 1 {
				t.Errorf("replies logged = %d, want 1", replies)
			}
		})
	}
}

// A turn that came back with no reasoning writes no such field, so an ordinary
// run's log is exactly what it was.
func TestTheLogOmitsReasoningWhenThereIsNone(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		model.NewAssistantMessage("the answer"),
	}}
	sess, lg, read := recorded(t)

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), lg, client,
		registryWith(t), asked("question"), nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v, want nil", err)
	}
	read()

	data, err := os.ReadFile(sess.LogPath())
	if err != nil {
		t.Fatalf("ReadFile() = %v", err)
	}
	if strings.Contains(string(data), "reasoning_content") {
		t.Errorf("log = %s, want no reasoning key where there was none", data)
	}
}

// Storing is not displaying. A piped run shows no reasoning and still saves
// every word of it, because the snapshot is what the next run sends back to
// the endpoint and has nothing to do with who was watching this one.
func TestAPipedRunStillStoresReasoning(t *testing.T) {
	sess, err := session.New()
	if err != nil {
		t.Fatalf("session.New() = %v", err)
	}
	defer os.RemoveAll(sess.Dir())

	client := &scriptedClient{replies: []model.AssistantMessage{
		{Content: "the disk is fine", ReasoningContent: deliberation},
	}}

	var out bytes.Buffer
	if err := runSession(context.Background(), pipedOutput(&out), nolog(), client,
		registryWith(t), sess, "system", "check the disk", nil, nil, false, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runSession() = %v, want nil", err)
	}

	if strings.Contains(out.String(), deliberation) {
		t.Fatalf("output = %q, want no reasoning printed on a pipe", out.String())
	}

	stored, err := sess.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	last, ok := stored.LastAssistant()
	if !ok {
		t.Fatal("the stored conversation does not end with an assistant turn")
	}
	if last.ReasoningContent != deliberation {
		t.Errorf("stored reasoning = %q, want it saved whether or not it was shown", last.ReasoningContent)
	}
}
