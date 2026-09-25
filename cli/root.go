package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
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
			Usage: `answer as JSON: 'id:int:"User ID", name:str' for one object, '[id:int, name:str]' for a list of them`,
		},
		&cli.StringFlag{
			Name:  "session",
			Usage: "continue the conversation kept in `DIR`, which must already exist",
		},
		&cli.BoolFlag{
			Name:  "rm",
			Usage: "keep nothing: remove the session, and everything in it, when the run ends",
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
// --rm gets a session of its own that is deleted when the run ends, rather
// than no session at all. The run is an ordinary one while it lasts: it writes
// a snapshot, records a log, and its tools have somewhere to keep a copy of a
// file they are about to change, which is the thing that would otherwise have
// to be switched off for this one flag. What --rm means is what its name says,
// and it happens at the end.
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
		return session.Ephemeral()
	case dir != "":
		return session.Open(dir)
	default:
		return session.New()
	}
}

// openLog returns where the run records what happens. It belongs to the
// session, so a session with nowhere to write is a run that records nothing.
//
// --rm is not that case any more: it records into a real file like any other
// run, and the file goes when the directory does. What it buys is a log that
// exists while the run does, which is what --verbose and anything watching the
// directory read.
//
// --no-log is the way to ask for silence, for a run that is worth keeping but
// not worth tracing. Either way the returned log answers every call and writes
// nothing, so no caller has to ask which kind it has.
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

// stdinHoldsPrompt reports whether standard input is somewhere a prompt can
// be read from.
//
// Anything piped or redirected into standard input holds one: reading it to
// EOF returns whatever is there and returns at once, whether that is a
// here-document, a file, another program's output, or nothing at all. A
// terminal on standard input holds one only if somebody is there to type it,
// and what says so is a terminal on standard output as well — agi was started
// with its answer going to the screen, so whoever started it is watching and
// can type.
//
// `agi > result` is the case this exists for. Standard output is a file, so
// nothing about the invocation suggests a person waiting at the keyboard, but
// standard input is still the terminal and reading it waits for a Ctrl-D that
// never comes. Treating that as no prompt at all turns a run that hung
// forever into the empty prompt it always was.
func stdinHoldsPrompt(stdin io.Reader, stdout io.Writer) bool {
	return holdsPrompt(isTerminal(stdin), isTerminal(stdout))
}

// holdsPrompt is the rule itself, kept separate from stdinHoldsPrompt so every
// branch can be tested without a terminal.
func holdsPrompt(stdinIsTerminal, stdoutIsTerminal bool) bool {
	return !stdinIsTerminal || stdoutIsTerminal
}

// promptFrom settles where the prompt came from. One positional argument is
// the prompt; no argument at all means it arrives on standard input and is
// read to EOF, so a here-document, a file, or the output of another program
// can be the question. Nothing about the run changes afterwards: the two are
// two spellings of the same argument, which is why --schema and the session
// flags need to know nothing about which was used.
//
// The third spelling is no prompt at all, which is what fromStdin being false
// means: there was no argument, and standard input is not somewhere a prompt
// could come from. Nothing is read, because reading is the thing that would
// hang.
//
// However it arrived the result is trimmed, and an empty prompt is refused
// here rather than in the agent, so no gateway is contacted and no session is
// created for a question nobody asked.
func promptFrom(args []string, stdin io.Reader, fromStdin bool) (string, error) {
	raw := ""
	switch {
	case len(args) == 1:
		raw = args[0]
	case fromStdin:
		read, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("reading the prompt from standard input: %w", err)
		}
		raw = string(read)
	}

	prompt := strings.TrimSpace(raw)
	if prompt == "" {
		return "", errors.New("the prompt is empty")
	}
	return prompt, nil
}

// rootAction runs when `agi` is invoked without a subcommand. Its single
// positional argument is the prompt to run the agent on, or standard input if
// there is none.
//
// Standard input is read the same way whoever is on the other end of it: a
// pipe, a file, or a person typing the prompt at a terminal and ending it with
// Ctrl-D. Nothing is printed to announce that, because the run has not started
// yet and anything agi said here would be in the way of whoever already knew —
// which is also why a keyboard nobody is at is not waited on at all.
func rootAction(ctx context.Context, cmd *cli.Command) error {
	out := cmd.Root().Writer
	args := cmd.Args().Slice()

	if len(args) > 1 {
		// An unquoted prompt arrives as several arguments, which is the most
		// likely cause of landing here.
		return fmt.Errorf("expected a single prompt but got %d arguments; quote the prompt", len(args))
	}

	stdin := cmd.Root().Reader
	prompt, err := promptFrom(args, stdin, stdinHoldsPrompt(stdin, out))
	if err != nil {
		return err
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
}
