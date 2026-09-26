package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ChrisVilches/agi/eventlog"
	"github.com/ChrisVilches/agi/hooks"
	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/tools"
)

// hooked is one agent's hooks together with everything running them needs
// besides the call itself: where to print, where to record, and which agent
// and turn the call belongs to.
//
// It is a value rather than five arguments repeated at two call sites, for
// the reason wiring is one: two argument lists that happen to match are a
// claim nothing holds up, and the next field added is the one passed to one
// of them and forgotten in the other.
type hooked struct {
	out   output
	log   *eventlog.Log
	set   *hooks.Set
	agent string
	turn  int
}

// before runs the PreToolUse hooks and returns the arguments the tool should
// be called with, along with whatever the hooks asked to be put into the
// conversation.
//
// The arguments that come back may not be the ones the model wrote. The
// record is not touched either way: the assistant turn keeps the call as it
// was asked for, and what changed is only what the tool is handed. The user
// is shown the change, since what is about to happen to their machine is not
// what was announced a moment ago.
func (h hooked) before(ctx context.Context, registry *tools.Registry, req model.ToolRequest) (json.RawMessage, string, error) {
	arguments, report, err := h.set.Before(ctx, h.call(req))
	h.report(req.Name, req.ID, hooks.PreToolUse, report)
	if err != nil {
		return req.Arguments, report.Context, err
	}
	if !bytes.Equal(arguments, req.Arguments) {
		h.out.step("%s\n%s\n", tools.Detail("a hook changed this call:"),
			registry.Announce(req.Name, arguments))
	}
	return arguments, report.Context, nil
}

// after runs the PostToolUse hooks and returns what the model should read.
//
// A hook here cannot refuse anything — the tool has already run and there is
// nothing left to withhold — but it can replace the output, and it can stop
// the agent once the result has been read.
func (h hooked) after(ctx context.Context, req model.ToolRequest, output string) (string, string, error) {
	replaced, report, err := h.set.After(ctx, h.call(req), output)
	h.report(req.Name, req.ID, hooks.PostToolUse, report)
	if err != nil {
		return output, report.Context, err
	}
	return replaced, report.Context, nil
}

// say prints something the model wrote alongside its tool calls, and the
// prose answer of a run that was given a schema, after the MessageDisplay
// hooks have had their say about it.
//
// The two sites print the same shape — a paragraph and a blank line — which
// is why the format is here rather than passed in. What they have in common
// is that both are progress: they are dropped when nobody is watching, and
// so are the hooks over them.
func (h hooked) say(ctx context.Context, content string) {
	h.out.step("%s\n\n", h.shown(ctx, content, h.out.stepsShown))
}

// answer prints what the run produced, after the MessageDisplay hooks have
// had their say about it.
//
// The answer is displayed whatever it is going to — a terminal, a pipe, a
// file — so the hooks always run over it. A hook that emits escape sequences
// into a pipe has corrupted the answer, and that is the hook author's
// business, exactly as it is their business that a bash guard can be written
// to delete a file. agi documents it and does not police it, because the
// alternative is agi deciding what somebody's hook is for: one that redacts a
// secret, translates, or trims a long message is worth running into a pipe.
func (h hooked) answer(ctx context.Context, content string) {
	h.out.final(h.shown(ctx, content, h.out.answerShown))
}

// shown returns the text to print in place of content.
//
// Hooking here, at the print site, is what makes this display-only by
// construction rather than by care. The text has already been stored in the
// conversation, saved in the snapshot and written to the log by the time it
// arrives, so there is no path by which a hook could reach any of the three.
// That is a stronger guarantee than PostToolUse has, where the same rule is
// kept by nobody writing the line that would break it.
//
// Text going nowhere is returned untouched, which is the rule the event
// carries rather than a policy about terminals: the hooks fire when agi
// displays something, and a piped run without --verbose displays no
// progress at all. Spawning a renderer for a message about to be thrown away
// would cost the run a process per turn and show nobody anything.
func (h hooked) shown(ctx context.Context, content string, reaches bool) string {
	if !reaches {
		return content
	}
	displayed, report := h.set.OnDisplay(ctx, hooks.Message{Content: content, Agent: h.agent})
	h.report("", "", hooks.MessageDisplay, report)
	return displayed
}

// call describes one tool call to the hooks around it.
func (h hooked) call(req model.ToolRequest) hooks.Call {
	return hooks.Call{
		Tool:      req.Name,
		Arguments: req.Arguments,
		RequestID: req.ID,
		Agent:     h.agent,
	}
}

