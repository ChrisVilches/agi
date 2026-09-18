package cli

import (
	"context"
	"fmt"

	"github.com/ChrisVilches/agi/model"
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
		// The schema is part of one question rather than a setting, so it has
		// no environment variable: a shell that quietly constrained every
		// answer to JSON would be a trap.
		&cli.StringFlag{
			Name:  "schema",
			Usage: `answer as a JSON object, given as 'id:int:"User ID", name:str:"Full name"'`,
		},
	}
}

// schemaFlag reads the --schema flag. An unset flag means no schema, which is
// a run that answers in prose.
func schemaFlag(cmd *cli.Command) (*model.Schema, error) {
	spec := cmd.String("schema")
	if spec == "" {
		return nil, nil
	}
	return model.ParseSchema(spec)
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
		schema, err := schemaFlag(cmd)
		if err != nil {
			return err
		}
		return runPrompt(ctx, newOutput(out, cmd.Root().ErrWriter, cmd.Bool("verbose")), args[0], schema)
	default:
		// An unquoted prompt arrives as several arguments, which is the most
		// likely cause of landing here.
		return fmt.Errorf("expected a single prompt but got %d arguments; quote the prompt", len(args))
	}
}
