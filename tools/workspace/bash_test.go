package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fatih/color"

	"github.com/ChrisVilches/agi/tools"
)

func args(t *testing.T, command, explanation string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(BashParams{Command: command, Explanation: explanation})
	if err != nil {
		t.Fatalf("marshalling args: %v", err)
	}
	return b
}

func TestBashIsATool(t *testing.T) {
	var _ tools.Tool = NewBash()
	var _ tools.Announcer = NewBash()
}

// Workspace tools must never explain themselves in the system prompt; a run
// may not include them at all.
func TestBashIsNotASystemPrompter(t *testing.T) {
	if _, ok := any(NewBash()).(tools.SystemPrompter); ok {
		t.Error("bash implements SystemPrompter, but only native tools may")
	}
}

// The description is all the model is told, so it has to carry the usage rules.
func TestDefinitionRequiresBothParameters(t *testing.T) {
	def := NewBash().Definition()
	if def.Name != "bash" {
		t.Errorf("name = %q, want %q", def.Name, "bash")
	}
	if len(strings.TrimSpace(def.Description)) < 100 {
		t.Error("description is too thin to be the only thing the model is told")
	}

	required, ok := def.Parameters["required"].([]string)
	if !ok {
		t.Fatalf("required = %T, want []string", def.Parameters["required"])
	}
	if strings.Join(required, ",") != "command,explanation" {
		t.Errorf("required = %v, want both command and explanation", required)
	}

	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %T, want a map", def.Parameters["properties"])
	}
	for _, name := range []string{"command", "explanation"} {
		if _, ok := props[name]; !ok {
			t.Errorf("properties has no %q", name)
		}
	}
}

func TestCallRunsCommand(t *testing.T) {
	out, err := NewBash().Call(context.Background(), args(t, "echo hello", "Say hello."))
	if err != nil {
		t.Fatalf("Call() = %v, want nil", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("Call() = %q, want the command output", out)
	}
}

func TestCallCombinesStderr(t *testing.T) {
	out, err := NewBash().Call(context.Background(), args(t, "echo oops >&2", "Write to stderr."))
	if err != nil {
		t.Fatalf("Call() = %v, want nil", err)
	}
	if !strings.Contains(out, "oops") {
		t.Errorf("Call() = %q, want stderr included", out)
	}
}

// A failing command is information for the model, not a harness error.
func TestCallReportsExitStatusWithoutErroring(t *testing.T) {
	out, err := NewBash().Call(context.Background(), args(t, "echo bad; exit 3", "Fail on purpose."))
	if err != nil {
		t.Fatalf("Call() = %v, want nil so the model can read the failure", err)
	}
	if !strings.Contains(out, "exit status 3") {
		t.Errorf("Call() = %q, want the exit status reported", out)
	}
	if !strings.Contains(out, "bad") {
		t.Errorf("Call() = %q, want the output kept alongside the status", out)
	}
}

func TestCallRejectsMissingParameters(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		wantErr string
	}{
		{"no command", `{"explanation":"why"}`, "command parameter is required"},
		{"blank command", `{"command":"  ","explanation":"why"}`, "command parameter is required"},
		{"no explanation", `{"command":"echo hi"}`, "explanation parameter is required"},
		{"blank explanation", `{"command":"echo hi","explanation":" "}`, "explanation parameter is required"},
		{"not json", `not json`, "not valid JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewBash().Call(context.Background(), json.RawMessage(tt.args))
			if err == nil {
				t.Fatalf("Call(%s) = nil error, want an error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Call(%s) error = %v, want it to contain %q", tt.args, err, tt.wantErr)
			}
		})
	}
}

func TestCallHonoursTimeout(t *testing.T) {
	b := NewBash()
	b.Timeout = 100 * time.Millisecond

	out, err := b.Call(context.Background(), args(t, "sleep 5", "Sleep too long."))
	if err != nil {
		t.Fatalf("Call() = %v, want nil", err)
	}
	if !strings.Contains(out, "timed out") {
		t.Errorf("Call() = %q, want a timeout report", out)
	}
}

// A command killed because the run itself was stopped did not fail: it never
// got to finish. Saying so lets the run answer the call as one that could not
// be made, rather than handing the model an exit status that describes a
// signal it knows nothing about.
func TestCallReportsCancellationRatherThanAnExitStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	out, err := NewBash().Call(ctx, args(t, "sleep 5", "Sleep until the run is stopped."))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Call() = %q, %v, want the cancellation reported", out, err)
	}
	if out != "" {
		t.Errorf("Call() = %q, want nothing: the command has no result", out)
	}
}

