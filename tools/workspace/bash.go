package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/tools"
)

// DefaultTimeout bounds a single command, so a hung process cannot stall a run.
const DefaultTimeout = 2 * time.Minute

// maxOutput caps how much command output is handed back to the model. Output
// is trimmed in the middle by tools.Truncate, since the head and tail are the
// informative parts.
const maxOutput = 30_000

// BashParams is the input schema for the bash tool.
type BashParams struct {
	// Command is the shell command to execute.
	Command string `json:"command"`
	// Explanation is why the agent is running this command.
	//
	// TODO: when stdout is not a terminal the explanation has no reader. Its
	// one consumer is Announce, and that output is discarded in that case, so
	// requiring it there costs tokens on every call and buys nothing visible.
	// It could be dropped from the schema for non-TTY runs.
	//
	// Keeping it required for now, deliberately: the explanation is not only
	// for display. Asking for a reason alongside the command makes the model
	// state its intent before acting, which is worth something whether or not
	// anyone reads it. Removing it in the piped case would also make the tool
	// schema differ between runs, which would fragment prompt caching and make
	// a piped run behave unlike the terminal run it was debugged in. Revisit
	// once there is evidence about how much the reasoning actually helps.
	Explanation string `json:"explanation"`
}

// Bash runs shell commands.
type Bash struct {
	// Shell is the interpreter used. Defaults to "bash".
	Shell string
	// Dir is the working directory. Defaults to the process's own.
	Dir string
	// Timeout bounds a single command. Defaults to DefaultTimeout.
	Timeout time.Duration
}

// NewBash returns a Bash tool with defaults applied.
func NewBash() *Bash {
	return &Bash{Shell: "bash", Timeout: DefaultTimeout}
}

// Definition describes the tool to the model.
//
// This description is the only thing the model is told about bash, so it has to
// carry the usage rules itself — the system prompt says nothing about it.
func (b *Bash) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: "bash",
		Description: strings.TrimSpace(`
Run a shell command on the user's machine and return its output.

Use this for inspecting the system, running builds and tests, and any task a
command line handles well. Output is combined stdout and stderr, followed by
the exit status when it is non-zero; a failing command is reported to you
rather than hidden, so read the output and adjust.

Prefer one focused command per call so that a failure is easy to attribute.
Commands run non-interactively: they cannot prompt, so pass flags that avoid
prompting, and never start a command that waits for input or runs forever.
Quote paths that may contain spaces.`),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command to execute.",
				},
				"explanation": map[string]any{
					"type": "string",
					"description": "One sentence, in the second person, saying why you are " +
						"running this command and what you expect to learn or change. " +
						"It is shown to the user as the command runs.",
				},
			},
			"required":             []string{"command", "explanation"},
			"additionalProperties": false,
		},
	}
}

// parse decodes and validates the model's arguments.
func parse(args json.RawMessage) (BashParams, error) {
	var p BashParams
	if err := json.Unmarshal(args, &p); err != nil {
		return p, fmt.Errorf("bash: arguments are not valid JSON: %w", err)
	}
	if strings.TrimSpace(p.Command) == "" {
		return p, errors.New("bash: the command parameter is required")
	}
	if strings.TrimSpace(p.Explanation) == "" {
		return p, errors.New("bash: the explanation parameter is required")
	}
	return p, nil
}

// Announce summarizes a pending call for the user: the reason the agent gave,
// then the command itself on its own line, dimmed so the reason reads first.
// The tool's name is added by the registry.
func (b *Bash) Announce(args json.RawMessage) string {
	p, err := parse(args)
	if err != nil {
		return ""
	}
	return p.Explanation + "\n" + tools.Detail("  $ "+singleLine(p.Command))
}

// Call runs the command and returns its combined output.
func (b *Bash) Call(ctx context.Context, args json.RawMessage) (string, error) {
	p, err := parse(args)
	if err != nil {
		return "", err
	}

	shell := b.Shell
	if shell == "" {
		shell = "bash"
	}
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-c", p.Command)
	cmd.Dir = b.Dir
	out, runErr := cmd.CombinedOutput()

	result := tools.Truncate(string(out), maxOutput)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return appendStatus(result, fmt.Sprintf("command timed out after %s", timeout)), nil
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		// The command was killed along with the run that asked for it, rather
		// than failing on its own. Reporting what it managed to print, under
		// the exit status a signal leaves behind, would describe it as a
		// command that ran and went wrong; it is a call that could not be
		// made, and the run is ending anyway.
		return "", ctx.Err()
	}

	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		return result, nil
	case errors.As(runErr, &exitErr):
		// A non-zero exit is information for the model, not a harness failure.
		return appendStatus(result, fmt.Sprintf("exit status %d", exitErr.ExitCode())), nil
	default:
		return appendStatus(result, fmt.Sprintf("could not run command: %v", runErr)), nil
	}
}

// appendStatus adds a trailing status line to command output.
func appendStatus(out, status string) string {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return "(no output)\n" + status
	}
	return out + "\n" + status
}

// singleLine collapses a command to one line for display.
func singleLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		return strings.TrimSpace(s[:i]) + " ..."
	}
	return s
}
