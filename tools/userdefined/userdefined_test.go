package userdefined

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fatih/color"
)

// tool writes a one-tool folder with the given script body and parameters, and
// returns the tool it declares.
func tool(t *testing.T, body, parameters string) *Tool {
	t.Helper()
	if parameters == "" {
		parameters = `{"type": "object", "properties": {"text": {"type": "string"}}}`
	}
	dir := folder(t, `[{
		"name": "probe",
		"path": "probe.sh",
		"description": "A tool used in a test.",
		"parameters": `+parameters+`
	}]`)
	script(t, dir, "probe.sh", body)

	found, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	return found[0]
}

func call(t *testing.T, tl *Tool, args string) (string, error) {
	t.Helper()
	return tl.Call(context.Background(), json.RawMessage(args))
}

// The arguments reach the script as one JSON argument, exactly as the model
// wrote them, which is what makes the parameters named rather than positional.
func TestCallPassesTheArgumentsAsJSON(t *testing.T) {
	tl := tool(t, echoArgs, `{"type": "object", "properties": {
		"city": {"type": "string"},
		"days": {"type": "integer"}
	}}`)

	got, err := call(t, tl, `{"city": "Lima", "days": 3}`)
	if err != nil {
		t.Fatalf("Call() = %v", err)
	}

	var back map[string]any
	if err := json.Unmarshal([]byte(got), &back); err != nil {
		t.Fatalf("the script was not given JSON: %q", got)
	}
	if back["city"] != "Lima" {
		t.Errorf("the script was given %v, want the arguments it was called with", back)
	}
}

// There is no shell between agi and the script, so nothing in an argument can
// be read as a command: it arrives as the text it is.
func TestCallRunsTheScriptWithoutAShell(t *testing.T) {
	tl := tool(t, echoArgs, "")

	const dangerous = "; touch /tmp/agi-should-not-exist; $(whoami) `id` *"
	args, err := json.Marshal(map[string]string{"text": dangerous})
	if err != nil {
		t.Fatalf("marshalling the arguments: %v", err)
	}

	got, err := call(t, tl, string(args))
	if err != nil {
		t.Fatalf("Call() = %v", err)
	}
	var back map[string]string
	if err := json.Unmarshal([]byte(got), &back); err != nil {
		t.Fatalf("the script was not given JSON: %q", got)
	}
	if back["text"] != dangerous {
		t.Errorf("the script was given %q, want the argument unchanged", back["text"])
	}
}

// A tool is configured the way agi is, by the environment it inherits.
func TestCallInheritsTheEnvironment(t *testing.T) {
	t.Setenv("AGI_TEST_TOKEN", "secret-value")
	tl := tool(t, "#!/bin/sh\nprintf '%s' \"$AGI_TEST_TOKEN\"\n", "")

	got, err := call(t, tl, `{}`)
	if err != nil {
		t.Fatalf("Call() = %v", err)
	}
	if got != "secret-value" {
		t.Errorf("Call() = %q, want the inherited variable", got)
	}
}

// A script that reads standard input finds end of file rather than waiting for
// input that is never coming.
func TestCallGivesTheScriptNoInput(t *testing.T) {
	tl := tool(t, "#!/bin/sh\ncat\necho 'done'\n", "")

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := call(t, tl, `{}`); err != nil {
			t.Errorf("Call() = %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Call() waited for input on standard input")
	}
}

// A script that ran and exited non-zero has not failed the call: it reported
// something, and the model reads it and can act on it. Failing the call instead
// would throw away whatever it managed to print and would run no PostToolUse
// hook, so a guard would never see a failing script at all.
func TestANonZeroExitIsAResultAndNotAFailure(t *testing.T) {
	tl := tool(t, "#!/bin/sh\necho 'partial output'\necho 'the city is unknown' >&2\nexit 1\n", "")

	got, err := call(t, tl, `{}`)
	if err != nil {
		t.Fatalf("Call() = %v, want the exit reported in the result", err)
	}
	for _, want := range []string{"partial output", "the city is unknown", "exit status 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("Call() = %q, want it to carry %q", got, want)
		}
	}
}

// Both streams reach the model, whatever the status. Standard error is not a
// failure — plenty of scripts log to it while succeeding — and discarding it
// loses the half of the output that usually says what went wrong.
func TestBothStreamsReachTheModel(t *testing.T) {
	tl := tool(t, "#!/bin/sh\necho 'the forecast is rain'\necho 'cache miss' >&2\n", "")

	got, err := call(t, tl, `{}`)
	if err != nil {
		t.Fatalf("Call() = %v", err)
	}
	for _, want := range []string{"the forecast is rain", "cache miss"} {
		if !strings.Contains(got, want) {
			t.Errorf("Call() = %q, want it to carry %q", got, want)
		}
	}
	if strings.Contains(got, "exit status") {
		t.Errorf("Call() = %q, want no status line for a script that succeeded", got)
	}
}

// A script that failed and explained nothing is the one case with nothing to
// show, and it still has to say it failed: nothing else in the text would,
// since a program can fail having printed not a byte.
func TestASilentFailureStillSaysItFailed(t *testing.T) {
	tl := tool(t, "#!/bin/sh\nexit 3\n", "")

	got, err := call(t, tl, `{}`)
	if err != nil {
		t.Fatalf("Call() = %v", err)
	}
	if want := "(no output)\nexit status 3"; got != want {
		t.Errorf("Call() = %q, want %q", got, want)
	}
}

