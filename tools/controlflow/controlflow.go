// Package controlflow holds the agent's control-flow primitives: spawning
// subagents, and later rewinding and committing a checkpoint.
//
// These tools are different in kind from the ones that do work. They act on the
// agent's own execution rather than on the machine, and nothing in an ordinary
// task description hints that they exist. A model that is only handed their
// schemas will not reach for them at the right moment, so every tool in this
// package implements tools.SystemPrompter and explains itself in the system
// prompt.
//
// The explanation itself is not written here. It lives in package prompt, as
// text in a file, so that it can be rewritten without touching Go and stays
// separate from the base prompt it is concatenated after — that one is meant
// to be swapped, this one is not. What this package decides is that the
// explanation reaches the model only when a tool from it is registered: a
// prompt describing a primitive the run does not have would be a standing lie.
package controlflow

import "github.com/ChrisVilches/agi/prompt"

// Base is embedded by every control-flow tool so that each one reports the
// shared guidance to the registry, satisfying tools.SystemPrompter.
//
// The registry collects each tool's contribution once, so embedding this in
// several tools would repeat the guidance. Split it per tool when there is a
// second one to split.
type Base struct{}

// SystemPrompt implements tools.SystemPrompter.
func (Base) SystemPrompt() string { return prompt.ControlFlow() }
