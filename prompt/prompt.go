// Package prompt holds the system prompts agi runs on, each in a file of its
// own so that the text can be read and rewritten as text rather than edited
// around Go syntax.
//
// They are kept apart because they change independently. Base is the
// instruction a run starts from, and it is the one that is meant to be
// swapped: it says nothing about agi and a different one would serve just as
// well. ControlFlow explains the primitives that act on the agent itself, and
// it is not interchangeable with anything — whatever base a run uses, this is
// concatenated after it, because a model that has not been told what the
// control-flow tools are for will not reach for them at the right moment.
// Subagent is what a spawned agent runs under instead of Base.
//
// The files are compiled in rather than read from disk. Nothing here is
// configuration, and agi does not go looking for files at run time.
package prompt

import (
	_ "embed"
	"strings"
)

//go:embed base.md
var base string

//go:embed controlflow.md
var controlFlow string

//go:embed subagent.md
var subagent string

// Base returns the instruction an ordinary run starts from. It is deliberately
// plain: it describes no tool and assumes no task, so that replacing it changes
// the agent's character without breaking anything.
//
// TODO: let the user supply their own in place of this one.
func Base() string { return strings.TrimSpace(base) }

// ControlFlow returns the explanation of the control-flow primitives — when
// reaching for one is the right move, and how to use it well. It reaches the
// system prompt through the tools that implement it, so it is never sent to a
// model that has not been given them.
func ControlFlow() string { return strings.TrimSpace(controlFlow) }

// Subagent returns the instruction a spawned agent runs under, in place of
// Base. Its whole job is to make the subagent's reply a final answer: it is
// read by the agent that spawned it and there is no conversation after it, so
// questions and offers of further help are wasted words.
func Subagent() string { return strings.TrimSpace(subagent) }
