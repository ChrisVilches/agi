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

// waitDelay bounds how long a stopped command waits for its output to close.
//
// Killing a process does not kill what that process started, and this tool
// runs a shell, so it is the one most exposed to that: a command line with
// more than one command in it leaves the shell alive as a parent, and the
// children it spawned inherit the pipe this package is reading. Killing the
// shell orphans them, they hold the write end open, and a read that waits for
// end of file waits for the orphan rather than for the run.
//
// Without this bound that wait has no end, and what it holds up is the whole
// shutdown: Ctrl-C is supposed to stop the command where it is and let the run
// store its conversation and close its log, and instead the run sat there
// until the orphan happened to finish. The second Ctrl-C then killed agi
// outright, losing the snapshot.
//
// Two seconds is long enough for a command that is genuinely finishing its
// last write and short enough that nobody watching thinks agi ignored them. It
// is the same bound hooks and user-defined tools use, and it is written out
// here rather than shared because each of the three is bounding its own
// cleanup and has its own reason to change.
const waitDelay = 2 * time.Second

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
// then the command itself, dimmed so the reason reads first. The tool's name is
// added by the registry.
//
// The command is shown in full, however many lines it runs to. It is the one
// thing about the call that is about to happen to the user's machine, and a
// command abbreviated to its first line hides the part a reader is watching
// for — the redirection at the end, or the second command after the first.
func (b *Bash) Announce(args json.RawMessage) string {
	p, err := parse(args)
	if err != nil {
		return ""
	}
	return p.Explanation + "\n" + tools.Detail(block("$", p.Command))
}

// Call runs the command and returns its output.
func (b *Bash) Call(ctx context.Context, args json.RawMessage) (string, error) {
	out, _, err := b.Execute(ctx, args)
	return out, err
}

// Execute runs the command and hands back the record of it alongside the text
// the model reads, so that a hook can be shown the two streams apart.
//
// A returned error is a call that could not be made. A command that ran and
// exited non-zero is not one: that is information for the model, and it comes
// back as a result under the status line.
func (b *Bash) Execute(ctx context.Context, args json.RawMessage) (string, *tools.Execution, error) {
	p, err := parse(args)
	if err != nil {
		return "", nil, err
	}

	shell := b.Shell
	if shell == "" {
		shell = "bash"
	}

	ctx, cancel := context.WithTimeout(ctx, b.timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-c", p.Command)
	cmd.Dir = b.Dir
	cmd.WaitDelay = waitDelay
	// A stopped run takes the command's own children with it. This is the
	// tool that most needs it: the model writes the command line, and a
	// command line is exactly where a background job or a pipeline comes
	// from.
	tools.KillGroupOnCancel(cmd)
	ran := tools.Execute(ctx, cmd)

	switch ran.Ending {
	case tools.Cancelled:
		// The command was killed along with the run that asked for it, rather
		// than failing on its own. Reporting what it managed to print, under
		// the status a signal leaves behind, would describe it as a command
		// that ran and went wrong; it is a call that could not be made, and
		// the run is ending anyway.
		return "", nil, ctx.Err()
	case tools.Unstarted:
		return "", nil, fmt.Errorf("could not run command: %w", ran.Err)
	}
	return ran.Account(maxOutput, b.timeout()), ran, nil
}

// timeout is the bound on one command, with the default applied.
func (b *Bash) timeout() time.Duration {
	if b.Timeout <= 0 {
		return DefaultTimeout
	}
	return b.Timeout
}

// Response implements tools.Responder, in the shape every tool that runs a
// program answers in. See tools.Response for what each field means and why
// there is no fifth.
func (b *Bash) Response(result string, ran *tools.Execution) any {
	return tools.Response(result, ran, maxOutput)
}

// Replacement implements tools.Responder, reading a hook's rewritten output
// back into the one string the model reads. See tools.Replacement.
func (b *Bash) Replacement(raw json.RawMessage, ran *tools.Execution) (string, bool) {
	return tools.Replacement(raw, ran, maxOutput, b.timeout())
}