// report writes down what the hooks did and shows the user what they asked to
// have shown.
//
// Every hook that ran is recorded, including the one that refused the call or
// stopped the agent, because that is the line a reader of the log is looking
// for. What a hook asked to say to the model is not printed here: it goes
// into the conversation, and the agent loop prints what it appends.
//
// The tool and the request are passed rather than taken from a request,
// because a MessageDisplay hook guarded neither and has both empty. Both
// fields are left out of the line when they are, so a display hook's record
// says what it is — which hook ran, at which moment, and what it came to —
// and claims nothing about a call.
func (h hooked) report(tool, requestID string, moment hooks.Event, report hooks.Report) {
	for _, ran := range report.Ran {
		kind := eventlog.HookResult
		if ran.Decided == hooks.Failed {
			kind = eventlog.HookFailure
		}
		line := eventlog.Event{
			Kind:      kind,
			Turn:      h.turn,
			Tool:      tool,
			Hook:      ran.Hook,
			Moment:    string(moment),
			Decided:   string(ran.Decided),
			Reason:    ran.Problem,
			RequestID: requestID,
			Elapsed:   ran.Elapsed.Milliseconds(),
		}
		// What a hook rewrote goes on the line of the hook that rewrote it,
		// which is the only place it can go: the call and the result each get
		// one line for the whole batch of hooks, so a chain of them would
		// leave every rewrite but the last with nowhere to be recorded. It is
		// also the only account there is of what a display hook printed,
		// since the conversation and the snapshot keep the model's own words.
		//
		// Rewritten arguments go in the field arguments already go in, so a
		// reader joining a rewrite to the tool.call above it reads the two the
		// same way and the line stays machine-readable. Everything else a hook
		// can rewrite is text, and text is what Content is for.
		if ran.Decided == hooks.Rewrote {
			if moment == hooks.PreToolUse {
				line.Arguments = json.RawMessage(ran.Replacement)
			} else {
				line.Content = ran.Replacement
			}
		}
		h.log.Record(line)
	}
	for _, note := range report.Notes {
		h.out.step("%s\n", tools.Detail(note))
	}
}

// refused reports whether a hook would not let the call be made, in which
// case the model is told and the agent carries on.
func refused(err error) (*hooks.Refusal, bool) {
	var refusal *hooks.Refusal
	ok := errors.As(err, &refusal)
	return refusal, ok
}

// stopped reports whether a hook ended the agent.
func stopped(err error) (*hooks.Stop, bool) {
	var stop *hooks.Stop
	ok := errors.As(err, &stop)
	return stop, ok
}

// halt ends an agent a hook stopped, leaving the conversation in a state a
// later run can continue from.
//
// It is abandon's sibling rather than abandon itself, because the two report
// different things about a run that stopped for different reasons: the user
// pressing Ctrl-C, and a guard refusing to let the run go on. What they share
// is everything that has to happen afterwards, which is that the calls nobody
// answered are answered as calls that could not be made.
//
// Which agent this ends is decided by where it is called from and not here. A
// fork or a subagent that stops this way reports the reason to whoever
// spawned it, because cli.contain catches the sentinel on the way out; only
// the run's own agent ends the run.
func halt(lg *eventlog.Log, conv *model.Conversation, turn int, stop *hooks.Stop) error {
	for _, f := range conv.AbandonToolRequests(stop) {
		lg.Record(eventlog.Event{
			Kind:      eventlog.ToolFailure,
			Turn:      turn,
			RequestID: f.RequestID,
			Reason:    f.Reason,
		})
	}
	lg.Record(eventlog.Event{
		Kind:     eventlog.RunInterrupt,
		Turn:     turn,
		Hook:     stop.Hook,
		Reason:   stop.Reason,
		Messages: conv.Len(),
	})
	return stop
}

// hookList renders the hooks a run is armed with, for the opening block of a
// verbose run.
//
// A hook can stop an agent, silently rewrite what a tool was asked to do, or
// change what the user is shown, which makes it exactly the kind of thing
// somebody watching a run needs to have been told about beforehand. It is one
// line each: when it runs, which tools it is watching, and what it runs.
//
// A hook of an event that is not about a tool has no matcher, and nothing is
// printed where one would be. Hook.Matcher renders an absent matcher as "*",
// which is right for a tool hook that watches every tool and would be a
// small lie here: there is no tool it declined to name.
func hookList(set *hooks.Set) string {
	if set.Len() == 0 {
		return ""
	}
	var b bytes.Buffer
	for _, h := range set.All() {
		watches := ""
		if h.Event().AboutTool() {
			watches = h.Matcher() + " "
		}
		fmt.Fprintf(&b, "  %s %s%s\n",
			tools.Name(string(h.Event())), watches, tools.Detail(h.Command()))
	}
	return b.String()
}
