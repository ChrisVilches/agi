// Package commands wires up the agi command tree.
package cli

import (
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
		Usage:                 "agi command line tool",
		Version:               version,
		EnableShellCompletion: true,
		Flags:                 rootFlags(),
		Action:                rootAction,
	}
}
