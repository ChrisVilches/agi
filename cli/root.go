package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/session"
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
		&cli.StringFlag{
			Name:  "session",
			Usage: "store the run in `DIR`, continuing the conversation already there",
		},
		&cli.BoolFlag{
			Name:  "rm",
			Usage: "keep nothing: run the prompt and leave no session behind",
		},
	}
}

// sessionFlags are the two flags that decide where a run is kept. Neither is
// an environment variable: --session names one particular conversation, and a
// shell that silently discarded every run would lose work.
type sessionFlags struct {
	// dir is --session. Empty means agi picks a directory of its own.
	dir string
	// discard is --rm.
	discard bool
}

// open turns the flags into somewhere to store the run.
func (f sessionFlags) open() (*session.Session, error) {
	switch {
	case f.discard && f.dir != "":
		return nil, errors.New("--rm and --session contradict each other: one throws the run away, the other keeps it")
	case f.discard:
		return session.Discarded(), nil
	case f.dir != "":
		return session.Open(f.dir)
	default:
		return session.New()
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
		// A prompt of nothing but spaces is caught here rather than in the
		// agent, so no gateway is contacted and no session created for it.
		prompt := strings.TrimSpace(args[0])
		if prompt == "" {
			return fmt.Errorf("the prompt is empty")
		}
		schema, err := schemaFlag(cmd)
		if err != nil {
			return err
		}
		store := sessionFlags{dir: cmd.String("session"), discard: cmd.Bool("rm")}
		return runPrompt(ctx, newOutput(out, cmd.Root().ErrWriter, cmd.Bool("verbose")), prompt, schema, store)
	default:
		// An unquoted prompt arrives as several arguments, which is the most
		// likely cause of landing here.
		return fmt.Errorf("expected a single prompt but got %d arguments; quote the prompt", len(args))
	}
}
