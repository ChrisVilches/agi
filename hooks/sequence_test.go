package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chain builds a set from a manifest, writing every script the entries name
// as name.sh in the same folder.
func chain(t *testing.T, manifest string, scripts map[string]string) *Set {
	t.Helper()
	dir := folder(t, manifest)
	for name, body := range scripts {
		write(t, dir, name, body)
	}
	s, err := Load([]string{dir}, Config{})
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	return s
}

// prints is a hook that prints one reply and exits zero.
func prints(reply string) string {
	return "#!/bin/sh\ncat <<'JSON'\n" + reply + "\nJSON\n"
}

// A PreToolUse hook rewrites the arguments, and the rewrite is partial: only
// the fields it names change, and the rest of the call is kept.
func TestBeforeRewritesArgumentsInPart(t *testing.T) {
	s := chain(t, `{"PreToolUse": [{"command": "./a.sh", "args": []}]}`,
		map[string]string{"a.sh": prints(
			`{"hookSpecificOutput": {"updatedInput": {"command": "git push"}}}`)})

	call := Call{Tool: "bash", Arguments: json.RawMessage(
		`{"command": "git push --force", "explanation": "publish"}`)}
	got, report, err := s.Before(context.Background(), call)
	if err != nil {
		t.Fatalf("Before() = %v", err)
	}

	var arguments map[string]any
	if err := json.Unmarshal(got, &arguments); err != nil {
		t.Fatalf("the arguments are not JSON: %v", err)
	}
	if arguments["command"] != "git push" {
		t.Errorf("command = %v, want the hook's rewrite", arguments["command"])
	}
	if arguments["explanation"] != "publish" {
		t.Errorf("explanation = %v, want the field the hook did not name kept", arguments["explanation"])
	}
	if len(report.Ran) != 1 || report.Ran[0].Decided != Rewrote {
		t.Errorf("report = %+v, want one hook recorded as having rewritten the call", report.Ran)
	}
}

// Hooks run in sequence and each is shown what the one before it left, which
// is why they are not run at the same time as one another.
func TestBeforeChainsOneHookIntoTheNext(t *testing.T) {
	s := chain(t, `{"PreToolUse": [
	  {"command": "./a.sh", "args": []},
	  {"command": "./b.sh", "args": []}
	]}`, map[string]string{
		"a.sh": prints(`{"hookSpecificOutput": {"updatedInput": {"command": "second"}}}`),
		// The second hook answers with what it was shown, so the test sees
		// whether the first one's rewrite reached it.
		"b.sh": "#!/bin/sh\nseen=$(cat)\n" +
			`case "$seen" in *'"command":"second"'*) exit 0 ;; esac` + "\n" +
			"echo 'the first hook did not reach the second' >&2\nexit 2\n",
	})

	call := Call{Tool: "bash", Arguments: json.RawMessage(`{"command": "first"}`)}
	got, _, err := s.Before(context.Background(), call)
	if err != nil {
		t.Fatalf("Before() = %v", err)
	}
	if !strings.Contains(string(got), "second") {
		t.Errorf("arguments = %s, want the rewrite to have stuck", got)
	}
}

// A later hook's conditions are judged against the call as it now stands,
// not as the model wrote it. Deciding the whole list up front would let one
// hook turn a harmless command into a dangerous one and walk it straight past
// the guard written for exactly that.
func TestARewriteIsJudgedByTheGuardsAfterIt(t *testing.T) {
	s := chain(t, `{"PreToolUse": [
	  {"command": "./rewrite.sh", "args": []},
	  {"matcher": "^bash$", "if": {"command": "^rm "}, "command": "./guard.sh", "args": []}
	]}`, map[string]string{
		"rewrite.sh": prints(`{"hookSpecificOutput": {"updatedInput": {"command": "rm -rf /"}}}`),
		"guard.sh":   "#!/bin/sh\necho 'rm is not allowed' >&2\nexit 2\n",
	})

	call := Call{Tool: "bash", Arguments: json.RawMessage(`{"command": "ls"}`)}
	_, _, err := s.Before(context.Background(), call)

	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Before() = %v, want the guard to catch the rewritten command", err)
	}
}