// The arguments are checked before anything runs, so a call the tool would
// have refused never reaches it.
func TestCallChecksArgumentsBeforeRunning(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	tl := tool(t, "#!/bin/sh\ntouch "+marker+"\n",
		`{"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}`)

	_, err := call(t, tl, `{}`)
	if err == nil {
		t.Fatal("Call() = nil, want the missing parameter refused")
	}
	// The whole sentence, not a fragment of it: this is what the model reads
	// when a call is refused, so it is worth being unable to change by
	// accident. It names the tool, says which contract was broken, and then
	// says how.
	want := "probe: the arguments do not match the tool's parameters: city is required"
	if err.Error() != want {
		t.Errorf("Call() = %q, want %q", err, want)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("Call() ran the script despite arguments it would have refused")
	}
}

// The script is run directly, so the kernel needs a shebang to know what to
// run it with, and saying so is worth more than what the kernel says.
func TestCallExplainsAMissingShebang(t *testing.T) {
	tl := tool(t, "echo 'no shebang here'\n", "")

	_, err := call(t, tl, `{}`)
	if err == nil {
		t.Fatal("Call() = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "shebang") {
		t.Errorf("Call() = %v, want it to say what is missing", err)
	}
}

// Nothing in the protocol lets a script say it is still working, so a script
// that hangs is stopped rather than waited on — and what it printed before it
// was stopped is still worth reading, so it comes back under the reason.
func TestCallHonoursTheTimeout(t *testing.T) {
	tl := tool(t, "#!/bin/sh\necho 'got this far'\nsleep 30\n", "")
	tl.Timeout = 100 * time.Millisecond

	started := time.Now()
	got, err := call(t, tl, `{}`)
	if err != nil {
		t.Fatalf("Call() = %v, want the timeout reported in the result", err)
	}
	for _, want := range []string{"got this far", "timed out"} {
		if !strings.Contains(got, want) {
			t.Errorf("Call() = %q, want it to carry %q", got, want)
		}
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Errorf("Call() took %s, want it stopped at the timeout", elapsed)
	}
}

// A run the user stopped is not a tool that refused the call, and there is no
// next turn to correct anything in.
func TestCallReportsCancellationRatherThanAFailure(t *testing.T) {
	tl := tool(t, "#!/bin/sh\nsleep 30\n", "")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := tl.Call(ctx, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("Call() = nil, want the cancellation")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("Call() = %v, want the cancellation rather than an exit status", err)
	}
}

// Output that would fill the context window is cut, and the cut is visible.
func TestCallCutsOversizedOutput(t *testing.T) {
	tl := tool(t, "#!/bin/sh\nhead -c 100000 /dev/zero | tr '\\0' 'x'\n", "")

	got, err := call(t, tl, `{}`)
	if err != nil {
		t.Fatalf("Call() = %v", err)
	}
	if len(got) > maxOutput+200 {
		t.Errorf("Call() returned %d bytes, want it capped near %d", len(got), maxOutput)
	}
	if !strings.Contains(got, "bytes omitted") {
		t.Error("Call() did not say that output was omitted")
	}
}

// A user-defined tool has no explanation parameter, so what is shown as a call
// happens is the call itself.
func TestAnnounceShowsTheArguments(t *testing.T) {
	previous := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = previous })

	tl := tool(t, echoArgs, "")

	if got := tl.Announce(json.RawMessage("{\n \"text\": \"hello\"\n}")); got != `{"text":"hello"}` {
		t.Errorf("Announce() = %q, want the arguments on one line", got)
	}
	// A call with nothing in it has nothing to show beyond the tool's name,
	// which the registry adds.
	for _, empty := range []string{``, `{}`} {
		if got := tl.Announce(json.RawMessage(empty)); got != "" {
			t.Errorf("Announce(%q) = %q, want nothing", empty, got)
		}
	}
	// Display must not fail on arguments that never parsed.
	if got := tl.Announce(json.RawMessage(`{"text":`)); got == "" {
		t.Error("Announce() of unparsable arguments = empty, want a best effort")
	}
	long := `{"text": "` + strings.Repeat("y", maxAnnounced*2) + `"}`
	if got := tl.Announce(json.RawMessage(long)); len(got) > maxAnnounced+10 {
		t.Errorf("Announce() showed %d characters, want it kept to a line", len(got))
	}
}

// A cancelled call takes what the script started with it.
//
// Killing a script does not kill what that script started, and running other
// programs is most of what somebody writes a tool for. Those children would
// go on working for a run that has been stopped, which is what
// tools.KillGroupOnCancel is called here to prevent.
//
// The grandchild leaves its trace after a delay, so a missing marker means it
// was killed rather than that the test looked too early.
func TestACancelledCallTakesTheScriptsChildrenWithIt(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survived")
	tl := tool(t, "#!/bin/sh\n(sleep 2; touch "+marker+") &\nwait\n", "")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Nothing here touches t, so this goroutine outliving the test is
		// harmless.
		_, _ = tl.Call(ctx, json.RawMessage(`{"text": "x"}`))
	}()

	// Long enough for the script to have started its child, which is the
	// whole situation under test.
	time.Sleep(200 * time.Millisecond)
	cancel()
	// Well past the grandchild's delay, so it has had every chance to finish.
	time.Sleep(3 * time.Second)

	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the grandchild left its mark (%v), so it outlived the run that was "+
			"stopped; a script's children have to go with it", err)
	}
}
