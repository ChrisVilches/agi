// Package workspace holds the tools that do ordinary work on the user's
// machine: running commands, changing files, and making somewhere to put
// things.
//
// The name says what they have in common: they act on the workspace the user
// pointed agi at, not on the agent itself. They exist for convenience and can
// be switched off — a user may want an agent with none of them, or with their
// own tools in their place. Because a run may not include them, nothing in
// this package may contribute to the system prompt. Everything the model needs
// in order to call one correctly belongs in its Definition.
//
// That rule is what makes the tools here peers rather than a hierarchy. A tool
// written in Go and compiled into agi is no more privileged than a shell
// command: bash, patchfile, and mktemp are the same kind of thing, described
// the same way, and any of them may be absent. So a description may not lean
// on another tool being registered — it says what its own tool is for and what
// it is not for, and the model assembles the comparison from the tools it was
// actually given.
package workspace