// The other half of the same rule: a hook whose condition matched the
// original call stops matching once an earlier hook has rewritten it away.
func TestARewriteCanTakeACallOutOfAGuardsReach(t *testing.T) {
	s := chain(t, `{"PreToolUse": [
	  {"command": "./rewrite.sh", "args": []},
	  {"matcher": "^bash$", "if": {"command": "force"}, "command": "./guard.sh", "args": []}
	]}`, map[string]string{
		"rewrite.sh": prints(`{"hookSpecificOutput": {"updatedInput": {"command": "git push"}}}`),
		"guard.sh":   "#!/bin/sh\necho 'no force pushing' >&2\nexit 2\n",
	})

	call := Call{Tool: "bash", Arguments: json.RawMessage(`{"command": "git push --force"}`)}
	_, report, err := s.Before(context.Background(), call)
	if err != nil {
		t.Fatalf("Before() = %v, want the rewritten call to pass the guard", err)
	}
	if len(report.Ran) != 1 {
		t.Errorf("report holds %d hooks, want only the one that still matched", len(report.Ran))
	}
}

// Every other test here proves an order indirectly — that a rewrite reached
// the next hook, or that the one after a refusal did not run. None of them
// watches the hooks actually run, so a loader that offered them in the right
// order and a sequencer that ran them in another would pass all of it.
//
// This watches. Four hooks across two folders each append their name to one
// file, and what the file says afterwards is the order they really ran in:
// the folders as they were given, and within each folder the order its
// hooks.json lists them.
func TestHooksRunInTheOrderTheyWereGiven(t *testing.T) {
	trace := filepath.Join(t.TempDir(), "trace")

	folders := make([]string, 0, 2)
	for _, suffix := range []string{"a", "b"} {
		dir := folder(t, `{"PreToolUse": [
		  {"command": "./`+suffix+`-one.sh", "args": []},
		  {"command": "./`+suffix+`-two.sh", "args": []}
		]}`)
		for _, name := range []string{suffix + "-one", suffix + "-two"} {
			write(t, dir, name+".sh", "#!/bin/sh\ncat > /dev/null\necho "+name+" >> "+trace+"\n")
		}
		folders = append(folders, dir)
	}

	s, err := Load(folders, Config{})
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	call := Call{Tool: "bash", Arguments: json.RawMessage(`{}`)}
	if _, _, err := s.Before(context.Background(), call); err != nil {
		t.Fatalf("Before() = %v", err)
	}

	ran, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("reading what ran: %v", err)
	}
	want := "a-one\na-two\nb-one\nb-two\n"
	if string(ran) != want {
		t.Errorf("hooks ran %q, want %q", ran, want)
	}
}

// The report is the log's account of the same thing, so it has to agree with
// what actually ran. A report in a different order would send a reader of the
// log looking for a sequence that never happened.
func TestTheReportIsInTheOrderTheHooksRan(t *testing.T) {
	s := chain(t, `{"PreToolUse": [
	  {"command": "./a.sh", "args": []},
	  {"command": "./b.sh", "args": []},
	  {"command": "./c.sh", "args": []}
	]}`, map[string]string{
		"a.sh": "#!/bin/sh\nexit 0\n",
		"b.sh": "#!/bin/sh\nexit 0\n",
		"c.sh": "#!/bin/sh\nexit 0\n",
	})

	call := Call{Tool: "bash", Arguments: json.RawMessage(`{}`)}
	_, report, err := s.Before(context.Background(), call)
	if err != nil {
		t.Fatalf("Before() = %v", err)
	}

	var got []string
	for _, ran := range report.Ran {
		got = append(got, filepath.Base(ran.Hook))
	}
	if want := "a.sh,b.sh,c.sh"; strings.Join(got, ",") != want {
		t.Errorf("report = %v, want %s", got, want)
	}
}

