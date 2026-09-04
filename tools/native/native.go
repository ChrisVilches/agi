// Package native holds the tools that are part of the agent itself: spawning
// subagents, ending the run without an answer, loading a skill, and later
// rewinding and committing a checkpoint.
//
// These tools are different in kind from the ones that do work. A workspace
// tool reaches out to the machine and would do the same thing for any program
// that ran it; a native tool acts on the run — on how it proceeds, or on what
// the agent knows — and means nothing outside one. Some of them steer
// execution and some do not, which is why the package is not named after
// control flow: what they have in common is being part of the agent rather
// than being branches in it.
//
// Nothing in an ordinary task description hints that any of them exist, and a
// model handed only their schemas will not reach for them at the right moment,
// so every tool in this package is explained in the system prompt.
//
// Nearly all of them explain themselves, by implementing tools.SystemPrompter.
// The exception is loadskill, whose explanation is only half of what the model
// needs — the other half is the list of skills this particular run was given,
// which is not the tool's to write. The two are joined where the prompt is
// assembled, from the same list the tool is built from, so the rule below
// holds for it as it does for the rest.
//
// The explanations are not written here. They live in package prompt, as text
// in files, so that they can be rewritten without touching Go and stay
// separate from the base prompt they are concatenated after — that one is
// meant to be swapped, these are not. What this package decides is that each
// explanation reaches the model only when its own tool is registered: a prompt
// describing a tool the run does not have would be a standing lie. The
// guidance is therefore per tool and not shared, so that adding a second
// native tool to a run adds a section rather than repeating a preamble.
package native
