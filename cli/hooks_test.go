package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/eventlog"
	"github.com/ChrisVilches/agi/hooks"
	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/tools"
	"github.com/ChrisVilches/agi/tools/userdefined"
)

// armWith builds a set of hooks from a manifest and the scripts it names,
// written into a folder of their own.
func armWith(t *testing.T, manifest string, scripts map[string]string) *hooks.Set {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing the manifest: %v", err)
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	armed, err := hooks.Load([]string{dir}, hooks.Config{})
	if err != nil {
		t.Fatalf("hooks.Load() = %v", err)
	}
	return armed
}

// callsProbe is a model that asks for the probe tool once and then answers.
func callsProbe(arguments string) *scriptedClient {
	return &scriptedClient{replies: []model.AssistantMessage{
		{ToolRequests: []model.ToolRequest{
			{ID: "call-1", Name: "probe", Arguments: json.RawMessage(arguments)},
		}},
		model.NewAssistantMessage("done"),
	}}
}

// A hook that refuses a call stops the tool from running, and the model is
// told why — which is what a tool failure is for, and what lets the agent try
// something else rather than the run ending.
func TestAHookRefusingACallReachesTheModel(t *testing.T) {
	armed := armWith(t, `{"hooks": {"PreToolUse": [
	  {"matcher": "^probe$", "command": "./no.sh", "args": []}
	]}}`, map[string]string{"no.sh": "#!/bin/sh\necho 'probing is not allowed' >&2\nexit 2\n"})

	tool := &fakeTool{name: "probe", output: "disk is fine"}
	client := callsProbe(`{"a":1}`)

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), client,
		registryWith(t, tool), asked("check"), nil, defaultMaxTurns, armed); err != nil {
		t.Fatalf("runAgent() = %v, want the run to carry on", err)
	}
	if len(tool.calls) != 0 {
		t.Errorf("the tool ran %d times, want the refusal to have stopped it", len(tool.calls))
	}
	if !strings.Contains(out.String(), "probing is not allowed") {
		t.Errorf("output = %q, want the hook's reason", out.String())
	}

	// The model has to read the reason, or it has nothing to correct.
	last := client.sent[len(client.sent)-1].Conversation
	if !strings.Contains(renderAll(last), "probing is not allowed") {
		t.Error("the model was not told why the call was refused")
	}
}

// A hook rewriting the arguments changes what the tool is handed, and changes
// nothing about the record: the assistant turn keeps the call as the model
// wrote it, because what was said is never edited.
func TestAHookRewritingACallLeavesTheRecordAlone(t *testing.T) {
	armed := armWith(t, `{"hooks": {"PreToolUse": [
	  {"matcher": "^probe$", "command": "./fix.sh", "args": []}
	]}}`, map[string]string{"fix.sh": "#!/bin/sh\n" +
		`echo '{"hookSpecificOutput": {"updatedInput": {"a": 2}}}'` + "\n"})

	tool := &fakeTool{name: "probe", output: "disk is fine"}
	client := callsProbe(`{"a":1}`)

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), client,
		registryWith(t, tool), asked("check"), nil, defaultMaxTurns, armed); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}
	if len(tool.calls) != 1 || !strings.Contains(tool.calls[0], `"a":2`) {
		t.Errorf("the tool was called with %v, want the hook's arguments", tool.calls)
	}

	// The assistant turn is the record of what the model asked for, and a
	// hook may not touch it.
	last := client.sent[len(client.sent)-1].Conversation
	for _, m := range last.Messages {
		if assistant, ok := m.(model.AssistantMessage); ok && len(assistant.ToolRequests) > 0 {
			if got := string(assistant.ToolRequests[0].Arguments); got != `{"a":1}` {
				t.Errorf("the recorded call is %s, want the arguments the model wrote", got)
			}
		}
	}
	// And the user is shown that what ran is not what was announced.
	if !strings.Contains(out.String(), "a hook changed this call") {
		t.Errorf("output = %q, want the change shown", out.String())
	}
}

// A hook stopping the run's own agent ends the run, and the reason is what
// the user is told. The conversation is still stored, so the session can be
// resumed: everything said was said, and a guard firing is not in doubt.
func TestAHookStoppingTheRunEndsIt(t *testing.T) {
	armed := armWith(t, `{"hooks": {"PreToolUse": [{"command": "./halt.sh", "args": []}]}}`,
		map[string]string{"halt.sh": "#!/bin/sh\n" +
			`echo '{"continue": false, "stopReason": "the branch is protected"}'` + "\n"})

	tool := &fakeTool{name: "probe", output: "disk is fine"}
	client := callsProbe(`{}`)

	var out bytes.Buffer
	err := runAgent(context.Background(), humanOutput(&out), nolog(), client,
		registryWith(t, tool), asked("check"), nil, defaultMaxTurns, armed)

	var stop *hooks.Stop
	if !errors.As(err, &stop) {
		t.Fatalf("runAgent() = %v, want the run stopped", err)
	}
	if stop.Reason != "the branch is protected" {
		t.Errorf("reason = %q, want the hook's own", stop.Reason)
	}
	if !ended(err) {
		t.Error("ended() says the conversation is not worth storing, want it stored")
	}
	if len(tool.calls) != 0 {
		t.Error("the tool ran, want it stopped before it did")
	}
}

