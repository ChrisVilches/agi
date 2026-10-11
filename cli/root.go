package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ChrisVilches/agi/eventlog"
	"github.com/ChrisVilches/agi/hooks"
	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/prompt"
	"github.com/ChrisVilches/agi/session"
	"github.com/ChrisVilches/agi/tools/native"
	"github.com/ChrisVilches/agi/tools/workspace"
	"github.com/urfave/cli/v3"
)

// rootFlags are the flags accepted by `agi` itself, which is the whole of the
// command line: there are no subcommands to inherit them.
//
// A flag that can say for itself what a usable value is says it here, in a
// Validator, and the rest of the package never sees a value that failed one.
// That is every rule small enough to be one comparison — a number below a
// floor, a path that is nothing but space — and putting it on the flag is
// worth doing for two reasons. It is read beside the usage line it belongs
// to, which is where somebody looking for what the flag accepts is already
// looking; and the refusal urfave builds around it names the flag and quotes
// what it was given, so the message has only to supply the part urfave cannot
// know.
//
// What stays out of here is everything that is not one rule about one flag.
// Two flags contradicting each other is one: --rm and --session are refused
// together in sessionFlags.open, because no Validator is shown the other flag.
// So is anything where the checking and the work are the same act — reading
// the files --sys names, reading the folders --resources names, parsing
// --schema — since a Validator that did it would do it a second time for the
// value to be used, and a Validator that only half did it would let through
// what the real read refuses. Those have functions of their own below, worded
// for what they are about.
func rootFlags() []cli.Flag {
	return []cli.Flag{
		// The model is the one setting that is a flag and an environment
		// variable both, and the two are not the same statement said twice.
		// AGI_DEFAULT_MODEL is the model this machine runs on, chosen once by
		// whoever set up the gateway or the provider key and wanted again by
		// every run after. This names the model one run runs on, which is a
		// question the work raises rather than the setup: a prompt worth a
		// larger model than the day's, a trivial one worth a cheaper model,
		// or the same prompt put to two models to see which answers it
		// better. Neither spelling can say the other's thing — a variable
		// re-exported per run is a setting being used as an argument, and a
		// flag on every invocation is an argument being used as a setting.
		//
		// Given, it wins; left off, the variable answers; given neither, the
		// run is refused in openBackend, which is where both are read and
		// where why agi never picks one is written. The name is sent exactly
		// as it is typed, and is not checked against `agi models` first: the
		// listing is a good guide and not a guarantee — `deepseek-chat` is
		// absent from DeepSeek's and works anyway — so the endpoint is what
		// decides, and a name it refuses comes back as its own refusal.
		&cli.StringFlag{
			Name:  "model",
			Usage: "run on `MODEL`, overriding " + EnvDefaultModel + ", spelled exactly as `agi models` prints it",
			// As with the path flags, a value of nothing but space is a
			// quoting accident rather than a way of asking for the variable
			// back, and it is refused here so that it cannot reach the
			// endpoint as an empty model name.
			Validator: func(name string) error {
				if strings.TrimSpace(name) == "" {
					return errors.New("a value of nothing but spaces names no model; give one as `agi models` prints it")
				}
				return nil
			},
		},
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
			Usage: `answer as JSON: 'id:int:"User ID", name:str' for one object, '[id:int, name:str]' for a list of them; a number can name the range it is in, as in 'age:int(0..120)'`,
		},
		// What a process's exit status means is the contract between agi and
		// whatever ran it, so it is a flag and never an environment variable,
		// for the reason --schema is not one: a shell that quietly made every
		// run able to fail would break the scripts written against the agi
		// that could not, and the run it changed would look exactly like one
		// it had not.
		//
		// Off by default, which is the whole of why it is a flag at all. A run
		// that exits 0 having answered is what agi has always done, and a run
		// that asks the model to grade its own work spends a request on the
		// question — worth it for a script that has to branch on the result,
		// and worth nothing to a person reading the answer on the screen.
		&cli.BoolFlag{
			Name:  "exit-code",
			Usage: "exit 1 when the task was not completed, with the reason on standard error; the answer still goes to standard output",
		},
		// The base prompt decides how the agent behaves, which is why it is a
		// flag and not an environment variable either: a shell that silently
		// replaced the instruction every run starts from would change every
		// answer agi gave and say nothing about it, and the run it changed
		// would look exactly like one that had not been changed.
		//
		// It may be given more than once, because an instruction is written in
		// parts that are worth keeping apart — how to behave, what the machine
		// is, what the house style is — and a user who has them in three files
		// would otherwise have to keep a fourth with all three pasted into it.
		&cli.StringSliceFlag{
			Name:      "sys",
			Usage:     "begin the system prompt with the instruction in `FILE` instead of the built-in base prompt; repeatable, and the files are joined in the order given",
			Validator: namesPaths("file the instruction is in"),
		},
		// Everything the user brings to a run lives in a folder, and the
		// folder is named here: the tools they wrote, the hooks that guard a
		// call, and the skills the agent may draw on. It is a flag and not an
		// environment variable for the reason --sys is one — what the agent
		// can do to the machine and what it is told are decided by the run
		// and not by the shell it was started from, and a variable that
		// quietly added tools or armed a guard on every run would change what
		// happened and say nothing about it.
		//
		// It is repeatable because a folder of resources is a thing kept
		// somewhere and reused — one the user wrote, one a colleague wrote,
		// one that came with a project — and the alternative is a folder of
		// symbolic links to the folders they actually meant. The order is
		// kept, because it decides the order the hooks run in and the order
		// the skills are listed to the model.
		//
		// Naming a folder brings in everything in it, hooks included, which
		// is the one thing three flags could say that this one cannot: the
		// folder used to have to be named to --hooks before the guards in it
		// were armed. What replaces that promise is that a folder is the
		// user's own and they named it — and the alternative, an opt-in per
		// kind, is three flags again under another spelling.
		&cli.StringSliceFlag{
			Name:      "resources",
			Usage:     "bring what is in `DIR` to the run: the tools its tools.json declares, the hooks its hooks.json declares, and the skills kept under it; repeatable, and the folders are read in the order given",
			Validator: namesPaths("folder the resources are in"),
		},
		// How long a run may go on before it gives up is a property of the
		// work rather than a preference, so it is a flag and not an
		// environment variable, for the reason --schema and --sys are not: a
		// shell that quietly lowered the cap would make runs give up that
		// would otherwise have answered, and the run it changed would look
		// exactly like one it had not.
		//
		// A cap below the smallest a run could finish in is refused by the
		// flag, which is where the whole of that rule fits: it is one
		// comparison against a constant and needs nothing the command line
		// has not already been given. Refusing it there is refusing it before
		// a gateway is contacted or a session is created, which is the point
		// — the alternative is a run that was never going to answer, spending
		// the user's money on the turns it was allowed and ending with the
		// message it could have been given for nothing.
		&cli.IntFlag{
			Name:  "max-turns",
			Value: defaultMaxTurns,
			Usage: "give up after `N` exchanges with the model without a final answer",
			// The message names the smallest cap and leaves it there, since a
			// user who asked for one turn wants the number and not a lecture.
			// It does not repeat the flag or the number given: urfave puts
			// both in front of whatever comes back from here.
			Validator: func(turns int) error {
				if turns < minMaxTurns {
					return fmt.Errorf("too few turns to answer anything in; the smallest is %d", minMaxTurns)
				}
				return nil
			},
		},
		// How wide the agent may fan out is a property of the work as well, so
		// it is a flag for the reason --max-turns is one: a shell that quietly
		// narrowed every fan-out would change how the work got done and say
		// nothing about it.
		//
		// There is no ceiling, and that is deliberate. What a wide fan-out
		// costs is conversations open with the gateway at once, which is the
		// user's money and the user's judgement; agi knows neither the work
		// nor the gateway's limits, so a number it invented would be a guess
		// standing in the way of somebody who meant what they typed. What is
		// refused is the floor, because a cap below the fewest tasks a call
		// may carry is a tool no call could ever be made to.
		&cli.IntFlag{
			Name:  "max-subagents",
			Value: defaultMaxSubagents,
			Usage: "spawn at most `N` subagents in one call",
			// As with --max-turns, the message supplies only the part urfave
			// cannot know: the flag and the value given are already in front
			// of whatever comes back from here.
			Validator: func(most int) error {
				if most < native.MinTasks {
					return fmt.Errorf("too few subagents for a call to be worth making; the smallest is %d", native.MinTasks)
				}
				return nil
			},
		},
		// How long a hook may take is a property of the machine the hooks
		// run on rather than a preference, so it is a flag for the reason
		// --max-turns is one: a shell that quietly lowered it would make
		// guards fail that would otherwise have passed, and a hook that
		// fails to finish stops the agent.
		//
		// It is a duration where the two caps are plain numbers, because
		// those count things that have no unit and this measures time. A
		// bare 30 would leave the user to guess at seconds or milliseconds.
		&cli.DurationFlag{
			Name:  "hook-timeout",
			Value: hooks.DefaultTimeout,
			Usage: "give a hook `DURATION` to finish before the agent it is guarding stops",
			// As with the caps, the message supplies only the part urfave
			// cannot know: the flag and the value given are already in front
			// of whatever comes back from here.
			Validator: func(d time.Duration) error {
				if d < hooks.MinTimeout {
					return fmt.Errorf("too little time for a hook that was going to answer; the smallest is %s", hooks.MinTimeout)
				}
				return nil
			},
		},
		// Terminals are off unless asked for, and the flag is the asking. What
		// a terminal does cannot be guarded the way a bash command can: a hook
		// sees what is typed and not what runs — a command typed in pieces, a
		// line recalled from history, a shell inside ssh or a REPL — so
		// enabling it lets the agent past every hook written for bash. That
		// is a decision about what the agent may do to the machine, which is
		// why it is a flag and never an environment variable, for the reason
		// --resources is not one: a shell that quietly enabled it would change
		// what the agent can do, and the run would look like any other.
		//
		// It is one flag rather than a switch and a cap, because a cap on
		// terminals means nothing in a run without them, and two flags let the
		// user write that. Given alone it is the default cap; given a number,
		// written --enable-terminals=N, it is that cap. The floor is one, the
		// fewest a tool for opening them could be given, and there is no
		// ceiling: every terminal is a process on the user's own machine, and
		// how many of those are worth having is theirs to judge.
		&cli.GenericFlag{
			Name:  "enable-terminals",
			Value: &terminalCap{},
			// The placeholder is [=N] because the number has to be attached
			// with an equals sign; a placeholder that read as a separate word
			// would teach the one spelling that does not work.
			Usage: fmt.Sprintf("give the agent interactive terminals it can keep open across calls, "+
				"at most %d per agent, or N when written `[=N]`; anything typed in one bypasses "+
				"hooks written for bash, since a hook sees the keys and not what they run",
				workspace.DefaultMaxTerminals),
			// As with the other caps, the message supplies only the part urfave
			// cannot know: the flag and the value given are already in front
			// of whatever comes back from here.
			Validator: func(v cli.Value) error {
				if c, ok := v.(*terminalCap); ok && c.most < 1 {
					return errors.New("an agent given terminals has to be able to open one; the smallest is 1")
				}
				return nil
			},
		},
		// Giving this flag is also what makes a run stop saying where it is
		// being kept, and the usage says so because that is where somebody
		// would look for it. The rule is in sessionFlags.chose, with why it
		// is a rule and not a flag of its own.
		&cli.StringFlag{
			Name:      "session",
			Usage:     "continue the conversation kept in `DIR`, which must already exist; a run given one does not say where it is being kept, since whoever gave it knows",
			Validator: namesPath("directory the conversation is kept in"),
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

// namesPath builds the Validator for a flag that names one path, refusing a
// value that is nothing but whitespace. What the path is expected to hold is
// given in the caller's own words — "file the instruction is in" — so the
// refusal talks about the flag the user typed rather than about paths in
// general.
//
// A path arrives as a shell argument, and space around one is never part of
// the path meant, so every flag that takes one trims it before it is used.
// What is left of a value that was nothing but space is nothing at all, and
// that is a mistake — a quoting accident, a shell variable that expanded to
// nothing — rather than a way of asking for the flag's default. Left
// unrefused it becomes a working directory relative path: "   /tmp/chat" is a
// directory called "   " with tmp/chat inside it, which is a real path that
// nothing further down would have objected to.
//
// The rule is one trim and belongs on the flag for that reason. It is the
// same rule for every path flag agi has, which is why it is written once here
// rather than four times in the functions that read them.
func namesPath(holds string) func(string) error {
	return func(path string) error {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("a value of nothing but spaces names nothing; give the %s", holds)
		}
		return nil
	}
}

// namesPaths is namesPath for a flag that may be given more than once.
//
// urfave hands a slice Validator everything the flag has collected so far
// rather than the one value just read, so the values already checked are
// checked again on each new one. That is a trim per path per path, over lists
// a handful of paths long, which is cheaper than remembering how far the last
// call got.
func namesPaths(holds string) func([]string) error {
	one := namesPath(holds)
	return func(paths []string) error {
		for _, path := range paths {
			if err := one(path); err != nil {
				return err
			}
		}
		return nil
	}
}

// trimmedPaths is what a repeatable path flag was given, with the space a
// shell left around each value removed, for the reason namesPath explains.
//
// None of what comes back can be blank, because the flag's Validator refused a
// blank value while the command line was being read. The functions below can
// therefore read what they were given without checking it first, which is the
// point of putting the rule on the flag.
func trimmedPaths(cmd *cli.Command, flag string) []string {
	given := cmd.StringSlice(flag)
	paths := make([]string, 0, len(given))
	for _, raw := range given {
		paths = append(paths, strings.TrimSpace(raw))
	}
	return paths
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

// chose reports whether agi picked the session's directory itself, which is
// the whole of whether a run says where it is being kept.
//
// The line exists for one reader: somebody who could not otherwise know the
// path, because agi invented it. A run given --session has no such reader.
// What would be printed is the value of the flag, kept exactly as it was
// typed — session.Open stores the path it is handed and normalizes nothing —
// so the line would be a string the user wrote, read back to them from two
// lines below where they wrote it.
//
// That is why this is a rule and not a flag. A flag would ask the user to
// tell agi something agi can already see, and the one case for it is the
// case this rule covers: a program that starts runs — agi-repl, a shell
// function that pins a directory — passes --session by definition, so it is
// quiet without being told to be, and it is the one that knows the path and
// can say it in its own words if it wants to.
//
// It asks the flags rather than the session because the question is about
// what the user typed, not about the filesystem. A directory agi made an hour
// ago, named to --session now, is one the user holds: they typed it. Nothing
// about the directory itself could tell those two runs apart.
//
// A --rm run names no directory, so agi chose that one too and says it — once,
// at the top, for the reason runPrompt gives. The flags contradict each other
// anyway, which open refuses.
func (f sessionFlags) chose() bool {
	return strings.TrimSpace(f.dir) == ""
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
// The directory is trimmed, because it arrived as a shell argument and space
// around a path is never part of the path the user meant. A value that is
// nothing but space was refused by the flag's own Validator, so what is
// trimmed here is a real path with room around it.
//
// What is left for this function is the one rule no Validator could have made:
// --rm and --session contradict each other, and a Validator is shown its own
// flag and nothing else.
func (f sessionFlags) open() (*session.Session, error) {
	dir := strings.TrimSpace(f.dir)

	switch {
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
// session, and every session has a directory, so every run has somewhere to
// record unless it asked not to.
//
// --rm is not an exception: it records into a real file like any other run,
// and the file goes when the directory does. What it buys is a log that exists
// while the run does, which is what --verbose and anything watching the
// directory read.
//
// --no-log is the one way to ask for silence, for a run that is worth keeping
// but not worth tracing. The log it returns answers every call and writes
// nothing, so no caller has to ask which kind it has.
func (f sessionFlags) openLog(sess *session.Session) (*eventlog.Log, error) {
	if f.noLog {
		return eventlog.Discarded(), nil
	}
	return eventlog.Open(sess.LogPath())
}

// schemaFlag reads the --schema flag. An unset flag means no schema, which is
// a run that answers in prose.
//
// Parsing is the whole of the checking, which is why there is no Validator on
// the flag: one would have to parse the spec to find out whether it parses,
// and the schema itself would then be parsed a second time here for the run to
// use. A Validator doing less than the parse would be the worse arrangement
// still — it would pass a spec this function goes on to refuse, in a message
// the user was told the flag had already accepted.
func schemaFlag(cmd *cli.Command) (*model.Schema, error) {
	spec := cmd.String("schema")
	if spec == "" {
		return nil, nil
	}
	return model.ParseSchema(spec)
}

// basePromptFlag reads --sys: the instruction the run starts from, which is
// whatever is in the files it names, or the built-in base when it names none.
//
// What it returns is the base of the system prompt and not the whole of it,
// but "base" is agi's word and the flag is spelled for the user, so what a
// refusal here says is that the system prompt could not be read. The
// difference matters to this package and to nobody typing the flag.
//
// Any file will do, whatever it is called. The prompts agi ships happen to be
// Markdown because prose is easier to write that way, not because anything
// reads them as Markdown — what is sent to the model is the file's text, so
// the extension says nothing and is not looked at.
//
// Several files are one instruction, joined by a blank line in the order they
// were given on the command line, so that each arrives as its own paragraph
// and none of them runs into the next. The order is the user's and is never
// rearranged: an instruction is prose, and prose read in a different order
// than it was written says something else. Nothing is inserted between them
// either — no heading, no separator — because this is one document the user
// wrote in parts, not several documents agi is joining. That is what the
// === name === delimiters ahead of the tools' guidance are for, and they
// exist because those really are separate explanations.
//
// Each file's text is trimmed, the way prompt.Base() is, since what comes out
// of here is sent exactly as it is; and a file with nothing but whitespace in
// it is refused rather than skipped. A run has to start from some instruction,
// and a file that turned out to be empty is far likelier to be the wrong path,
// or one the user has not written yet, than a deliberate contribution of
// nothing. The refusal happens here, before a gateway is contacted or a
// session is created, for the same reason an empty prompt does.
func basePromptFlag(cmd *cli.Command) (string, error) {
	paths := trimmedPaths(cmd, "sys")
	if len(paths) == 0 {
		return prompt.Base(), nil
	}

	parts := make([]string, 0, len(paths))
	for _, path := range paths {
		read, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("reading the system prompt: %w", err)
		}
		part := strings.TrimSpace(string(read))
		if part == "" {
			return "", fmt.Errorf("the system prompt in %s is empty", path)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "\n\n"), nil
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
	base, err := basePromptFlag(cmd)
	if err != nil {
		return err
	}
	brought, err := resourcesFlag(cmd)
	if err != nil {
		return err
	}
	schema, err := schemaFlag(cmd)
	if err != nil {
		return err
	}
	// Both caps are read and not checked: each flag refused a number too small
	// to be worth obeying while the command line was being parsed, so what
	// reaches here is a cap a run could finish under and a fan-out a call
	// could be made to.
	maxTurns := cmd.Int("max-turns")
	maxSubagents := cmd.Int("max-subagents")
	maxTerminals, err := terminalFlags(cmd)
	if err != nil {
		return err
	}
	store := sessionFlags{
		dir:     cmd.String("session"),
		discard: cmd.Bool("rm"),
		noLog:   cmd.Bool("no-log"),
	}
	// The hooks are told what agi's output is attached to here, where the
	// streams are, and not where the folders were read: a hooks.json says
	// which kind of run each of its hooks is for, and this is the run.
	armed := brought.hooks.WithTTY(isTerminal(out))
	// --model is handed on as it was typed, empty when it was not given. What
	// an empty one means — fall back to the variable, and refuse the run if
	// that is empty too — is openBackend's to say, since it is the one place
	// that knows both sources.
	return runPrompt(ctx, newOutput(out, cmd.Root().ErrWriter, cmd.Bool("verbose")), cmd.String("model"), base, prompt, schema, cmd.Bool("exit-code"), store, brought.tools, brought.skills, armed, maxTurns, maxSubagents, maxTerminals)
}

// terminalCap is the value of --enable-terminals: how many terminals each
// agent may have open at once.
//
// The flag's value is optional, and that is what this type is for. urfave/cli
// asks a flag's value whether it behaves like a boolean, and one that says it
// does is never handed the next argument: given bare, it is set to "true",
// and given a number it has to be written --enable-terminals=N. So "true" is
// the default cap and anything else has to be a number. The one cost of the
// form is that --enable-terminals 5 hands the 5 to the prompt instead, which
// terminalFlags catches.
type terminalCap struct {
	most int
	// bare is whether the flag was given with no number, which is the case
	// in which a number standing on its own afterwards was probably meant
	// for it.
	bare bool
}

func (c *terminalCap) Set(s string) error {
	if s == "true" {
		c.most, c.bare = workspace.DefaultMaxTerminals, true
		return nil
	}
	most, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("want a number of terminals, as in --enable-terminals=%d", workspace.DefaultMaxTerminals)
	}
	c.most, c.bare = most, false
	return nil
}

// Get returns the value itself, which is what urfave hands the Validator.
func (c *terminalCap) Get() any { return c }

func (c *terminalCap) String() string {
	if c.most == 0 {
		return ""
	}
	return strconv.Itoa(c.most)
}

// IsBoolFlag is what makes the number optional; see terminalCap.
func (c *terminalCap) IsBoolFlag() bool { return true }

// terminalFlags reads --enable-terminals and returns how many terminals each
// agent may have open: zero for a run that was not given any.
//
// It refuses one thing the flag's own Validator cannot see, because it is
// about the prompt and not about the flag. --enable-terminals 5, with a space,
// gives the flag no number and hands the 5 to the prompt — and a prompt from
// standard input would then be ignored, since an argument wins over it. A
// prompt that is nothing but a number, after the flag was given bare, is that
// mistake far more often than it is a question, so it is refused, naming the
// spelling that was meant. A number really meant as the prompt can still be
// piped in.
func terminalFlags(cmd *cli.Command) (int, error) {
	if !cmd.IsSet("enable-terminals") {
		return 0, nil
	}
	c, ok := cmd.Generic("enable-terminals").(*terminalCap)
	if !ok {
		return 0, errors.New("--enable-terminals: unexpected value")
	}
	if args := cmd.Args().Slice(); c.bare && len(args) == 1 && isNumber(args[0]) {
		return 0, fmt.Errorf("the prompt is only %q, which looks like a number meant for "+
			"--enable-terminals; write it as --enable-terminals=%s, or pipe the prompt in on "+
			"standard input if %s really is the question", args[0], strings.TrimSpace(args[0]), strings.TrimSpace(args[0]))
	}
	return c.most, nil
}

// isNumber reports whether s, trimmed, is one or more decimal digits.
func isNumber(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
