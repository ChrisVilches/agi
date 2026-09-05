// Package commands wires up the agi command tree.
package cli

import (
	"context"

	"github.com/urfave/cli/v3"
)

// NewApp builds the root command, which doubles as the application itself in
// urfave/cli v3. There are no subcommands: running agi is running the agent on
// a prompt, and everything else is a flag on that run.
//
// TODO: implement a subcommand for compacting the context.
func NewApp(version string) *cli.Command {
	return &cli.Command{
		Name:                  "agi",
		Usage:                 "run an LLM agent on a prompt, with tools that act on this machine",
		Version:               version,
		EnableShellCompletion: true,
		// A flag that may be given more than once takes one value each time
		// and nothing else. urfave/cli otherwise splits a single value on
		// commas as well, which would quietly turn one path with a comma in
		// it into two paths that do not exist — and a repeatable flag already
		// has a spelling for two values, so the second one buys nothing and
		// can only be a trap.
		DisableSliceFlagSeparator: true,
		Flags:                     rootFlags(),
		Action:                    rootAction,
		OnUsageError:              usageError,
	}
}

// usageError hands a flag's refusal back unchanged, and is the whole of what
// this hook is here to do.
//
// Without it urfave/cli answers a bad flag by writing `Incorrect Usage:`, the
// message, and the entire help text to standard error, and then returning the
// same error for main to print a second time. That is the one rule agi keeps
// about errors broken three ways over: an error is printed once, on standard
// error, with nothing in front of it, and main is the only place that prints
// one. Returning it here skips all of that, so a refusal a flag's Validator
// made reads like every other refusal agi makes.
//
// The help is no loss. A user who mistyped one flag is told which flag and
// what was wrong with it, and a user who wants the list can ask for it; a
// screen of every flag agi has is what buries the one sentence that matters.
func usageError(_ context.Context, _ *cli.Command, err error, _ bool) error {
	return err
}
