// Package controlflow holds the agent's control-flow primitives: spawning
// subagents, ending the run without an answer, and later rewinding and
// committing a checkpoint.
//
// These tools are different in kind from the ones that do work. They act on the
// agent's own execution rather than on the machine, and nothing in an ordinary
// task description hints that they exist. A model that is only handed their
// schemas will not reach for them at the right moment, so every tool in this
// package implements tools.SystemPrompter and explains itself in the system
// prompt.
//
// The explanations are not written here. They live in package prompt, as text
// in files, so that they can be rewritten without touching Go and stay
// separate from the base prompt they are concatenated after — that one is
// meant to be swapped, these are not. What this package decides is that each
// explanation reaches the model only when its own tool is registered: a prompt
// describing a primitive the run does not have would be a standing lie. The
// guidance is therefore per tool and not shared, so that adding a second
// primitive to a run adds a section rather than repeating a preamble.
package controlflow