// Stopping the run stops the command even when the command left something
// running behind it, which is the case the test above does not reach.
//
// Killing a process does not kill what that process started. A command line
// holding more than one command leaves the shell alive as a parent, so killing
// the shell orphans the children rather than ending them, and those orphans
// inherit the pipe this package reads. A read that waits for end of file would
// then be waiting for the orphan and not for the run: Ctrl-C would leave agi
// sitting there until the orphan happened to finish, and the second Ctrl-C
// would kill it outright and lose the snapshot. waitDelay is the bound that
// stops that.
//
// The numbers are what make this able to fail. The command outlasts the window
// several times over, so a Call that returns inside it returned because the
// run was stopped and not because the command finished; and the window is
// several times waitDelay, so a slow machine does not fail it.
func TestCallStopsWhenCancelledEvenIfTheCommandLeftSomethingRunning(t *testing.T) {
	call := args(t, "sleep 15 & echo started; wait", "Start something that outlives the shell.")

	ctx, cancel := context.WithCancel(context.Background())
	type answer struct {
		out string
		err error
	}
	returned := make(chan answer, 1)
	go func() {
		// Nothing here touches t, so this goroutine outliving the test is
		// harmless: it writes to a buffered channel and stops.
		out, err := NewBash().Call(ctx, call)
		returned <- answer{out, err}
	}()

	// Long enough for the shell to have started its child, which is the whole
	// situation under test.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case got := <-returned:
		if !errors.Is(got.err, context.Canceled) {
			t.Errorf("Call() = %q, %v, want the cancellation reported", got.out, got.err)
		}
		if got.out != "" {
			t.Errorf("Call() = %q, want nothing: the command has no result", got.out)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Call() did not return within six seconds of the run being stopped: it is " +
			"waiting on output the run is no longer waiting for, and Ctrl-C cannot end a " +
			"run that does this")
	}
}

func TestCallRunsInConfiguredDir(t *testing.T) {
	dir := t.TempDir()
	b := NewBash()
	b.Dir = dir

	out, err := b.Call(context.Background(), args(t, "pwd", "Show the working directory."))
	if err != nil {
		t.Fatalf("Call() = %v, want nil", err)
	}
	if !strings.Contains(out, strings.TrimPrefix(dir, "/private")) {
		t.Errorf("Call() = %q, want it to run in %q", out, dir)
	}
}

// The explanation exists so the user can see why a command is running. The
// tool name is not included here: the registry adds the [bash] label.
func TestAnnounceShowsExplanationAndCommand(t *testing.T) {
	got := NewBash().Announce(args(t, "df -h", "Check free disk space."))
	if !strings.HasPrefix(got, "Check free disk space.") {
		t.Errorf("Announce() = %q, want it to lead with the explanation", got)
	}
	if !strings.Contains(got, "$ df -h") {
		t.Errorf("Announce() = %q, want the command", got)
	}
	if strings.Contains(got, "[bash]") {
		t.Errorf("Announce() = %q, want the label left to the registry", got)
	}
}

// The command line is dimmed so the reason reads first.
func TestAnnounceDimsTheCommand(t *testing.T) {
	previous := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = previous })

	got := NewBash().Announce(args(t, "df -h", "Check free disk space."))
	reason, command, found := strings.Cut(got, "\n")
	if !found {
		t.Fatalf("Announce() = %q, want the command on its own line", got)
	}
	if strings.Contains(reason, "\x1b[") {
		t.Errorf("explanation = %q, want it left uncolored", reason)
	}
	if !strings.Contains(command, "\x1b[90m") {
		t.Errorf("command line = %q, want it gray", command)
	}
}

// A command is shown in full: it is what is about to happen to the user's
// machine, and the line that matters is as often the last as the first.
func TestAnnounceKeepsEveryLineOfTheCommand(t *testing.T) {
	command := "for f in *.go; do\n  gofmt -l \"$f\"\ndone"
	got := NewBash().Announce(args(t, command, "Check formatting."))
	for _, want := range []string{"gofmt -l", "done"} {
		if !strings.Contains(got, want) {
			t.Errorf("Announce() = %q, want it to keep %q", got, want)
		}
	}
	if strings.Contains(got, "...") {
		t.Errorf("Announce() = %q, want nothing elided", got)
	}
}

