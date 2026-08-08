// Package controlflow holds the agent's control-flow primitives: spawning
// subagents, rewinding, and committing a checkpoint.
//
// These tools are different in kind from the ones that do work. They act on the
// agent's own execution rather than on the machine, and nothing in an ordinary
// task description hints that they exist. A model that is only handed their
// schemas will not reach for them at the right moment, so every tool in this
// package implements tools.SystemPrompter and explains itself in the system
// prompt. Prompt returns the shared guidance they all draw on.
//
// The tools themselves are not implemented yet; this package currently carries
// the guidance and the contract they will satisfy.
package controlflow

import "strings"

// Guidance explains the control-flow primitives to the model: what each one
// does, when reaching for it is the right move, and how to use it well. It is
// written to be read by the model, not by a developer.
const Guidance = `You can steer your own execution with control-flow tools. They do not touch the
user's machine; they change how you work on the problem.

Subagents. Spawning a subagent runs a fresh agent on a sub-task with its own
context, and returns only its final answer to you. Reach for one when a
sub-task would otherwise flood your context with material you do not need to
keep: searching a large codebase, reading many files to answer one question, or
exploring an approach that may turn out to be a dead end. Give the subagent a
self-contained brief, state exactly what you want back, and keep the result
small. Do not spawn one for work you could finish in a couple of steps
yourself, and do not spawn several to do the same thing in the hope one
succeeds.

Checkpoints. Committing a checkpoint records the current state as a point you
can return to. Commit one when you are about to try something you may want to
undo, and when you have just reached a state that is clearly good: a working
build, a passing test run, a completed step of a longer plan. A checkpoint is
cheap; losing a good state is not. Say in the checkpoint message what has been
achieved, not what you are about to attempt.

Rewinding. Rewinding returns to an earlier checkpoint and abandons everything
after it. Use it when an approach has clearly failed and continuing would mean
building on a broken state, or when you have wandered far from the task. Prefer
rewinding to a known-good checkpoint over trying to repair a tangled state step
by step. After rewinding, say what you learned from the attempt you abandoned,
so the same path is not taken twice.`

// Prompt returns the guidance with surrounding whitespace removed, ready to be
// embedded in a system prompt.
func Prompt() string { return strings.TrimSpace(Guidance) }

// Base is embedded by every control-flow tool so that each one reports the
// shared guidance to the registry, satisfying tools.SystemPrompter.
type Base struct{}

// SystemPrompt implements tools.SystemPrompter.
func (Base) SystemPrompt() string { return Prompt() }