// A stopped run leaves nothing outstanding, or the snapshot could not be
// resumed: a provider may refuse a turn that moves on from a call it asked
// for and never saw answered.
func TestAStoppedRunAnswersTheCallsItNeverMade(t *testing.T) {
	armed := armWith(t, `{"hooks": {"PreToolUse": [{"command": "./halt.sh", "args": []}]}}`,
		map[string]string{"halt.sh": "#!/bin/sh\necho '{\"continue\": false}'\n"})

	tool := &fakeTool{name: "probe", output: "fine"}
	client := &scriptedClient{replies: []model.AssistantMessage{
		{ToolRequests: []model.ToolRequest{
			{ID: "call-1", Name: "probe", Arguments: json.RawMessage(`{}`)},
			{ID: "call-2", Name: "probe", Arguments: json.RawMessage(`{}`)},
		}},
	}}

	conv := asked("check")
	var out bytes.Buffer
	if _, err := converse(context.Background(), humanOutput(&out), nolog(), client,
		registryWith(t, tool), conv, defaultMaxTurns, armed, eventlog.MainAgent); err == nil {
		t.Fatal("converse() = nil, want the run stopped")
	}
	if pending := conv.PendingToolRequests(); len(pending) != 0 {
		t.Errorf("%d calls left outstanding, want none", len(pending))
	}
	if err := conv.Validate(); err != nil {
		t.Errorf("the stored conversation does not validate: %v", err)
	}
}

// A PostToolUse hook replaces what the model reads, and what is stored is
// what the model read: a conversation holding a result nobody was shown would
// be a record of a run that did not happen.
func TestAPostHookReplacesWhatTheModelReads(t *testing.T) {
	armed := armWith(t, `{"hooks": {"PostToolUse": [{"command": "./redact.sh", "args": []}]}}`,
		map[string]string{"redact.sh": "#!/bin/sh\n" +
			`echo '{"hookSpecificOutput": {"updatedToolOutput": {"type": "text", "text": "[redacted]"}}}'` + "\n"})

	tool := &fakeTool{name: "probe", output: "AKIA0123456789"}
	client := callsProbe(`{}`)

	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), client,
		registryWith(t, tool), asked("check"), nil, defaultMaxTurns, armed); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}

	last := client.sent[len(client.sent)-1].Conversation
	whole := renderAll(last)
	if strings.Contains(whole, "AKIA0123456789") {
		t.Error("the model was shown the output the hook replaced")
	}
	if !strings.Contains(whole, "[redacted]") {
		t.Error("the model was not shown the replacement")
	}
}

// additionalContext reaches the model as a harness instruction, and it waits
// for every call of the turn to be answered: an instruction between a request
// and its result would strand the calls made alongside it.
func TestAdditionalContextArrivesAfterTheWholeBatch(t *testing.T) {
	armed := armWith(t, `{"hooks": {"PreToolUse": [{"command": "./say.sh", "args": []}]}}`,
		map[string]string{"say.sh": "#!/bin/sh\n" +
			`echo '{"hookSpecificOutput": {"additionalContext": "this touches production"}}'` + "\n"})

	tool := &fakeTool{name: "probe", output: "fine"}
	client := &scriptedClient{replies: []model.AssistantMessage{
		{ToolRequests: []model.ToolRequest{
			{ID: "call-1", Name: "probe", Arguments: json.RawMessage(`{}`)},
			{ID: "call-2", Name: "probe", Arguments: json.RawMessage(`{}`)},
		}},
		model.NewAssistantMessage("done"),
	}}

	conv := asked("check")
	var out bytes.Buffer
	if _, err := converse(context.Background(), humanOutput(&out), nolog(), client,
		registryWith(t, tool), conv, defaultMaxTurns, armed, eventlog.MainAgent); err != nil {
		t.Fatalf("converse() = %v", err)
	}

	if err := conv.Validate(); err != nil {
		t.Fatalf("the conversation does not validate: %v", err)
	}
	var instructions int
	for _, m := range conv.Messages {
		if _, ok := m.(model.HarnessInstruction); ok {
			instructions++
		}
	}
	if instructions != 1 {
		t.Errorf("%d instructions, want the two hooks' context joined into one", instructions)
	}
	if !strings.Contains(renderAll(conv), "this touches production") {
		t.Error("the model was not told what the hook added")
	}
}

