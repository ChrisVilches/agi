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

// rootAction runs when `agi` is invoked without a subcommand. Its single
// positional argument is the prompt to run the agent on.
func rootAction(ctx context.Context, cmd *cli.Command) error {
	out := cmd.Root().Writer
	args := cmd.Args().Slice()

	switch len(args) {
	case 0:
		fmt.Fprintln(out, "agi: give me a prompt, for example:")
		fmt.Fprintln(out, `  agi "summarize this computer's status"`)
		fmt.Fprintln(out, "Run `agi --help` for the full usage.")
		return nil
	case 1:
		return runPrompt(ctx, out, args[0], cmd.Bool("verbose"))
	default:
		// An unquoted prompt arrives as several arguments, which is the most
		// likely cause of landing here.
		return fmt.Errorf("expected a single prompt but got %d arguments; quote the prompt", len(args))
	}
}
