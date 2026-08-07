// Package commands wires up the agi command tree.
package cli

import (
	"github.com/urfave/cli/v3"
)

// NewApp builds the root command, which doubles as the application itself in
// urfave/cli v3. Running it with no subcommand invokes the root action; running
// it with "compact" invokes the compact subcommand.
func NewApp(version string) *cli.Command {
	return &cli.Command{
		Name:                  "agi",
		Usage:                 "agi command line tool",
		Version:               version,
		EnableShellCompletion: true,
		Flags:                 rootFlags(),
		Action:                rootAction,
		Commands: []*cli.Command{
			compactCommand(),
		},
	}
}