// Every line after the first is padded to where the first line's text starts,
// so the command reads as one block under the prompt marker.
func TestAnnounceAlignsContinuationLinesUnderTheFirst(t *testing.T) {
	previous := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = previous })

	command := "for f in *.go; do\n  gofmt -l \"$f\"\ndone"
	got := NewBash().Announce(args(t, command, "Check formatting."))
	want := []string{
		"Check formatting.",
		"  $ for f in *.go; do",
		"      gofmt -l \"$f\"",
		"    done",
	}
	if lines := strings.Split(got, "\n"); !slices.Equal(lines, want) {
		t.Errorf("Announce() lines = %q, want %q", lines, want)
	}
}

// A blank line inside a command stays blank rather than becoming padding
// nobody can see but everybody copies.
func TestAnnounceLeavesBlankLinesEmpty(t *testing.T) {
	previous := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = previous })

	got := NewBash().Announce(args(t, "echo one\n\necho two", "Run two commands."))
	if !strings.Contains(got, "\n\n    echo two") {
		t.Errorf("Announce() = %q, want the blank line left empty", got)
	}
}

// Announce is display only and must not fail on unparsable arguments. An
// empty summary leaves the registry showing the label alone.
func TestAnnounceToleratesBadArguments(t *testing.T) {
	if got := NewBash().Announce(json.RawMessage(`{`)); got != "" {
		t.Errorf("Announce() = %q, want an empty summary", got)
	}
}

// Oversized output is cut by tools.Truncate, which the tools package tests;
// what belongs here is that bash asks for it at all.
func TestOversizedOutputIsCut(t *testing.T) {
	b := NewBash()
	out, err := b.Call(context.Background(), args(t, "head -c 100000 /dev/zero | tr '\\0' 'x'", "fill the output"))
	if err != nil {
		t.Fatalf("Call() = %v", err)
	}
	if len(out) > maxOutput+200 {
		t.Errorf("Call() returned %d bytes, want it capped near %d", len(out), maxOutput)
	}
	if !strings.Contains(out, "bytes omitted") {
		t.Error("Call() did not say that output was omitted")
	}
}

// Stopping the run kills what the command started as well as the command,
// however deep it goes.
//
// This is the case Ctrl-C is actually pressed for: a find over the whole disk,
// a connection to a server that will not answer. Killing the shell alone
// leaves those running, and the person who pressed it has not got what they
// asked for.
//
// The grandchild leaves its trace after a delay, so a missing marker means it
// was killed rather than that the test looked too early, and the wait after
// the cancellation is twice that delay so it had every chance.
func TestCancellationKillsWhatTheCommandStarted(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survived")
	call := args(t, "(sleep 2; touch "+marker+") & echo started; wait",
		"Start something that outlives the shell and leaves a trace.")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Nothing here touches t, so this goroutine outliving the test is
		// harmless.
		_, _ = NewBash().Call(ctx, call)
	}()

	// Long enough for the shell to have started its child, which is the whole
	// situation under test.
	time.Sleep(200 * time.Millisecond)
	cancel()
	time.Sleep(4 * time.Second)

	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the grandchild left its mark (%v), so it outlived the run that "+
			"was stopped; a command's children have to go with it", err)
	}
}

// The counterpart, and the line between the two: a command that finished by
// itself keeps whatever it deliberately left running. Only a stopped run
// sweeps up.
//
// Without this, the rule above could be made true by killing the group at the
// end of every call, which would take away from a model any way of leaving
// something running on purpose — and nothing would say so.
func TestAFinishedCommandKeepsWhatItLeftRunning(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survived")
	// The delay has to outlast waitDelay, or this cannot fail. Call returns
	// when the bound closes the pipes the background job is holding, and a
	// job that had already finished by then would leave its mark whether or
	// not anything went on to kill it.
	call := args(t, "(sleep 3; touch "+marker+") & echo started",
		"Leave something running and return without waiting for it.")

	if _, err := NewBash().Call(context.Background(), call); err != nil {
		t.Fatalf("Call() = %v, want the command to succeed", err)
	}
	time.Sleep(4 * time.Second)

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the background job left no trace (%v), want a command that "+
			"finished to keep what it deliberately left running", err)
	}
}
