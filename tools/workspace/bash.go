// Package workspace holds the tools that do ordinary work on the user's
// machine: running commands, and later reading, writing, and editing files.
//
// The name says what they have in common: they act on the workspace the user
// pointed agi at, not on the agent itself. They exist for convenience and can
// be switched off — a user may want an agent with none of them, or with their
// own tools in their place. Because a run may not include them, nothing in
// this package may contribute to the system prompt. Everything the model needs
// in order to call one correctly belongs in its Definition.
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
)

// DefaultTimeout bounds a single command, so a hung process cannot stall a run.
const DefaultTimeout = 2 * time.Minute

// maxOutput caps how much command output is handed back to the model. Output
// is trimmed in the middle, since the head and tail are the informative parts.
const maxOutput = 30_000

// BashParams is the input schema for the bash tool.
type BashParams struct {
	// Command is the shell command to execute.
	Command string `json:"command"`
	// Explanation is why the agent is running this command.
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

// Announce summarizes a pending call for the user.
func (b *Bash) Announce(args json.RawMessage) string {
	p, err := parse(args)
	if err != nil {
		return "bash"
	}
	return fmt.Sprintf("bash: %s\n  $ %s", p.Explanation, singleLine(p.Command))
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

	result := truncate(string(out))
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return appendStatus(result, fmt.Sprintf("command timed out after %s", timeout)), nil
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

// truncate trims the middle of oversized output, keeping head and tail.
func truncate(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	half := maxOutput / 2
	omitted := len(s) - 2*half
	return s[:half] +
		fmt.Sprintf("\n... [%d bytes omitted] ...\n", omitted) +
		s[len(s)-half:]
}

// singleLine collapses a command to one line for display.
func singleLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		return strings.TrimSpace(s[:i]) + " ..."
	}
	return s
}