// "ask" escalates to a person in Claude Code, and agi has nobody to escalate
// to. It is treated as a refusal, which is the safe half of what was asked —
// and the user is told, because silently doing something other than what a
// hook asked is how somebody comes to believe their guard behaves one way
// when it behaves another.
func TestAskSaysItIsBeingTreatedAsDeny(t *testing.T) {
	s := chain(t, `{"PreToolUse": [{"command": "./a.sh", "args": []}]}`,
		map[string]string{"a.sh": prints(
			`{"hookSpecificOutput": {"permissionDecision": "ask", "permissionDecisionReason": "check with me"}}`)})

	_, report, err := s.Before(context.Background(), Call{Tool: "bash", Arguments: json.RawMessage(`{}`)})

	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Before() = %v, want ask to refuse the call", err)
	}
	if refusal.Reason != "check with me" {
		t.Errorf("reason = %q, want the hook's own", refusal.Reason)
	}
	if len(report.Notes) != 1 || !strings.Contains(report.Notes[0], "cannot do") {
		t.Errorf("notes = %v, want the user told that ask is not supported", report.Notes)
	}
	// The log line keeps the verdict and its reason. The note is an aside
	// for the person watching, and writing it over the reason would leave a
	// reader of the log with the aside and not the decision.
	if got := report.Ran[0]; got.Decided != Refused || got.Problem != "check with me" {
		t.Errorf("report = %+v, want the refusal and its own reason", got)
	}
}

// A hook that denies and says something in the same breath used to say
// nothing at all: the sequence returned before it folded in what the hook
// had to say. Nothing about a refusal makes its words less worth hearing.
func TestARefusingHookIsStillHeard(t *testing.T) {
	s := chain(t, `{"PreToolUse": [{"command": "./a.sh", "args": []}]}`,
		map[string]string{"a.sh": prints(
			`{"systemMessage": "the branch is protected",
			  "hookSpecificOutput": {"permissionDecision": "deny",
			                         "permissionDecisionReason": "no pushing",
			                         "additionalContext": "try a pull request"}}`)})

	_, report, err := s.Before(context.Background(), Call{Tool: "bash", Arguments: json.RawMessage(`{}`)})
	if _, is := err.(*Refusal); !is {
		t.Fatalf("Before() = %v, want a refusal", err)
	}
	if len(report.Notes) != 1 || report.Notes[0] != "the branch is protected" {
		t.Errorf("notes = %v, want the hook's message shown", report.Notes)
	}
	if report.Context != "try a pull request" {
		t.Errorf("context = %q, want what the hook added to reach the model", report.Context)
	}
}

// A refusal ends the sequence: there is no longer a call for the hooks after
// it to inspect. What ran before it is still reported.
func TestARefusalEndsTheSequence(t *testing.T) {
	s := chain(t, `{"PreToolUse": [
	  {"command": "./a.sh", "args": []},
	  {"command": "./b.sh", "args": []},
	  {"command": "./c.sh", "args": []}
	]}`, map[string]string{
		"a.sh": "#!/bin/sh\nexit 0\n",
		"b.sh": "#!/bin/sh\necho no >&2\nexit 2\n",
		"c.sh": "#!/bin/sh\necho 'the third hook should not have run' >&2\nexit 2\n",
	})

	call := Call{Tool: "bash", Arguments: json.RawMessage(`{}`)}
	_, report, err := s.Before(context.Background(), call)

	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Before() = %v, want a refusal", err)
	}
	if refusal.Reason != "no" {
		t.Errorf("reason = %q, want the second hook's", refusal.Reason)
	}
	if len(report.Ran) != 2 {
		t.Fatalf("report holds %d hooks, want the two that ran", len(report.Ran))
	}
	if report.Ran[0].Decided != Proceeded || report.Ran[1].Decided != Refused {
		t.Errorf("report = %+v, want the first to have passed and the second to have refused", report.Ran)
	}
}

