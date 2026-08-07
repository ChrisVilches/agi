package cli

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
)

// rootFlags are the flags accepted by `agi` itself. Because they live on the
// root command they are also inherited as persistent flags by subcommands.
func rootFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{
			Name:    "verbose",
			Aliases: []string{"v"},
			Usage:   "enable verbose output",
			Sources: cli.EnvVars("AGI_VERBOSE"),
		},
	}
}

// rootAction runs when `agi` is invoked without a subcommand.
func rootAction(ctx context.Context, cmd *cli.Command) error {
	// The root takes no positional arguments, so anything left over is a
	// mistyped subcommand. Without this it would be silently ignored.
	if args := cmd.Args().Slice(); len(args) > 0 {
		return fmt.Errorf("unknown command %q, see `agi --help`", args[0])
	}

	if cmd.Bool("verbose") {
		fmt.Fprintf(cmd.Root().Writer, "agi %s (verbose)\n", cmd.Root().Version)
	}
	fmt.Fprintln(cmd.Root().Writer, "agi: nothing to do yet")
	return nil
}
