// Package userdefined runs tools the user wrote themselves, in whatever
// language they wrote them in.
//
// A folder named on the command line holds a manifest.json and the executables
// it declares. The manifest is the whole of what agi knows about them: which
// tools there are, what each one is called, which file runs it, what it is for,
// and what it takes. Nothing is discovered by looking around the folder, so a
// file that is not listed is not a tool, and a tool that is listed and missing
// fails the run rather than disappearing from it.
//
// A tool from a folder is an ordinary tool. It is described to the model by
// its Definition and by nothing else, exactly as a workspace tool is, and it
// contributes nothing to the system prompt — a run may have been given none of
// them, and a prompt describing a tool that is not registered is a standing
// lie to the model. It follows that the description in the manifest is the
// only thing the model will ever learn about the tool, which is why an empty
// one is refused.
//
// # The protocol
//
// The script is executed directly, never through a shell, so the file must be
// executable and must begin with a shebang line naming its interpreter. It is
// given exactly one argument: the JSON object of arguments the model produced,
// already checked against the parameters the manifest declared. Named
// parameters are why it is one JSON argument rather than several positional
// ones — the script parses it and takes what it needs, and adding a parameter
// to the manifest does not renumber anything.
//
// It inherits agi's environment and working directory, and its standard input
// is empty. Whatever it needs — an API key, a path, a machine to talk to — is
// therefore configured the way agi itself is, by exporting it in the shell
// that starts the run.
//
// What it says is read from its output streams, and the exit status decides
// which one:
//
//   - Exit status 0 is success, and standard output is the result handed to
//     the model. Standard error is discarded, so a script may log to it.
//   - Any other status is failure, and standard error is the reason handed to
//     the model, or standard output when standard error is empty.
//
// So a script should explain itself in words either way: on success what it
// did or found, and on failure why it could not. The model reads one or the
// other and nothing else, and a tool that fails silently leaves it guessing.
//
// Success and failure reach the conversation as the two things they are: a
// tool result for the first, and a tool failure for the second, which is the
// same distinction agi draws everywhere between a call that produced something
// and a call that could not be completed.
package userdefined

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/tools"
)

// DefaultTimeout bounds one call, so a script that hangs cannot stall a run.
// It is the same bound a shell command gets and for the same reason: nothing
// in the protocol lets a script say it is still working, so waiting forever
// on one is waiting on a promise nobody made.
const DefaultTimeout = 2 * time.Minute

// waitDelay bounds how long a stopped call waits for its output to close.
//
// Killing a script does not necessarily end everything it started: a child it
// left behind holds the same output pipes open, and waiting for them to close
// would be waiting on a process agi never ran and cannot see. So the pipes are
// given a moment to drain and then closed, which is what makes a timeout a
// bound on the call rather than on the best-behaved part of it.
const waitDelay = 2 * time.Second

// maxOutput caps how much of a script's output is handed back to the model.
const maxOutput = 30_000

// maxAnnounced caps how much of a call's arguments is shown to the user as the
// call is announced. The announcement is a line, not the record: what was
// actually sent is in the log.
const maxAnnounced = 160

// Tool is one tool declared by a manifest.
//
// Everything in it was settled when the manifest was read: the script exists
// and is executable, the description is not empty, and the parameters are a
// schema agi can check an argument list against. What is left for a call is
// checking the model's arguments and running the script.
type Tool struct {
	name        string
	description string
	// script is the absolute path to the executable, resolved against the
	// folder the manifest is in. It is absolute so that nothing about the
	// call depends on where agi happens to be running from.
	script string
	// params is the schema the model is shown, as the manifest wrote it.
	params map[string]any
	// check is the same schema in the form arguments are validated against.
	check *objectRule
	// Timeout bounds one call. Zero means DefaultTimeout.
	Timeout time.Duration
}

// Name returns the name the model calls the tool by.
func (t *Tool) Name() string { return t.name }

// Script returns the path to the executable the tool runs.
func (t *Tool) Script() string { return t.script }

