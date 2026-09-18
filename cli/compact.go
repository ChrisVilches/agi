package cli

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
)

// compactCommand builds the `agi compact` subcommand.
func compactCommand() *cli.Command {
	return &cli.Command{
		Name:  "compact",
		Usage: "compact the current context",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "report what would be compacted without writing anything",
			},
		},
		Action: compactAction,
	}
}

func compactAction(ctx context.Context, cmd *cli.Command) error {
	if cmd.Bool("verbose") {
		fmt.Fprintln(cmd.Root().Writer, "agi compact (verbose)")
	}
	if cmd.Bool("dry-run") {
		fmt.Fprintln(cmd.Root().Writer, "agi compact: dry run, nothing to do yet")
		return nil
	}
	fmt.Fprintln(cmd.Root().Writer, "agi compact: nothing to do yet")
	return nil
}
