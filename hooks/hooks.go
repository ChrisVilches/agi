// Package hooks runs programs the user wrote at two fixed moments in a run:
// before a tool call is made, and after one has produced a result.
//
// A hook is the user's policy and not agi's. It is told which tool is about
// to run and with what, and it answers by its exit status and, optionally, a
// JSON object on standard output: let the call through, change its arguments,
// refuse it, change what the model reads afterwards, add context, or stop the
// agent that made the call. agi has no opinion about any of it, and a run
// given no hooks behaves exactly as it did before they existed.
//
// The protocol is Claude Code's, deliberately, so that a hook script written
// for that program usually works here. It is a flavor of it and not a copy —
// agi has no interactive permission prompt, no plugins and no transcript file
// — and every place the two differ is listed in docs/hooks-specification.md,
// which is the whole of the feature and the document to read before changing
// anything here.
//
// This package knows nothing about the agent loop, the CLI, or the event log.
// It is handed a tool's name and the raw JSON arguments the model produced,
// and it hands back either the data as the hooks left it or an error saying
// what a hook decided.
package hooks

import (
	"encoding/json"
	"regexp"
)

// Event names a moment in a run at which hooks run.
//
// There are two, and the strings are the format: they are what a manifest
// writes and what a hook reads back in hook_event_name, so renaming one makes
// every manifest written before the rename read differently.
type Event string

const (
	// PreToolUse runs before a tool call is made. It can refuse the call or
	// change the arguments it is made with.
	PreToolUse Event = "PreToolUse"
	// PostToolUse runs after a call has produced a result, and can change
	// what the model reads. It does not run for a call that failed: Claude
	// Code has a separate event for that one and agi does not implement it.
	PostToolUse Event = "PostToolUse"
)

// events is every event a manifest may name, which is what an unknown name in
// one is checked against. It is in declaration order rather than sorted,
// because that is the order they happen in and the order a message about them
// reads best in.
var events = []Event{PreToolUse, PostToolUse}

// Hook is one entry of a manifest: which calls it runs for, and what to run.
//
// Everything in it was settled when the manifest was read. The regular
// expressions are compiled, the program is resolved, and what is left for a
// call is deciding whether it matches and then running it.
//
// What is deliberately not settled is whether the program can be run. The
// executable bit can change while a run is going, so checking it once at the
// start would be a promise agi cannot keep; the only honest check is the
// attempt itself.
type Hook struct {
	// event is which of the two moments this hook runs at.
	event Event
	// matcher accepts the tool's name. A nil matcher accepts every tool,
	// which is what a manifest says by leaving it out, writing it empty, or
	// writing "*".
	matcher *regexp.Regexp
	// conditions are one regular expression per named tool parameter,
	// combined with OR: the hook runs if at least one of them matches. An
	// empty map imposes no condition.
	conditions map[string]*regexp.Regexp

	// command is the program to run, or a shell command line when args is
	// nil. In the first case it is already absolute; in the second it is the
	// user's own text and is handed to a shell as it was written.
	command string
	// args are the arguments the program is run with, and their presence is
	// what says this is the exec form rather than the shell form. A hook with
	// no arguments and no shell is spelled with an empty, non-nil args.
	args []string
	// dir is the folder the manifest is in, which is where the program runs.
	// It is what makes a relative path in a shell command line mean what its
	// author meant, and what makes a folder of hooks something you can move.
	dir string
}

// Event returns the moment the hook runs at.
func (h *Hook) Event() Event { return h.event }

// Command returns the program the hook runs, or the shell command line.
func (h *Hook) Command() string { return h.command }

// Matcher returns the tool-name pattern as the manifest wrote it, or "*" for
// a hook that runs for every tool. It is for showing the user what the run
// was armed with, which is why it renders the absent case rather than
// reporting it.
func (h *Hook) Matcher() string {
	if h.matcher == nil {
		return "*"
	}
	return h.matcher.String()
}

// Set is every hook a run was given.
//
// It is a Set and not a Guard, which is what an earlier draft called it.
// Guarding is one of the three things a hook does, and the name would have
// been wrong for the other two: a hook that rewrites a call's arguments,
// redacts a secret out of a result, or adds a line of context guards nothing.
// Naming the whole after the part is the widening this repository refuses
// elsewhere, and it is why the native tools are called native rather than
// control flow.
//
// Registry was the near miss, echoing tools.Registry one level over. It loses
// because a registry here is registered into and looked up by name, and
// nothing ever looks a hook up: the model never addresses one, which is also
// why two hooks of one name are not an error.
type Set struct {
	// hooks are in the order they will run: the order the folders were given
	// on the command line, and within a folder the order the manifest lists
	// them. Order is the user's and is never rearranged, because one hook's
	// output is the next one's input.
	hooks []*Hook
}

// None is a run that was given no hooks. Every call passes through untouched,
// so no caller has to ask whether there are any — the same bargain a
// discarded event log makes with a run that asked not to be recorded.
func None() *Set { return &Set{} }

// Len returns how many hooks the run was given.
func (s *Set) Len() int { return len(s.hooks) }

// All returns every hook, in the order they run. It is for saying what a run
// was armed with, which is one of the things that has to be known to
// reproduce it.
func (s *Set) All() []*Hook { return s.hooks }

// matching returns the hooks that run for one call, in order.
//
// A hook matches when its matcher accepts the tool's name and its conditions
// are satisfied. Both halves are the manifest's, and both default to
// accepting, so a hook that says neither runs for every call of its event.
func (s *Set) matching(event Event, tool string, args json.RawMessage) []*Hook {
	var found []*Hook
	for _, h := range s.hooks {
		if h.event == event && h.matches(tool, args) {
			found = append(found, h)
		}
	}
	return found
}
