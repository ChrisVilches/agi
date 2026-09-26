package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// armed builds a set holding one hook, with the entry a test is about and a
// script beside it. The script is written into the folder, so a test reads as
// the pair of things a folder of hooks actually is.
func armed(t *testing.T, entry, body string) (*Set, *Hook) {
	t.Helper()
	dir := folder(t, `{"PreToolUse": [`+entry+`]}`)
	if body != "" {
		write(t, dir, "hook.sh", body)
	}
	s, err := Load([]string{dir}, Config{Timeout: 5 * time.Second, Session: "abc123"})
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	return s, s.All()[0]
}

// write puts an executable script in dir.
func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("writing the script: %v", err)
	}
	return path
}

// call is the tool call every test here guards.
func call() Call {
	return Call{
		Tool:      "bash",
		Arguments: json.RawMessage(`{"command":"git push --force"}`),
		RequestID: "call_01",
		Agent:     "main",
	}
}

// fire runs one hook and returns what it came to.
func fire(t *testing.T, s *Set, h *Hook) (step, error) {
	t.Helper()
	return s.run(context.Background(), h, PreToolUse, call(), nil)
}

// A hook that exits zero and says nothing lets the call through untouched.
func TestHookThatSaysNothing(t *testing.T) {
	s, h := armed(t, `{"command": "./hook.sh", "args": []}`, "#!/bin/sh\nexit 0\n")

	got, err := fire(t, s, h)
	if err != nil {
		t.Fatalf("run() = %v, want the call to pass", err)
	}
	if got.arguments != nil || got.result != nil || got.problem != "" {
		t.Errorf("run() = %+v, want nothing changed", got)
	}
}

// Exit 2 refuses the call, and standard error is the reason the model reads.
// This is the way a guard says no without printing any JSON at all.
func TestExitTwoRefusesTheCall(t *testing.T) {
	s, h := armed(t, `{"command": "./hook.sh", "args": []}`,
		"#!/bin/sh\necho 'force pushing is not allowed here' >&2\nexit 2\n")

	_, err := fire(t, s, h)
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("run() = %v, want a refusal", err)
	}
	if refusal.Reason != "force pushing is not allowed here" {
		t.Errorf("reason = %q, want what the hook said on standard error", refusal.Reason)
	}
}

// A hook that refuses and explains nothing still has to give the model
// something to act on, since a bare exit status is not a reason.
func TestRefusalWithNoWordsStillExplainsItself(t *testing.T) {
	s, h := armed(t, `{"command": "./hook.sh", "args": []}`, "#!/bin/sh\nexit 2\n")

	_, err := fire(t, s, h)
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("run() = %v, want a refusal", err)
	}
	if refusal.Reason == "" {
		t.Error("reason is empty, want a sentence agi wrote")
	}
}

// permissionDecision deny refuses the call from a hook that exited zero, and
// its own reason is preferred to anything on standard error.
func TestPermissionDecisionDeny(t *testing.T) {
	s, h := armed(t, `{"command": "./hook.sh", "args": []}`,
		`#!/bin/sh
echo 'noise' >&2
cat <<'JSON'
{"hookSpecificOutput": {"hookEventName": "PreToolUse",
 "permissionDecision": "deny", "permissionDecisionReason": "not on main"}}
JSON
`)

	_, err := fire(t, s, h)
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("run() = %v, want a refusal", err)
	}
	if refusal.Reason != "not on main" {
		t.Errorf("reason = %q, want the hook's own", refusal.Reason)
	}
}

// ask escalates to a person in Claude Code. agi has no permission prompt, so
// it is the safe half of what the hook asked for.
func TestPermissionDecisionAskIsTreatedAsDeny(t *testing.T) {
	s, h := armed(t, `{"command": "./hook.sh", "args": []}`,
		`#!/bin/sh
echo '{"hookSpecificOutput": {"permissionDecision": "ask", "permissionDecisionReason": "check with me"}}'
`)

	_, err := fire(t, s, h)
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("run() = %v, want ask to refuse the call", err)
	}
	if refusal.Reason != "check with me" {
		t.Errorf("reason = %q, want the hook's own", refusal.Reason)
	}
}

// allow and defer both let the call through.
func TestPermissionDecisionsThatProceed(t *testing.T) {
	for _, decision := range []string{decisionAllow, decisionDefer} {
		s, h := armed(t, `{"command": "./hook.sh", "args": []}`,
			`#!/bin/sh
echo '{"hookSpecificOutput": {"permissionDecision": "`+decision+`"}}'
`)
		if _, err := fire(t, s, h); err != nil {
			t.Errorf("%q = %v, want the call to pass", decision, err)
		}
	}
}

// continue: false stops the agent, and outranks everything else in a reply.
func TestContinueFalseStopsTheAgent(t *testing.T) {
	s, h := armed(t, `{"command": "./hook.sh", "args": []}`,
		`#!/bin/sh
echo '{"continue": false, "stopReason": "the build is broken"}'
`)

	_, err := fire(t, s, h)
	var stop *Stop
	if !errors.As(err, &stop) {
		t.Fatalf("run() = %v, want a stop", err)
	}
	if stop.Reason != "the build is broken" {
		t.Errorf("reason = %q, want stopReason", stop.Reason)
	}
}

