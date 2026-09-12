package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Report is what the hooks said alongside the data they were given.
//
// It is returned with the data and never instead of it, including when a hook
// refused the call or stopped the agent: the hooks that ran before that one
// did run, and their record is owed to the log whatever the last of them
// decided.
type Report struct {
	// Context is text a hook asked to be put into the conversation beside
	// the tool's result, joined when more than one hook asked. It is the
	// only thing here the model ever reads.
	Context string
	// Notes are for the person watching and never reach the model: what a
	// hook asked to be shown, and what went wrong with a hook that failed in
	// a way that stopped nothing.
	Notes []string
	// Ran is one entry per hook that ran, in order, for the log.
	Ran []Ran
}

// Ran is the record of one hook having run.
//
// It says what this package knows and nothing more. Which tool was being
// guarded, and which call, are the caller's own and are already beside it
// wherever this is written down.
type Ran struct {
	// Hook is the program, as the manifest named it.
	Hook string
	// Event is which of the two moments it ran at.
	Event Event
	// Elapsed is how long it took.
	Elapsed time.Duration
	// Decided is what it came to, in a word.
	Decided Decision
	// Problem is why it went wrong, for a hook that did.
	Problem string
}

// Decision is what one hook came to, in a word, for a reader filtering a log.
type Decision string

const (
	// Proceeded is a hook that let the call through unchanged.
	Proceeded Decision = "proceeded"
	// Rewrote is a hook that changed the arguments or the result.
	Rewrote Decision = "rewrote"
	// Refused is a hook that would not let the call be made.
	Refused Decision = "refused"
	// Stopped is a hook that ended the agent.
	Stopped Decision = "stopped"
	// Failed is a hook that went wrong without deciding anything, which
	// leaves the call to go ahead.
	Failed Decision = "failed"
)

// Before runs the PreToolUse hooks that match and returns the arguments the
// tool should be called with, which may not be the ones the model wrote.
//
// The hooks run in sequence, in the order the folders were named and the
// order each manifest lists them, and each one is shown what the one before
// it left. That chaining is why they are not run at the same time as one
// another, which is what Claude Code does: a partial rewrite of a call's
// arguments means nothing if two hooks write over each other.
//
// A refusal or a stop ends the sequence, because there is no longer a call
// for the hooks after it to inspect. The report still comes back, holding
// what the earlier ones did.
func (s *Set) Before(ctx context.Context, c Call) (json.RawMessage, Report, error) {
	var report Report
	arguments := c.Arguments

	for _, h := range s.forEvent(PreToolUse) {
		// Whether a hook matches is asked when its turn comes and not before,
		// so a rewrite by an earlier hook is judged by the conditions of the
		// later ones. Deciding the list up front would let one hook change a
		// harmless command into a dangerous one and walk it past the guard
		// written for exactly that.
		if !h.matches(c.Tool, arguments) {
			continue
		}
		at := c
		at.Arguments = arguments

		done, err := s.step(ctx, &report, h, PreToolUse, at, nil)
		if err != nil {
			return arguments, report, err
		}
		if len(done.arguments) > 0 {
			merged, err := merge(arguments, done.arguments)
			if err != nil {
				// A hook asked for a change agi could not make. The call goes
				// ahead unchanged rather than being refused: the hook did not
				// say no, it said something agi could not read, which is the
				// same as any other broken reply.
				report.note(fmt.Sprintf("the hook %s asked for a change to the arguments that "+
					"could not be made: %v", h.command, err))
				continue
			}
			arguments = merged
			report.decided(Rewrote)
		}
	}
	return arguments, report, nil
}

// After runs the PostToolUse hooks that match and returns what the model
// should read, which may not be what the tool produced.
//
// It does not run for a call that failed. Claude Code has an event of its own
// for that and agi does not implement it, so a tool that refused a call is
// answered without any hook seeing it.
//
// A hook here can stop the agent but cannot refuse anything: the tool has
// already run, and there is nothing left to withhold.
func (s *Set) After(ctx context.Context, c Call, output string) (string, Report, error) {
	var report Report

	for _, h := range s.forEvent(PostToolUse) {
		if !h.matches(c.Tool, c.Arguments) {
			continue
		}
		done, err := s.step(ctx, &report, h, PostToolUse, c, textPayload(output))
		if err != nil {
			return output, report, err
		}
		if done.result != nil {
			output = *done.result
			report.decided(Rewrote)
		}
	}
	return output, report, nil
}

// step runs one hook and folds what it said into the report.
//
// The record of it having run is written whatever it decided, including when
// it refused the call or stopped the agent, because a hook that ended the
// sequence is the one a reader of the log most wants to find.
func (s *Set) step(ctx context.Context, report *Report, h *Hook, event Event, c Call, out *payload) (step, error) {
	began := time.Now()
	done, err := s.run(ctx, h, event, c, out)
	ran := Ran{Hook: h.command, Event: event, Elapsed: time.Since(began), Decided: Proceeded}

	switch err.(type) {
	case *Refusal:
		ran.Decided, ran.Problem = Refused, err.Error()
	case *Stop:
		ran.Decided, ran.Problem = Stopped, err.Error()
	default:
		if err != nil {
			// The run was stopped underneath the hook. Nothing is recorded:
			// the hook did not decide anything and the run is ending for a
			// reason of its own.
			return done, err
		}
	}
	report.Ran = append(report.Ran, ran)
	if err != nil {
		return done, err
	}

	if done.problem != "" {
		report.at().Decided = Failed
		report.at().Problem = done.problem
		report.note(done.problem)
	}
	if done.message != "" {
		report.note(done.message)
	}
	if done.context != "" {
		report.add(done.context)
	}
	return done, nil
}

// at is the record of the hook that just ran, which is the last one appended.
func (r *Report) at() *Ran { return &r.Ran[len(r.Ran)-1] }

// decided marks what the hook that just ran came to, for a decision only the
// caller of step can see — a rewrite is known once the change has been made.
func (r *Report) decided(d Decision) {
	if len(r.Ran) > 0 && r.at().Decided == Proceeded {
		r.at().Decided = d
	}
}

// note adds a line for the person watching.
func (r *Report) note(line string) { r.Notes = append(r.Notes, line) }

// add joins one hook's context to whatever earlier hooks asked for.
//
// Several hooks may each have something to say, and they are separated by a
// blank line so that each arrives as its own paragraph — the same way the
// files of --sys are joined, and for the same reason: they are separate
// remarks and not one document.
func (r *Report) add(text string) {
	if r.Context == "" {
		r.Context = text
		return
	}
	r.Context += "\n\n" + text
}
