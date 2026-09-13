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
	h.report(req, hooks.PreToolUse, report)
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
	h.report(req, hooks.PostToolUse, report)
	if err != nil {
		return output, report.Context, err
	}
	return replaced, report.Context, nil
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
func (h hooked) report(req model.ToolRequest, moment hooks.Event, report hooks.Report) {
	for _, ran := range report.Ran {
		kind := eventlog.HookResult
		if ran.Decided == hooks.Failed {
			kind = eventlog.HookFailure
		}
		h.log.Record(eventlog.Event{
			Kind:      kind,
			Turn:      h.turn,
			Tool:      req.Name,
			Hook:      ran.Hook,
			Moment:    string(moment),
			Decided:   string(ran.Decided),
			Reason:    ran.Problem,
			RequestID: req.ID,
			Elapsed:   ran.Elapsed.Milliseconds(),
		})
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
// A hook can stop an agent or silently rewrite what a tool was asked to do,
// which makes it exactly the kind of thing somebody watching a run needs to
// have been told about beforehand. It is one line each: when it runs, which
// tools it is watching, and what it runs.
func hookList(set *hooks.Set) string {
	if set.Len() == 0 {
		return ""
	}
	var b bytes.Buffer
	for _, h := range set.All() {
		fmt.Fprintf(&b, "  %s %s %s\n",
			tools.Name(string(h.Event())), h.Matcher(), tools.Detail(h.Command()))
	}
	return b.String()
}
