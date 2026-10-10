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

// The one case where reasoning parts company with the rest of the progress. A
// piped run with --verbose shows its steps on standard error, and reasoning
// still stays out of them: it is shown to a person watching a terminal and to
// nobody else.
func TestReasoningStaysOutOfAPipedVerboseRun(t *testing.T) {
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

	// The progress is there, which is what --verbose was asked for.
	if !strings.Contains(stderr.String(), "running probe") {
		t.Errorf("stderr = %q, want the progress a verbose run asks for", stderr.String())
	}
	for name, stream := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String()} {
		if strings.Contains(stream, deliberation) || strings.Contains(stream, "[thinking]") {
			t.Errorf("%s = %q, want no reasoning away from a terminal", name, stream)
		}
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

// A fork is watched where a subagent is not, so a fork's reasoning is printed
// under its marker and a subagent's goes nowhere, exactly as the rest of what
// each prints does.
func TestNestedOutputCarriesReasoningAndQuietDoesNot(t *testing.T) {
	var buf bytes.Buffer
	if fork := humanOutput(&buf).nested(); !fork.reasoningShown {
		t.Error("a fork shows no reasoning, want it shown as its other progress is")
	}
	if piped := route(false, &buf, io.Discard, false).nested(); piped.reasoningShown {
		t.Error("a fork of a piped run shows reasoning, want none")
	}
	if quiet().reasoningShown {
		t.Error("a silent run shows reasoning, want nothing from one nobody reads")
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