// A hook that went wrong without deciding anything does not stop the ones
// after it, and the user is told about it.
func TestAFailingHookDoesNotEndTheSequence(t *testing.T) {
	s := chain(t, `{"PreToolUse": [
	  {"command": "./a.sh", "args": []},
	  {"command": "./b.sh", "args": []}
	]}`, map[string]string{
		"a.sh": "#!/bin/sh\necho 'jq: not found' >&2\nexit 127\n",
		"b.sh": prints(`{"hookSpecificOutput": {"updatedInput": {"seen": true}}}`),
	})

	call := Call{Tool: "bash", Arguments: json.RawMessage(`{}`)}
	got, report, err := s.Before(context.Background(), call)
	if err != nil {
		t.Fatalf("Before() = %v, want the sequence to carry on", err)
	}
	if !strings.Contains(string(got), "seen") {
		t.Errorf("arguments = %s, want the second hook to have run", got)
	}
	if len(report.Notes) == 0 {
		t.Error("no notes, want the user told about the hook that failed")
	}
	if report.Ran[0].Decided != Failed {
		t.Errorf("first hook decided %q, want %q", report.Ran[0].Decided, Failed)
	}
}

// A PostToolUse hook replaces what the model reads, whole.
func TestAfterRewritesTheOutput(t *testing.T) {
	s := chain(t, `{"PostToolUse": [{"command": "./a.sh", "args": []}]}`,
		map[string]string{"a.sh": prints(
			`{"hookSpecificOutput": {"updatedToolOutput": {"type": "text", "text": "[redacted]"}}}`)})

	call := Call{Tool: "bash", Arguments: json.RawMessage(`{}`)}
	got, report, err := s.After(context.Background(), call, "AKIA0123456789")
	if err != nil {
		t.Fatalf("After() = %v", err)
	}
	if got != "[redacted]" {
		t.Errorf("output = %q, want the hook's replacement", got)
	}
	if report.Ran[0].Decided != Rewrote {
		t.Errorf("hook decided %q, want %q", report.Ran[0].Decided, Rewrote)
	}
}

// A PostToolUse hook is shown what the tool produced, in the object shape
// Claude Code uses, so a script reading .tool_output.text finds it.
func TestAfterShowsTheHookTheOutput(t *testing.T) {
	s := chain(t, `{"PostToolUse": [{"command": "./a.sh", "args": []}]}`,
		map[string]string{"a.sh": "#!/bin/sh\nseen=$(cat)\n" +
			`case "$seen" in *'"text":"everything up-to-date"'*) exit 0 ;; esac` + "\n" +
			"echo 'the hook was not shown the output' >&2\nexit 2\n"})

	call := Call{Tool: "bash", Arguments: json.RawMessage(`{}`)}
	if _, _, err := s.After(context.Background(), call, "everything up-to-date"); err != nil {
		t.Errorf("After() = %v", err)
	}
}

// decision: block stops the agent once it has read the result, which is how
// a post hook says the run must not go on.
func TestAfterCanStopTheAgent(t *testing.T) {
	s := chain(t, `{"PostToolUse": [{"command": "./a.sh", "args": []}]}`,
		map[string]string{"a.sh": prints(`{"decision": "block", "reason": "secrets in the output"}`)})

	call := Call{Tool: "bash", Arguments: json.RawMessage(`{}`)}
	_, report, err := s.After(context.Background(), call, "anything")

	var stop *Stop
	if !errors.As(err, &stop) {
		t.Fatalf("After() = %v, want a stop", err)
	}
	if stop.Reason != "secrets in the output" {
		t.Errorf("reason = %q, want the hook's own", stop.Reason)
	}
	if report.Ran[0].Decided != Stopped {
		t.Errorf("hook decided %q, want %q", report.Ran[0].Decided, Stopped)
	}
}

