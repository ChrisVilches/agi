package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ChrisVilches/agi/eventlog"
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
			Usage: "continue the conversation kept in `DIR`, which must already exist",
		},
		&cli.BoolFlag{
			Name:  "rm",
			Usage: "keep nothing: run the prompt and leave no session behind",
		},
		// Like the two above, this is about what one run leaves behind rather
		// than a preference, so it is a flag and not an environment variable:
		// a shell that quietly stopped recording every run would take away
		// the one record that survives rewinding, and say nothing about it.
		&cli.BoolFlag{
			Name:  "no-log",
			Usage: "record nothing: run without writing the session's event log",
		},
	}
}

// sessionFlags are the flags that decide what a run leaves behind: where it is
// kept, and whether what happened along the way is recorded. None of them is
// an environment variable: --session names one particular conversation, and a
// shell that silently discarded or stopped recording every run would lose
// work without saying so.
type sessionFlags struct {
	// dir is --session. Empty means agi picks a directory of its own.
	dir string
	// discard is --rm.
	discard bool
	// noLog is --no-log.
	noLog bool
}

// open turns the flags into somewhere to store the run.
//
// The directory is trimmed first, because it arrived as a shell argument and
// space around a path is never part of the path the user meant. Left alone it
// is taken literally, and a leading space turns an absolute path into a
// relative one: "   /tmp/chat" names a directory called "   " in the working
// directory, with tmp/chat inside it. That is a real path, so nothing further
// down would have objected.
func (f sessionFlags) open() (*session.Session, error) {
	dir := strings.TrimSpace(f.dir)

	switch {
	case f.dir != "" && dir == "":
		return nil, errors.New("--session was given nothing but spaces; name the directory the conversation is kept in")
	case f.discard && dir != "":
		return nil, errors.New("--rm and --session contradict each other: one throws the run away, the other keeps it")
	case f.discard:
		return session.Discarded(), nil
	case dir != "":
		return session.Open(dir)
	default:
		return session.New()
	}
}

// openLog returns where the run records what happens. It belongs to the
// session, so a run that keeps nothing records nothing: --rm leaves no
// directory, and there is nowhere for a log to go.
//
// --no-log is the other way to get the same silence, for a run that is worth
// keeping but not worth tracing. Either way the returned log answers every
// call and writes nothing, so no caller has to ask which kind it has.
func (f sessionFlags) openLog(sess *session.Session) (*eventlog.Log, error) {
	if f.noLog {
		return eventlog.Discarded(), nil
	}
	return eventlog.Open(sess.LogPath())
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
		store := sessionFlags{
			dir:     cmd.String("session"),
			discard: cmd.Bool("rm"),
			noLog:   cmd.Bool("no-log"),
		}
		return runPrompt(ctx, newOutput(out, cmd.Root().ErrWriter, cmd.Bool("verbose")), prompt, schema, store)
	default:
		// An unquoted prompt arrives as several arguments, which is the most
		// likely cause of landing here.
		return fmt.Errorf("expected a single prompt but got %d arguments; quote the prompt", len(args))
	}
}
