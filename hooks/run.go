package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/ChrisVilches/agi/tools"
)

const (
	// blocked is the exit status a hook refuses a call with. It is Claude
	// Code's, and it is the reason a hook can say no without printing
	// anything at all.
	blocked = 2
	// shell runs a command line that was written as one. Claude Code uses a
	// shell for the same form, which is what lets a one-line hook pipe jq
	// into grep instead of being a file somebody had to create.
	shell = "/bin/sh"
	// maxReason caps what a hook's words can add to the conversation. A hook
	// that fails at length is still a hook whose reason the model reads, and
	// an unbounded one would be a way for a script to fill the context
	// window.
	maxReason = 4_000
	// waitDelay bounds how long a stopped hook waits for its output to
	// close. Killing a program does not necessarily end what it started, and
	// a child holding the same pipes open would otherwise hold the run.
	waitDelay = 2 * time.Second
)

// Refusal is a hook saying a call may not be made.
//
// It reaches the model as a tool failure, which is what a failure is for: a
// call that could not be made. The agent carries on and gets a turn to
// explain itself or try another way, so this is the outcome a guard is
// normally written to produce.
type Refusal struct {
	// Hook is the program that refused, for the user watching and the log.
	Hook string
	// Reason is what the hook said, and is the whole of what the model is
	// told about it.
	Reason string
}

func (r *Refusal) Error() string { return r.Reason }

// Stop is a hook saying the agent that made the call must stop.
//
// Three things raise one: a program that cannot be started, a program that
// outran the timeout, and a hook that printed continue: false. The first two
// are agi's machinery failing rather than anyone's decision, and they stop
// the agent because a guard that is broken must never fail open — the call it
// was there to stop is exactly the call that would otherwise go through.
//
// Which agent stops is the caller's to decide and not this package's. The run
// ends only when the agent is the one the user is talking to; a fork or a
// subagent ends alone and its parent reads why.
type Stop struct {
	Hook   string
	Reason string
}

func (s *Stop) Error() string { return s.Reason }

// step is what one hook's run came to, for a hook that did not refuse the
// call or stop the agent. Those two are errors, because they end the sequence.
type step struct {
	// arguments are what the call should now be made with, when a PreToolUse
	// hook rewrote them. Empty means it did not.
	arguments []byte
	// result is what the model should now read, when a PostToolUse hook
	// rewrote it. A nil pointer means it did not, which is not the same as a
	// hook deliberately replacing the output with nothing.
	result *string
	// context is text to put into the conversation beside the tool's result.
	context string
	// message is a warning for the user watching, and never reaches the
	// model.
	message string
	// problem is a hook that went wrong in a way that does not stop
	// anything: a status that means nothing in the protocol, or output that
	// claimed to be a reply and would not parse. The user is told and the
	// call goes ahead, which is Claude Code's behaviour for both.
	problem string
}