// Definition describes the tool to the model, out of what the manifest said.
// As for a workspace tool, this is the whole of what the model is told, so the
// usage rules have to be in the description the user wrote.
func (t *Tool) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name:        t.name,
		Description: t.description,
		Parameters:  t.params,
	}
}

// Announce summarizes a pending call as the arguments it was given, dimmed and
// on one line. A user-defined tool has no explanation parameter to show — agi
// did not write its schema and will not add a field to someone else's tool —
// so the arguments are what there is, and they are usually enough to see what
// is about to happen. The tool's name is added by the registry.
func (t *Tool) Announce(args json.RawMessage) string {
	text := compact(args)
	if text == "" || text == "{}" {
		return ""
	}
	if len(text) > maxAnnounced {
		text = text[:maxAnnounced] + "..."
	}
	return tools.Detail(text)
}

// compact renders arguments on one line, leaving them as they came if they are
// not valid JSON: this is display, and it must not fail.
func compact(args json.RawMessage) string {
	if len(bytes.TrimSpace(args)) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, args); err != nil {
		return strings.Join(strings.Fields(string(args)), " ")
	}
	return buf.String()
}

// Call checks the arguments against the manifest's parameters, runs the
// script, and returns what it said.
//
// A returned error is a call that did not produce anything — arguments the
// tool would have refused, a script that could not be started, or a script
// that exited non-zero — and reaches the model as a tool failure. Output on
// success comes back as the result. Which of the two a non-zero exit is was
// decided by the protocol rather than here: a script says it failed by
// failing, and agi passes that on as a failure instead of handing the model an
// exit status to interpret.
func (t *Tool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	payload, err := t.check.validate(args)
	if err != nil {
		return "", fmt.Errorf("%s: %w", t.name, err)
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The script is run directly, with the arguments as its one argument.
	// There is no shell in between, so nothing in the JSON can be read as a
	// command, and the interpreter comes from the file's own shebang.
	cmd := exec.CommandContext(ctx, t.script, payload)
	cmd.WaitDelay = waitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// The environment is inherited by leaving Env unset, which is the whole of
	// how a user-defined tool is configured. Standard input is left nil, so
	// the script reads end-of-file at once rather than waiting for input that
	// is never coming.
	runErr := cmd.Run()

	out := tools.Truncate(strings.TrimRight(stdout.String(), "\n"), maxOutput)
	problem := tools.Truncate(strings.TrimRight(stderr.String(), "\n"), maxOutput)

	if errors.Is(ctx.Err(), context.Canceled) {
		// The script was killed along with the run that asked for it, rather
		// than failing on its own. There is no next turn to report that in.
		return "", ctx.Err()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("%s: timed out after %s%s", t.name, timeout, said(problem, out))
	}

	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		return out, nil
	case errors.As(runErr, &exitErr):
		return "", fmt.Errorf("%s: %s", t.name, failureText(problem, out, exitErr.ExitCode()))
	case errors.Is(runErr, syscall.ENOEXEC):
		return "", fmt.Errorf("%s: %s cannot be run by the kernel; an executable tool has to "+
			"begin with a shebang line naming its interpreter, such as #!/usr/bin/env python3",
			t.name, t.script)
	default:
		return "", fmt.Errorf("%s: could not run %s: %w", t.name, t.script, runErr)
	}
}

// failureText is the protocol's choice of what a failed call says: standard
// error, or standard output when standard error is empty.
//
// A script that failed and said nothing at all is the one case with nothing to
// pass on, and the exit status stands in for the explanation it should have
// given. It is mentioned only then: a status number beside a sentence that
// already says what went wrong would add nothing the model can act on.
func failureText(problem, out string, status int) string {
	if text := firstNonEmpty(problem, out); text != "" {
		return text
	}
	return fmt.Sprintf("exited with status %d and explained nothing", status)
}

// said renders whatever a timed-out script managed to print, for appending to
// the reason it was stopped. It may well be empty.
func said(problem, out string) string {
	if text := firstNonEmpty(problem, out); text != "" {
		return "; it said: " + text
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
