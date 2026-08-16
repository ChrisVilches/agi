// Package prompt holds the system prompts agi runs on, each in a file of its
// own so that the text can be read and rewritten as text rather than edited
// around Go syntax.
//
// They are kept apart because they change independently. Base is the
// instruction a run starts from, and it is the one that is meant to be
// swapped: it says nothing about agi and a different one would serve just as
// well. Subagent is what a spawned agent runs under instead of Base.
//
// The rest explain the primitives that act on the agent itself, and they are
// not interchangeable with anything: whatever base a run uses, the guidance
// for the control-flow tools it was given is concatenated after it, because a
// model that has not been told what they are for will not reach for them at
// the right moment. There is one file per tool rather than one document for
// all of them, because each reaches the model only when its own tool is
// registered — guidance for a primitive a run does not have would be a
// standing lie. That is also why each of them stands on its own instead of
// sharing a preamble: a run given one control-flow tool and not another still
// reads a whole explanation.
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

//go:embed controlflow-subagents.md
var subagents string

//go:embed subagent.md
var subagent string

// Base returns the instruction an ordinary run starts from. It is deliberately
// plain: it describes no tool and assumes no task, so that replacing it changes
// the agent's character without breaking anything.
//
// TODO: let the user supply their own in place of this one.
func Base() string { return strings.TrimSpace(base) }

// Subagents returns the guidance for the subagents tool — when spawning agents
// is the right move, and how to brief them. It reaches the system prompt
// through the tool itself, so it is never sent to a model that has not been
// given it.
func Subagents() string { return strings.TrimSpace(subagents) }

// Subagent returns the instruction a spawned agent runs under, in place of
// Base. Its whole job is to make the subagent's reply a final answer: it is
// read by the agent that spawned it and there is no conversation after it, so
// questions and offers of further help are wasted words.
func Subagent() string { return strings.TrimSpace(subagent) }