// A program that is not executable stops the agent rather than being a
// warning, which is where agi parts company with Claude Code: a guard that
// is broken must never fail open.
func TestAProgramThatCannotBeRunStopsTheAgent(t *testing.T) {
	dir := folder(t, `{"PreToolUse": [{"command": "./hook.sh", "args": []}]}`)
	if err := os.WriteFile(filepath.Join(dir, "hook.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatalf("writing the script: %v", err)
	}
	s, err := Load([]string{dir}, Config{})
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	_, err = fire(t, s, s.All()[0])
	var stop *Stop
	if !errors.As(err, &stop) {
		t.Fatalf("run() = %v, want a stop", err)
	}
}

// A hook that outruns the timeout stops the agent, for the same reason.
func TestATimedOutHookStopsTheAgent(t *testing.T) {
	dir := folder(t, `{"PreToolUse": [{"command": "./hook.sh", "args": []}]}`)
	write(t, dir, "hook.sh", "#!/bin/sh\nsleep 5\n")
	s, err := Load([]string{dir}, Config{Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	_, err = fire(t, s, s.All()[0])
	var stop *Stop
	if !errors.As(err, &stop) {
		t.Fatalf("run() = %v, want a stop", err)
	}
	if !strings.Contains(stop.Reason, "longer than") {
		t.Errorf("reason = %q, want it to say the hook outran its time", stop.Reason)
	}
}

// The run being stopped underneath a hook is not the hook failing, and it
// must not be reported as one: there is no next turn to correct anything in.
func TestACancelledRunIsNotAHookFailing(t *testing.T) {
	s, h := armed(t, `{"command": "./hook.sh", "args": []}`, "#!/bin/sh\nsleep 5\n")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := s.run(ctx, h, PreToolUse, call(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("run() = %v, want the cancellation", err)
	}
}

// Any status that is not 0 or 2 is a hook that went wrong rather than a hook
// that decided something. Claude Code lets the call through and so does agi:
// a hook failing in a way nobody planned for must not become a silent veto.
func TestAnUnplannedStatusIsAWarning(t *testing.T) {
	s, h := armed(t, `{"command": "./hook.sh", "args": []}`,
		"#!/bin/sh\necho 'jq: command not found' >&2\nexit 127\n")

	got, err := fire(t, s, h)
	if err != nil {
		t.Fatalf("run() = %v, want the call to pass", err)
	}
	if !strings.Contains(got.problem, "127") || !strings.Contains(got.problem, "jq") {
		t.Errorf("problem = %q, want the status and what the hook said", got.problem)
	}
}

// Output that opens as an object and will not parse is a hook trying to say
// something and failing, which is a bug in the hook and not a decision.
func TestABrokenReplyIsAWarning(t *testing.T) {
	s, h := armed(t, `{"command": "./hook.sh", "args": []}`, "#!/bin/sh\necho '{oops}'\n")

	got, err := fire(t, s, h)
	if err != nil {
		t.Fatalf("run() = %v, want the call to pass", err)
	}
	if got.problem == "" {
		t.Error("problem is empty, want the user told the reply would not parse")
	}
}

// Output that is not JSON at all is a hook printing its own progress, and is
// not a problem with anything.
func TestPlainOutputIsNotAReply(t *testing.T) {
	s, h := armed(t, `{"command": "./hook.sh", "args": []}`, "#!/bin/sh\necho checking the command\n")

	got, err := fire(t, s, h)
	if err != nil {
		t.Fatalf("run() = %v, want the call to pass", err)
	}
	if got.problem != "" {
		t.Errorf("problem = %q, want plain output to be no problem", got.problem)
	}
}

// The hook reads one JSON object on standard input, under Claude Code's
// field names, so that a script written for that program finds what it
// expects.
func TestWhatTheHookIsTold(t *testing.T) {
	s, h := armed(t, `{"command": "./hook.sh", "args": []}`, "")

	var in map[string]any
	if err := json.Unmarshal([]byte(told(t, s, h)), &in); err != nil {
		t.Fatalf("what the hook read is not JSON: %v", err)
	}
	if in["hook_event_name"] != string(PreToolUse) {
		t.Errorf("hook_event_name = %v, want %q", in["hook_event_name"], PreToolUse)
	}
	if in["tool_name"] != "bash" {
		t.Errorf("tool_name = %v, want the tool", in["tool_name"])
	}
	if in["tool_use_id"] != "call_01" {
		t.Errorf("tool_use_id = %v, want the request id", in["tool_use_id"])
	}
	if in["session_id"] != "abc123" {
		t.Errorf("session_id = %v, want the run's identifier", in["session_id"])
	}
	if in["agent_id"] != "main" {
		t.Errorf("agent_id = %v, want the agent", in["agent_id"])
	}
	arguments, ok := in["tool_input"].(map[string]any)
	if !ok || arguments["command"] != "git push --force" {
		t.Errorf("tool_input = %v, want the arguments the call will be made with", in["tool_input"])
	}
	// The fields agi has no true twin for are left out rather than invented.
	for _, absent := range []string{"transcript_path", "scratchpad_dir", "permission_mode"} {
		if _, there := in[absent]; there {
			t.Errorf("%s was sent, want only fields agi really has", absent)
		}
	}
}

// told runs a hook that copies its input to a file, and returns what it read.
// A hook's standard output is the protocol's, so a test that wants the input
// verbatim has to put it somewhere else.
func told(t *testing.T, s *Set, h *Hook) string {
	t.Helper()
	path := filepath.Join(h.dir, "seen.json")
	write(t, h.dir, "hook.sh", "#!/bin/sh\ncat > "+path+"\n")
	if _, err := fire(t, s, h); err != nil {
		t.Fatalf("run() = %v", err)
	}
	read, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading what the hook was told: %v", err)
	}
	return string(read)
}

// A hook runs in the folder its manifest is in, which is what makes a
// relative path mean the same thing in a shell line as in the exec form.
func TestAHookRunsInItsOwnFolder(t *testing.T) {
	s, h := armed(t, `{"command": "./hook.sh", "args": []}`, "")
	write(t, h.dir, "hook.sh", "#!/bin/sh\npwd > "+filepath.Join(h.dir, "where")+"\n")

	if _, err := fire(t, s, h); err != nil {
		t.Fatalf("run() = %v", err)
	}
	where, err := os.ReadFile(filepath.Join(h.dir, "where"))
	if err != nil {
		t.Fatalf("reading where the hook ran: %v", err)
	}
	if got := strings.TrimSpace(string(where)); got != h.dir {
		t.Errorf("the hook ran in %q, want its manifest's folder %q", got, h.dir)
	}
}

// The shell form is a command line, so pipes work and a relative path in it
// resolves because of where the program runs.
func TestTheShellForm(t *testing.T) {
	dir := folder(t, `{"PreToolUse": [{"command": "./hook.sh | grep -q push"}]}`)
	write(t, dir, "hook.sh", "#!/bin/sh\njq -r .tool_input.command 2>/dev/null || cat\n")
	s, err := Load([]string{dir}, Config{})
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	if _, err := fire(t, s, s.All()[0]); err != nil {
		t.Fatalf("run() = %v, want the pipeline to succeed", err)
	}
}

// The exec form passes each argument as it was written, with no shell to
// reinterpret any of it.
func TestTheExecForm(t *testing.T) {
	dir := folder(t, `{"PreToolUse": [
	  {"command": "./hook.sh", "args": ["one two", "$HOME"]}
	]}`)
	write(t, dir, "hook.sh", "#!/bin/sh\nprintf '%s|%s' \"$1\" \"$2\" > "+filepath.Join(dir, "args")+"\n")
	s, err := Load([]string{dir}, Config{})
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	if _, err := fire(t, s, s.All()[0]); err != nil {
		t.Fatalf("run() = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatalf("reading the arguments: %v", err)
	}
	if want := "one two|$HOME"; string(got) != want {
		t.Errorf("arguments = %q, want %q", got, want)
	}
}

func TestIdentifyIsStableAndUnique(t *testing.T) {
	first := Identify("/home/chris/work/chat")
	again := Identify("/home/chris/work/chat/")
	other := Identify("/home/chris/play/chat")

	if first != again {
		t.Errorf("two spellings of one directory gave %q and %q, want one identifier", first, again)
	}
	if first == other {
		t.Error("two different directories share an identifier")
	}
	if len(first) != identifierLength {
		t.Errorf("identifier is %d characters, want %d", len(first), identifierLength)
	}
	if strings.ContainsRune(first, filepath.Separator) {
		t.Errorf("identifier %q has a separator in it and could not go in a filename", first)
	}
}

// The dangerous direction: two conversations started as --session ./chat from
// different folders must not share an identifier. A test that only checked
// one session's identifier is stable would pass without the path being
// resolved at all.
func TestIdentifyResolvesRelativePaths(t *testing.T) {
	root := t.TempDir()
	here, err := os.Getwd()
	if err != nil {
		t.Fatalf("reading the working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(here); err != nil {
			t.Fatalf("going back: %v", err)
		}
	})

	ids := make(map[string]string)
	for _, where := range []string{"work", "play"} {
		at := filepath.Join(root, where)
		if err := os.MkdirAll(filepath.Join(at, "chat"), 0o755); err != nil {
			t.Fatalf("making the session's folder: %v", err)
		}
		if err := os.Chdir(at); err != nil {
			t.Fatalf("going to %s: %v", at, err)
		}
		ids[where] = Identify("./chat")
	}

	if ids["work"] == ids["play"] {
		t.Errorf("--session ./chat in two folders gave one identifier %q, want two", ids["work"])
	}
}
