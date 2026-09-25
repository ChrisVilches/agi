package userdefined

import (
	"context"
	"encoding/json"
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
	dir := folder(t, `{"tools": [{
		"name": "probe",
		"path": "probe.sh",
		"description": "A tool used in a test.",
		"parameters": `+parameters+`
	}]}`)
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

// Standard output is the answer when the script succeeds, and standard error
// is not: a script may log to it.
func TestCallReturnsStandardOutputOnSuccess(t *testing.T) {
	tl := tool(t, "#!/bin/sh\necho 'noise' >&2\necho 'it is sunny'\n", "")

	got, err := call(t, tl, `{"text": "x"}`)
	if err != nil {
		t.Fatalf("Call() = %v", err)
	}
	if got != "it is sunny" {
		t.Errorf("Call() = %q, want the script's standard output", got)
	}
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

// A non-zero exit is the script saying the call failed, and standard error is
// what it says about it.
func TestCallFailsWithStandardError(t *testing.T) {
	tl := tool(t, "#!/bin/sh\necho 'partial output'\necho 'the city is unknown' >&2\nexit 1\n", "")

	got, err := call(t, tl, `{}`)
	if err == nil {
		t.Fatalf("Call() = %q, want a failure", got)
	}
	if !strings.Contains(err.Error(), "the city is unknown") {
		t.Errorf("Call() = %v, want the script's standard error", err)
	}
	if got != "" {
		t.Errorf("Call() = %q, want no result alongside a failure", got)
	}
	// The tool says which tool it was, since the model called several.
	if !strings.HasPrefix(err.Error(), "probe: ") {
		t.Errorf("Call() = %v, want the tool named", err)
	}
}

// Standard output stands in when a failing script said nothing on standard
// error, which is the commonest way a script written in a hurry reports a
// problem.
func TestCallFallsBackToStandardOutputOnFailure(t *testing.T) {
	tl := tool(t, "#!/bin/sh\necho 'the city is unknown'\nexit 2\n", "")

	if _, err := call(t, tl, `{}`); err == nil || !strings.Contains(err.Error(), "the city is unknown") {
		t.Errorf("Call() = %v, want the script's standard output", err)
	}
}

// A script that failed and explained nothing is the one case with nothing to
// pass on, and the exit status stands in for the explanation it owed.
func TestCallReportsASilentFailure(t *testing.T) {
	tl := tool(t, "#!/bin/sh\nexit 3\n", "")

	_, err := call(t, tl, `{}`)
	if err == nil {
		t.Fatal("Call() = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("Call() = %v, want the exit status where an explanation should have been", err)
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
	if !strings.Contains(err.Error(), "city is required") {
		t.Errorf("Call() = %v, want the parameter named", err)
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
// that hangs is stopped rather than waited on.
func TestCallHonoursTheTimeout(t *testing.T) {
	tl := tool(t, "#!/bin/sh\nsleep 30\n", "")
	tl.Timeout = 100 * time.Millisecond

	started := time.Now()
	_, err := call(t, tl, `{}`)
	if err == nil {
		t.Fatal("Call() = nil, want a timeout")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("Call() = %v, want it to say the call timed out", err)
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