// run executes one hook and reads what it decided.
//
// The exit status is the protocol's spine. Zero proceeds and whatever the
// hook printed is read as a reply; two refuses the call; anything else is a
// non-blocking error, where the reply is still honored if there was one and
// the call otherwise goes ahead. That last rule is Claude Code's and it is
// deliberately kept: a hook that fails in a way nobody planned for should not
// quietly become a veto.
//
// What is not Claude Code's is a program that cannot be started, or one that
// outruns the timeout. There they are warnings and here they stop the agent,
// for the reason Stop gives.
func (s *Set) run(ctx context.Context, h *Hook, event Event, c Call, out *payload) (step, error) {
	stdin, err := s.encode(event, c, out)
	if err != nil {
		// Nothing a hook did: agi could not describe the call. Stopping is
		// the safe reading, since the guard never got to see it.
		return step{}, &Stop{Hook: h.command, Reason: fmt.Sprintf(
			"agi could not describe the call to the hook %s: %v", h.command, err)}
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	cmd := h.process(ctx)
	cmd.Dir = h.dir
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = waitDelay
	// The environment is inherited by leaving Env unset, which is the whole
	// of how a hook is configured.
	runErr := cmd.Run()

	said := strings.TrimRight(stdout.String(), "\n")
	problem := strings.TrimRight(stderr.String(), "\n")

	// The run being stopped underneath the hook is not the hook failing, and
	// there is no next turn to report it in.
	if errors.Is(ctx.Err(), context.Canceled) {
		return step{}, ctx.Err()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return step{}, &Stop{Hook: h.command, Reason: fmt.Sprintf(
			"the hook %s took longer than %s%s", h.command, s.timeout, appended(problem))}
	}

	var exit *exec.ExitError
	switch {
	case runErr == nil, errors.As(runErr, &exit):
		// The program ran and said something, whatever its status. Which is
		// read below.
	case errors.Is(runErr, syscall.ENOEXEC):
		return step{}, &Stop{Hook: h.command, Reason: fmt.Sprintf(
			"the hook %s cannot be run by the kernel; a hook has to begin with a shebang line "+
				"naming its interpreter, such as #!/usr/bin/env python3", h.command)}
	default:
		return step{}, &Stop{Hook: h.command, Reason: fmt.Sprintf(
			"the hook %s could not be run: %v", h.command, runErr)}
	}

	status := 0
	if exit != nil {
		status = exit.ExitCode()
	}
	return h.decide(status, said, problem)
}

// decide reads one finished hook: its status, what it printed, and what it
// said went wrong.
func (h *Hook) decide(status int, said, problem string) (step, error) {
	var result step

	answer, err := parseReply(said)
	// The event-specific half is read through a variable rather than off the
	// reply, because a hook may have printed nothing at all and the refusal
	// below is reachable on a bare exit status of two.
	var only *specific
	if err != nil {
		// Output that opened as an object and would not parse is a hook
		// trying to say something and failing. Claude Code calls that a
		// non-blocking error, and so does agi: the call goes ahead and the
		// user is told, because a broken reply is a bug in the hook and not
		// a decision about the call.
		result.problem = fmt.Sprintf("the hook %s printed something that is not a reply: %v",
			h.command, err)
		answer = nil
	}

	if answer != nil {
		only = answer.Specific
		// continue: false outranks everything else a reply can say. It is
		// the one field that is about the run rather than about this call.
		if answer.halts() {
			return result, &Stop{Hook: h.command, Reason: reason(
				answer.StopReason, problem, fmt.Sprintf("the hook %s stopped the agent", h.command))}
		}
		result.context = only.context()
		result.message = answer.SystemMessage
	}

	// "ask" means escalate to a person, which agi has no way to do: there is
	// no permission prompt, and a run is as often piped with nobody watching
	// as it is run at a terminal. It is treated as "deny", which is the safe
	// half of what the hook asked for — but silently doing something other
	// than what a hook asked is how a user comes to believe their guard
	// behaves one way when it behaves another, so it is said out loud.
	//
	// It is a note and not a failure. The hook is not broken and the call is
	// still refused; what the user needs to know is that the decision they
	// wanted to make themselves was made for them.
	if only.decision() == decisionAsk {
		result.problem = fmt.Sprintf("the hook %s asked to put this call to a person, which agi "+
			"cannot do; it was refused instead", h.command)
	}

	// A status of two refuses the call, and a reply cannot take that back:
	// the hook said no twice over if it also printed a decision, and once if
	// it did not.
	if status == blocked || refuses(only.decision()) {
		return result, &Refusal{Hook: h.command, Reason: reason(
			only.reason(), problem,
			fmt.Sprintf("the hook %s refused the call and explained nothing", h.command))}
	}

	if answer != nil {
		if answer.blocks() {
			return result, &Stop{Hook: h.command, Reason: reason(
				answer.Reason, problem,
				fmt.Sprintf("the hook %s stopped the agent after reading the result", h.command))}
		}
		if only != nil {
			result.arguments = only.UpdatedInput
			if only.UpdatedToolOutput != nil {
				text := only.UpdatedToolOutput.Text
				result.result = &text
			}
		}
	}

	// Any other status is a hook that went wrong rather than a hook that
	// decided something. The call goes ahead and the user is told.
	if status != 0 && result.problem == "" {
		result.problem = fmt.Sprintf("the hook %s exited with status %d%s",
			h.command, status, appended(problem))
	}
	return result, nil
}

// decision, reason and context read a reply's event-specific half, which a
// hook need not have written at all.
func (s *specific) decision() string {
	if s == nil {
		return ""
	}
	return s.PermissionDecision
}

func (s *specific) reason() string {
	if s == nil {
		return ""
	}
	return s.PermissionDecisionReason
}

func (s *specific) context() string {
	if s == nil {
		return ""
	}
	return s.AdditionalContext
}

// reason is what the model or the user is told, out of the several places a
// hook may have said it.
//
// A hook's own words come first, then whatever it printed on standard error,
// and last a sentence agi writes for a hook that decided something and
// explained nothing. The fallback matters: a bare exit status is not a reason
// the model can act on, and a refusal with nothing in it would leave it
// guessing at what it did wrong.
func reason(given, problem, fallback string) string {
	for _, text := range []string{given, problem} {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			return tools.Truncate(trimmed, maxReason)
		}
	}
	return fallback
}

// appended renders whatever a hook managed to print, for adding to a sentence
// about it. It is often empty.
func appended(problem string) string {
	if trimmed := strings.TrimSpace(problem); trimmed != "" {
		return "; it said: " + tools.Truncate(trimmed, maxReason)
	}
	return ""
}

// command builds the process for one hook, in whichever of the two forms its
// manifest wrote.
//
// Without args the command is a shell command line, so a shell runs it and
// pipes and globs work. With args it is a program, run directly, with each
// argument passed as it was written and no shell to reinterpret any of it.
func (h *Hook) process(ctx context.Context) *exec.Cmd {
	if h.args == nil {
		return exec.CommandContext(ctx, shell, "-c", h.command)
	}
	return exec.CommandContext(ctx, h.command, h.args...)
}
