// Package userdefined runs tools the user wrote themselves, in whatever
// language they wrote them in.
//
// A folder named on the command line holds a tools.json and the executables it
// declares. That file is the whole of what agi knows about them: which
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
// What it says is both of its output streams, woven back together in the order
// agi observed them, followed by a line naming the exit status when it is not
// zero. That is the same thing a shell command gets, and deliberately so: a
// user-defined tool runs a program exactly as the bash tool does, and two rules
// for the same thing would mean the model had to learn which tools used which.
//
// So a script explains itself in words and may log freely to standard error:
// both reach the model, and neither is thrown away because of how the script
// chose to exit.
//
// A script that exits non-zero has not failed the call; it has run and reported
// something, which the model reads and can act on. That is the contract every
// tool in agi follows — an error is a call that could not be attempted, not a
// program that ran and went badly — and it is what lets a PostToolUse hook see
// a failing script at all, since a failed call runs no hook.
//
// A call does fail, and reaches the model as a tool failure, when there was
// nothing to run: arguments the manifest's parameters refuse, a script the
// kernel will not execute, a file that is not there. Nothing ran, so there is
// nothing to report but why.
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
func (t *Tool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	out, _, err := t.Execute(ctx, args)
	return out, err
}

// Execute runs the tool and hands back the record of the program alongside the
// text the model reads, so that a hook can be shown the two streams apart.
//
// A returned error is a call that could not be attempted — arguments the tool
// would have refused, a script the kernel will not run, a file that is not
// there — and reaches the model as a tool failure. A script that ran and exited
// non-zero is not one of those: it reports that in its output, under the status
// line, which is the contract every tool in agi follows.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (string, *tools.Execution, error) {
	payload, err := t.check.validate(args)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", t.name, err)
	}

	timeout := t.timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The script is run directly, with the arguments as its one argument.
	// There is no shell in between, so nothing in the JSON can be read as a
	// command, and the interpreter comes from the file's own shebang.
	cmd := exec.CommandContext(ctx, t.script, payload)
	cmd.WaitDelay = waitDelay
	// A stopped run takes the script's own children with it. A script is
	// somebody's program in whatever language they like, and running other
	// programs is most of what one is for.
	tools.KillGroupOnCancel(cmd)
	// The environment is inherited by leaving Env unset, which is the whole of
	// how a user-defined tool is configured. Standard input is left nil, so
	// the script reads end-of-file at once rather than waiting for input that
	// is never coming.
	ran := tools.Execute(ctx, cmd)

	switch ran.Ending {
	case tools.Cancelled:
		// The script was killed along with the run that asked for it, rather
		// than failing on its own. There is no next turn to report that in.
		return "", nil, ctx.Err()
	case tools.Unstarted:
		if errors.Is(ran.Err, syscall.ENOEXEC) {
			return "", nil, fmt.Errorf("%s: %s cannot be run by the kernel; an executable tool "+
				"has to begin with a shebang line naming its interpreter, such as "+
				"#!/usr/bin/env python3", t.name, t.script)
		}
		return "", nil, fmt.Errorf("%s: could not run %s: %w", t.name, t.script, ran.Err)
	}
	return ran.Account(maxOutput, timeout), ran, nil
}

// timeout is the bound on one call, with the default applied.
func (t *Tool) timeout() time.Duration {
	if t.Timeout <= 0 {
		return DefaultTimeout
	}
	return t.Timeout
}

// Response implements tools.Responder, in the shape every tool that runs a
// program answers in. A script is a program like any other, and a guard
// written for Claude Code's Bash reads the same fields of it. See
// tools.Response.
func (t *Tool) Response(result string, ran *tools.Execution) any {
	return tools.Response(result, ran, maxOutput)
}

// Replacement implements tools.Responder, reading a hook's rewritten output
// back into the one string the model reads. See tools.Replacement.
func (t *Tool) Replacement(raw json.RawMessage, ran *tools.Execution) (string, bool) {
	return tools.Replacement(raw, ran, maxOutput, t.timeout())
}