// Every hook that ran is recorded, including the one that refused the call,
// because that is the line a reader of the log is looking for.
func TestHooksAreRecorded(t *testing.T) {
	armed := armWith(t, `{"hooks": {"PreToolUse": [
	  {"command": "./pass.sh", "args": []},
	  {"command": "./no.sh", "args": []}
	]}}`, map[string]string{
		"pass.sh": "#!/bin/sh\nexit 0\n",
		"no.sh":   "#!/bin/sh\necho nope >&2\nexit 2\n",
	})

	_, lg, read := recorded(t)

	tool := &fakeTool{name: "probe", output: "fine"}
	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), lg, callsProbe(`{}`),
		registryWith(t, tool), asked("check"), nil, defaultMaxTurns, armed); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}

	var results, decisions []string
	for _, line := range only(read(), eventlog.HookResult) {
		results = append(results, line.Hook)
		decisions = append(decisions, line.Decided)
	}
	if len(results) != 2 {
		t.Fatalf("%d hook lines, want both hooks recorded", len(results))
	}
	if decisions[0] != "proceeded" || decisions[1] != "refused" {
		t.Errorf("decisions = %v, want the first to have passed and the second to have refused", decisions)
	}
}

// renderAll is every turn of a conversation as text, for a test that only
// cares whether something reached the model at all. Not every kind of turn
// renders — a user turn is its content — so the ones that do are asked and
// the rest are printed as they stand.
func renderAll(conv *model.Conversation) string {
	var b strings.Builder
	for _, m := range conv.Messages {
		if r, ok := m.(interface{ Render() string }); ok {
			b.WriteString(r.Render())
		} else {
			fmt.Fprintf(&b, "%+v", m)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// A run given no hooks behaves exactly as it did before hooks existed, which
// is what nothing having to ask whether there are any is for.
func TestNoHooksChangesNothing(t *testing.T) {
	tool := &fakeTool{name: "probe", output: "disk is fine"}
	var out bytes.Buffer
	if err := runAgent(context.Background(), humanOutput(&out), nolog(), callsProbe(`{"a":1}`),
		registryWith(t, tool), asked("check"), nil, defaultMaxTurns, hooks.None()); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}
	if len(tool.calls) != 1 || tool.calls[0] != `{"a":1}` {
		t.Errorf("the tool was called with %v, want the model's own arguments", tool.calls)
	}
}

// hookList is what a verbose run shows: one line per hook, saying when it
// runs, which tools it watches, and what it runs.
func TestHookListNamesEachHook(t *testing.T) {
	armed := armWith(t, `{"hooks": {
	  "PreToolUse":  [{"matcher": "^bash$", "command": "./a.sh", "args": []}],
	  "PostToolUse": [{"command": "./b.sh", "args": []}]
	}}`, map[string]string{"a.sh": "#!/bin/sh\n", "b.sh": "#!/bin/sh\n"})

	list := hookList(armed)
	for _, want := range []string{"PreToolUse", "^bash$", "a.sh", "PostToolUse", "b.sh"} {
		if !strings.Contains(list, want) {
			t.Errorf("hookList() = %q, want it to mention %q", list, want)
		}
	}
	// A hook watching everything says so rather than showing nothing.
	if !strings.Contains(list, "*") {
		t.Errorf("hookList() = %q, want a matcher-less hook shown as watching every tool", list)
	}
	if hookList(hooks.None()) != "" {
		t.Error("hookList() of no hooks says something, want nothing")
	}
}

// contain stops a hook inside a fork or a subagent from ending the whole run,
// the way it already stops a termination.
func TestContainKeepsAHookStopInsideANestedAgent(t *testing.T) {
	_, err := contain("", &hooks.Stop{Hook: "./no.sh", Reason: "not from a subagent"})
	if err == nil {
		t.Fatal("contain() = nil, want the agent's failure passed back")
	}
	var stop *hooks.Stop
	if errors.As(err, &stop) {
		t.Error("the sentinel escaped contain(), which would end the whole run")
	}
	if !strings.Contains(err.Error(), "not from a subagent") {
		t.Errorf("contain() = %v, want the reason carried over as text", err)
	}
}

// Hooks and tools are two halves of one file, and each package spells the
// name for itself rather than importing the other for one string. Nothing
// but this holds the two spellings together, and cli is where it belongs
// because cli is what imports both.
//
// It goes when the --resources revamp does, along with the shared name.
func TestTheManifestHasOneName(t *testing.T) {
	if hooks.ManifestName != userdefined.ManifestName {
		t.Errorf("hooks read %q and tools read %q; a folder holding both would load as neither",
			hooks.ManifestName, userdefined.ManifestName)
	}
}

var _ tools.Tool = (*fakeTool)(nil)
