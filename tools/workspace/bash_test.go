package workspace

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
		t.Error("bash implements SystemPrompter, but only control-flow tools may")
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

// The explanation exists so the user can see why a command is running.
func TestAnnounceShowsExplanationAndCommand(t *testing.T) {
	got := NewBash().Announce(args(t, "df -h", "Check free disk space."))
	if !strings.Contains(got, "Check free disk space.") {
		t.Errorf("Announce() = %q, want the explanation", got)
	}
	if !strings.Contains(got, "df -h") {
		t.Errorf("Announce() = %q, want the command", got)
	}
}

func TestAnnounceCollapsesMultilineCommands(t *testing.T) {
	got := NewBash().Announce(args(t, "echo one\necho two", "Run two commands."))
	if strings.Count(got, "\n") != 1 {
		t.Errorf("Announce() = %q, want the command collapsed to one line", got)
	}
	if !strings.Contains(got, "...") {
		t.Errorf("Announce() = %q, want an ellipsis marking the collapse", got)
	}
}

// Announce is display only and must not fail on unparsable arguments.
func TestAnnounceToleratesBadArguments(t *testing.T) {
	if got := NewBash().Announce(json.RawMessage(`{`)); got != "bash" {
		t.Errorf("Announce() = %q, want a safe fallback", got)
	}
}

func TestTruncateKeepsHeadAndTail(t *testing.T) {
	s := strings.Repeat("a", maxOutput/2) + strings.Repeat("b", 500) + strings.Repeat("c", maxOutput/2)
	got := truncate(s)
	if len(got) >= len(s) {
		t.Fatalf("truncate() did not shorten %d bytes", len(s))
	}
	if !strings.HasPrefix(got, "aaa") || !strings.HasSuffix(got, "ccc") {
		t.Error("truncate() did not keep both the head and the tail")
	}
	if !strings.Contains(got, "bytes omitted") {
		t.Error("truncate() did not say that output was omitted")
	}
}