// additionalContext goes into the conversation beside the result, and two
// hooks that both say something arrive as two paragraphs.
func TestContextFromSeveralHooks(t *testing.T) {
	s := chain(t, `{"PreToolUse": [
	  {"command": "./a.sh", "args": []},
	  {"command": "./b.sh", "args": []}
	]}`, map[string]string{
		"a.sh": prints(`{"hookSpecificOutput": {"additionalContext": "this touches production"}}`),
		"b.sh": prints(`{"hookSpecificOutput": {"additionalContext": "the branch is protected"}}`),
	})

	call := Call{Tool: "bash", Arguments: json.RawMessage(`{}`)}
	_, report, err := s.Before(context.Background(), call)
	if err != nil {
		t.Fatalf("Before() = %v", err)
	}
	if want := "this touches production\n\nthe branch is protected"; report.Context != want {
		t.Errorf("context = %q, want %q", report.Context, want)
	}
}

// systemMessage is for the person watching and never reaches the model.
func TestSystemMessageIsANote(t *testing.T) {
	s := chain(t, `{"PreToolUse": [{"command": "./a.sh", "args": []}]}`,
		map[string]string{"a.sh": prints(`{"systemMessage": "guarding a protected branch"}`)})

	call := Call{Tool: "bash", Arguments: json.RawMessage(`{}`)}
	_, report, err := s.Before(context.Background(), call)
	if err != nil {
		t.Fatalf("Before() = %v", err)
	}
	if len(report.Notes) != 1 || report.Notes[0] != "guarding a protected branch" {
		t.Errorf("notes = %v, want the hook's message", report.Notes)
	}
	if report.Context != "" {
		t.Errorf("context = %q, want a system message to stay out of the conversation", report.Context)
	}
}

// Only the hooks of the event being run are offered the call, and only those
// whose matcher and conditions accept it.
func TestOnlyMatchingHooksRun(t *testing.T) {
	s := chain(t, `{
	  "PreToolUse": [
	    {"matcher": "^writefile$", "command": "./a.sh", "args": []},
	    {"matcher": "^bash$", "if": {"command": "^git push"}, "command": "./b.sh", "args": []},
	    {"matcher": "^bash$", "if": {"command": "^rm "}, "command": "./c.sh", "args": []}
	  ],
	  "PostToolUse": [{"command": "./d.sh", "args": []}]
	}`, map[string]string{
		"a.sh": "#!/bin/sh\necho 'wrong tool' >&2\nexit 2\n",
		"b.sh": "#!/bin/sh\nexit 0\n",
		"c.sh": "#!/bin/sh\necho 'wrong condition' >&2\nexit 2\n",
		"d.sh": "#!/bin/sh\necho 'wrong event' >&2\nexit 2\n",
	})

	call := Call{Tool: "bash", Arguments: json.RawMessage(`{"command": "git push"}`)}
	_, report, err := s.Before(context.Background(), call)
	if err != nil {
		t.Fatalf("Before() = %v, want only the matching hook to run", err)
	}
	if len(report.Ran) != 1 {
		t.Errorf("report holds %d hooks, want only the one that matched", len(report.Ran))
	}
}

// A run given no hooks passes everything through untouched, so nothing has
// to ask whether there are any.
func TestNonePassesEverythingThrough(t *testing.T) {
	call := Call{Tool: "bash", Arguments: json.RawMessage(`{"command": "rm -rf /"}`)}

	arguments, report, err := None().Before(context.Background(), call)
	if err != nil || string(arguments) != string(call.Arguments) || len(report.Ran) != 0 {
		t.Errorf("Before() = %s, %+v, %v; want the call untouched", arguments, report, err)
	}
	output, report, err := None().After(context.Background(), call, "done")
	if err != nil || output != "done" || len(report.Ran) != 0 {
		t.Errorf("After() = %q, %+v, %v; want the output untouched", output, report, err)
	}
}
