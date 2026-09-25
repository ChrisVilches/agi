package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/prompt"
	"github.com/ChrisVilches/agi/transport/litellm"
	"github.com/ChrisVilches/agi/transport/openai"
	"github.com/urfave/cli/v3"
)

// run executes the app with the given argv (including the "agi" argv[0]) and
// returns everything it wrote plus the error it returned. Standard input is
// empty, which is what a pipe carrying nothing looks like; runStdin is for a
// prompt that arrives that way.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runStdin(t, "", args...)
}

// runStdin is run with something on standard input. The reader is a strings
// reader rather than a file, so it is never mistaken for a terminal and the
// prompt is always read from it.
func runStdin(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer
	app := NewApp("test")
	app.Writer = &out
	app.ErrWriter = &out
	app.Reader = strings.NewReader(stdin)

	err := app.Run(context.Background(), args)
	return out.String(), err
}

// No argument and a standard input nobody piped anything into is a run with no
// prompt at all, which must be refused rather than sent to a gateway.
func TestRootWithoutArgumentsOrStdinFails(t *testing.T) {
	_, err := run(t, "agi")
	if err == nil {
		t.Fatal("agi with nothing on stdin: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "the prompt is empty") {
		t.Errorf("error = %v, want it to say the prompt is empty", err)
	}
}

// A prompt piped in is the same prompt as one given as an argument, so the run
// gets as far as needing a gateway.
func TestPromptIsReadFromStdin(t *testing.T) {
	t.Setenv(EnvBackend, litellm.Name)
	t.Setenv(litellm.EnvURL, "")

	_, err := runStdin(t, "  summarize this computer's status\n\n", "agi")
	if err == nil {
		t.Fatal("agi with a piped prompt: want an error, got nil")
	}
	if !strings.Contains(err.Error(), litellm.EnvURL) {
		t.Errorf("error = %v, want the prompt to have been read and the gateway missing", err)
	}
}

// Standard input holding nothing but whitespace is an empty prompt, and is
// refused before any gateway is contacted.
func TestBlankStdinIsAnEmptyPrompt(t *testing.T) {
	_, err := runStdin(t, "   \n\t\n", "agi")
	if err == nil {
		t.Fatal("agi with a blank stdin: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "the prompt is empty") {
		t.Errorf("error = %v, want it to say the prompt is empty", err)
	}
}

// A schema is a flag, so it says nothing about where the prompt came from: a
// piped prompt is read the same way, and the run fails on the gateway rather
// than on the missing argument.
func TestSchemaWorksWithAPipedPrompt(t *testing.T) {
	t.Setenv(EnvBackend, litellm.Name)
	t.Setenv(litellm.EnvURL, "")

	_, err := runStdin(t, "how many cores?\n", "agi", "--schema", `cores:int:"CPU cores"`)
	if err == nil {
		t.Fatal("agi --schema with a piped prompt: want an error, got nil")
	}
	if !strings.Contains(err.Error(), litellm.EnvURL) {
		t.Errorf("error = %v, want the prompt and schema to have been accepted", err)
	}
}

// An unparseable schema must be reported as such, and not be hidden by the
// prompt having come from standard input.
func TestPipedPromptStillReportsABadSchema(t *testing.T) {
	_, err := runStdin(t, "how many cores?\n", "agi", "--schema", "cores:nonsense")
	if err == nil {
		t.Fatal("agi --schema with a bad spelling: want an error, got nil")
	}
	if strings.Contains(err.Error(), "the prompt is empty") {
		t.Errorf("error = %v, want the schema to be what failed", err)
	}
}

func TestPromptFrom(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		stdin string
		want  string
		fails bool
	}{
		{name: "argument", args: []string{"  hello  "}, stdin: "ignored", want: "hello"},
		{name: "stdin", stdin: "\n  hello\nworld  \n", want: "hello\nworld"},
		{name: "blank argument", args: []string{"   "}, stdin: "hello", fails: true},
		{name: "blank stdin", stdin: " \n", fails: true},
		{name: "nothing at all", fails: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := promptFrom(c.args, strings.NewReader(c.stdin), true)
			if c.fails {
				if err == nil {
					t.Fatalf("promptFrom(%q, %q) = %q, want an error", c.args, c.stdin, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("promptFrom(%q, %q): unexpected error: %v", c.args, c.stdin, err)
			}
			if got != c.want {
				t.Errorf("promptFrom(%q, %q) = %q, want %q", c.args, c.stdin, got, c.want)
			}
		})
	}
}

// A standard input that cannot be read is reported as such, rather than
// becoming an empty prompt.
func TestPromptFromReportsAReadFailure(t *testing.T) {
	_, err := promptFrom(nil, failingReader{}, true)
	if err == nil {
		t.Fatal("promptFrom with an unreadable stdin: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "standard input") {
		t.Errorf("error = %v, want it to name standard input", err)
	}
}

// failingReader is a standard input that cannot be read.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("broken pipe") }

// Standard input that holds no prompt is not read at all. A terminal nobody is
// typing at is the case that matters: reading it would wait for a Ctrl-D that
// never comes, so a reader that would block forever must never be touched.
func TestPromptFromDoesNotReadStandardInput(t *testing.T) {
	got, err := promptFrom(nil, blockingReader{t}, false)
	if err == nil {
		t.Fatalf("promptFrom with nothing to read = %q, want an error", got)
	}
	if !strings.Contains(err.Error(), "the prompt is empty") {
		t.Errorf("error = %v, want it to say the prompt is empty", err)
	}
}

// An argument is the prompt whether or not standard input holds one, so a
// standard input that must not be read is no obstacle to running.
func TestPromptFromPrefersTheArgument(t *testing.T) {
	got, err := promptFrom([]string{"  hello  "}, blockingReader{t}, false)
	if err != nil {
		t.Fatalf("promptFrom with an argument: unexpected error: %v", err)
	}
	if got != "hello" {
		t.Errorf("promptFrom = %q, want %q", got, "hello")
	}
}

// blockingReader stands for a terminal nobody is typing at: reading it in a
// real run never returns, so the test fails the moment it is read.
type blockingReader struct{ t *testing.T }

func (r blockingReader) Read([]byte) (int, error) {
	r.t.Helper()
	r.t.Fatal("standard input was read when it holds no prompt")
	return 0, nil
}

// holdsPrompt is the rule that keeps `agi > result` from hanging on a keyboard
// nobody is at. It is exercised through booleans because no test can be given
// a terminal.
func TestHoldsPrompt(t *testing.T) {
	for _, c := range []struct {
		name          string
		stdin, stdout bool
		want          bool
	}{
		{name: "piped in, piped out", want: true},
		{name: "piped in, terminal out", stdout: true, want: true},
		{name: "terminal in, terminal out", stdin: true, stdout: true, want: true},
		{name: "terminal in, redirected out", stdin: true, want: false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := holdsPrompt(c.stdin, c.stdout); got != c.want {
				t.Errorf("holdsPrompt(%v, %v) = %v, want %v", c.stdin, c.stdout, got, c.want)
			}
		})
	}
}

// stdinHoldsPrompt must reach the same answer from the streams themselves. A
// pipe is a file but not a terminal, which is the case a test can make.
func TestStdinHoldsPromptDetectsAPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() = %v", err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })

	if !stdinHoldsPrompt(r, &bytes.Buffer{}) {
		t.Error("stdinHoldsPrompt(pipe, buffer) = false, want true")
	}
}

// An unquoted prompt arrives as several arguments and must be rejected rather
// than silently run as only its first word.
func TestRootRejectsMultipleArguments(t *testing.T) {
	_, err := run(t, "agi", "summarize", "this", "computer")
	if err == nil {
		t.Fatal("agi with several arguments: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "quote the prompt") {
		t.Errorf("error = %v, want it to suggest quoting the prompt", err)
	}
}

// With a backend chosen but not configured, a prompt must fail with actionable
// advice rather than attempt a call.
func TestPromptWithoutGatewayIsActionable(t *testing.T) {
	t.Setenv(EnvBackend, litellm.Name)
	t.Setenv(litellm.EnvURL, "")

	_, err := run(t, "agi", "hello")
	if err == nil {
		t.Fatal("agi with no gateway: want an error, got nil")
	}
	if !strings.Contains(err.Error(), litellm.EnvURL) {
		t.Errorf("error = %v, want it to name the variable to set", err)
	}
}

// Choosing no backend at all is the other way to get nowhere, and it must say
// which names it would accept rather than send the user after one backend's
// variables.
func TestPromptWithoutABackendIsActionable(t *testing.T) {
	t.Setenv(EnvBackend, "")

	_, err := run(t, "agi", "hello")
	if err == nil {
		t.Fatal("agi with no backend chosen: want an error, got nil")
	}
	for _, want := range []string{EnvBackend, litellm.Name, openai.Name} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

func TestVersionFlag(t *testing.T) {
	out, err := run(t, "agi", "--version")
	if err != nil {
		t.Fatalf("agi --version: unexpected error: %v", err)
	}
	if !strings.Contains(out, "test") {
		t.Errorf("agi --version: got %q, want the injected version", out)
	}
}

// Running agi is running the agent on a prompt, so the root command carries
// the action itself and there is nothing else to invoke.
func TestCommandTreeShape(t *testing.T) {
	app := NewApp("test")
	if app.Name != "agi" {
		t.Errorf("root name: got %q, want %q", app.Name, "agi")
	}
	if app.Action == nil {
		t.Error("root command has no action, so bare `agi` would print help instead")
	}
	if got := len(app.Commands); got != 0 {
		t.Errorf("subcommands: got %d, want 0", got)
	}
}

// A schema that cannot be parsed must fail the command outright, before a
// gateway is contacted or a token is spent.
func TestRootRejectsABadSchema(t *testing.T) {
	_, err := run(t, "agi", "--schema", "id:number", "who is user 1?")
	if err == nil {
		t.Fatal("agi with a bad schema: want an error, got nil")
	}
	if !strings.Contains(err.Error(), `unknown type "number"`) {
		t.Errorf("agi with a bad schema = %v, want the parse error", err)
	}
}

// The flag is what turns the feature on, so an absent one must leave no schema.
func TestSchemaFlagIsOptional(t *testing.T) {
	app := NewApp("test")
	app.Action = func(_ context.Context, cmd *cli.Command) error {
		schema, err := schemaFlag(cmd)
		if err != nil {
			t.Fatalf("schemaFlag() = %v, want nil", err)
		}
		if schema != nil {
			t.Errorf("schemaFlag() = %v, want nil without the flag", schema)
		}
		return nil
	}
	if err := app.Run(context.Background(), []string{"agi", "prompt"}); err != nil {
		t.Fatalf("Run() = %v", err)
	}
}

// A prompt of nothing but whitespace must be refused by the command itself,
// before a gateway is contacted or a session directory is made for it.
func TestRootRejectsAnEmptyPrompt(t *testing.T) {
	_, err := run(t, "agi", "   ")
	if err == nil {
		t.Fatal("agi with a blank prompt: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("agi with a blank prompt = %v, want an empty-prompt error", err)
	}
}

// basePromptOf runs the app far enough to read --base-prompt and returns what
// the run would have started from. The action is replaced, so nothing is sent
// anywhere and no session is made: the flag is the whole of what is under
// test.
func basePromptOf(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var got string
	var readErr error
	app := NewApp("test")
	app.Action = func(_ context.Context, cmd *cli.Command) error {
		got, readErr = basePromptFlag(cmd)
		return nil
	}
	if err := app.Run(context.Background(), append([]string{"agi"}, args...)); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	return got, readErr
}

// Without the flag a run starts from the prompt agi ships, which is what makes
// the flag an override rather than a requirement.
func TestBasePromptDefaultsToTheBuiltInOne(t *testing.T) {
	got, err := basePromptOf(t, "prompt")
	if err != nil {
		t.Fatalf("basePromptFlag() = %v, want nil", err)
	}
	if got != prompt.Base() {
		t.Errorf("base prompt = %q, want the built-in one", got)
	}
}

// The flag names a file and the file's text is the base, whatever the file is
// called: nothing reads it as Markdown, so nothing depends on the extension.
func TestBasePromptComesFromTheFileNamed(t *testing.T) {
	path := writeTemp(t, "persona.txt", "\n  You are a Zettelkasten librarian.\n\n")

	got, err := basePromptOf(t, "--base-prompt", path, "prompt")
	if err != nil {
		t.Fatalf("basePromptFlag() = %v, want nil", err)
	}
	if got != "You are a Zettelkasten librarian." {
		t.Errorf("base prompt = %q, want the file's text trimmed", got)
	}
	if strings.Contains(got, prompt.Base()) {
		t.Error("the built-in base prompt survived a file replacing it")
	}
}

// Given several times the files are one instruction, in the order they were
// written on the command line and separated by a blank line so that each
// arrives as its own paragraph.
func TestBasePromptsAreJoinedInTheOrderGiven(t *testing.T) {
	first := writeTemp(t, "role.md", "You are a careful assistant.")
	second := writeTemp(t, "machine.md", "This machine runs Arch Linux.")
	third := writeTemp(t, "style.md", "Answer in at most three sentences.")

	got, err := basePromptOf(t, "--base-prompt", first, "--base-prompt", second, "--base-prompt", third, "prompt")
	if err != nil {
		t.Fatalf("basePromptFlag() = %v, want nil", err)
	}
	want := "You are a careful assistant.\n\nThis machine runs Arch Linux.\n\nAnswer in at most three sentences."
	if got != want {
		t.Errorf("base prompt = %q, want %q", got, want)
	}
}

// One value is one path. urfave/cli splits a slice flag's value on commas
// unless it is told not to, which would turn a path with a comma in it into
// two paths that do not exist.
func TestBasePromptPathMayContainAComma(t *testing.T) {
	path := writeTemp(t, "notes,drafts.md", "You are terse.")

	got, err := basePromptOf(t, "--base-prompt", path, "prompt")
	if err != nil {
		t.Fatalf("basePromptFlag() = %v, want nil", err)
	}
	if got != "You are terse." {
		t.Errorf("base prompt = %q, want the file with a comma in its name to have been read", got)
	}
}

// A path that is wrong costs a message rather than a run: the file is read
// before a gateway is contacted or a session is created.
func TestBasePromptReportsAMissingFile(t *testing.T) {
	_, err := run(t, "agi", "--base-prompt", "/nonexistent/persona.md", "hello")
	if err == nil {
		t.Fatal("agi with a missing base prompt: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "reading the base prompt") {
		t.Errorf("error = %v, want it to say the base prompt could not be read", err)
	}
}

// An empty file is refused rather than sent. A run has to start from some
// instruction, and a file with nothing in it is a mistake far more often than
// it is a request to run without one.
func TestBasePromptRefusesAnEmptyFile(t *testing.T) {
	path := writeTemp(t, "blank.md", "   \n\t\n")

	if _, err := basePromptOf(t, "--base-prompt", path, "prompt"); err == nil {
		t.Fatal("basePromptFlag() with a blank file: want an error, got nil")
	} else if !strings.Contains(err.Error(), "is empty") {
		t.Errorf("basePromptFlag() = %v, want it to say the file is empty", err)
	}
}

// One empty file among several is refused too: the run would otherwise start
// from an instruction missing the part the user thought they had given it.
func TestBasePromptRefusesAnEmptyFileAmongSeveral(t *testing.T) {
	good := writeTemp(t, "role.md", "You are a careful assistant.")
	blank := writeTemp(t, "style.md", "")

	if _, err := basePromptOf(t, "--base-prompt", good, "--base-prompt", blank, "prompt"); err == nil {
		t.Fatal("basePromptFlag() with one blank file: want an error, got nil")
	} else if !strings.Contains(err.Error(), blank) {
		t.Errorf("basePromptFlag() = %v, want it to name the file that is empty", err)
	}
}

// A path is a shell argument, and space around one is never part of the path.
// Nothing but space is no path at all, which is a mistake rather than a
// request for the built-in prompt.
func TestBasePromptRefusesBlankPath(t *testing.T) {
	if _, err := basePromptOf(t, "--base-prompt", "   ", "prompt"); err == nil {
		t.Fatal("basePromptFlag() with a blank path: want an error, got nil")
	} else if !strings.Contains(err.Error(), "nothing but spaces") {
		t.Errorf("basePromptFlag() = %v, want it to say the flag was given no path", err)
	}
}

// writeTemp writes a file into a directory of the test's own and returns its
// path.
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}
	return path
}

// A file the operating system will not let agi read is the same kind of
// mistake as one that is not there, and is reported the same way: whatever the
// system said, with nothing in front of it.
func TestBasePromptReportsAnUnreadableFile(t *testing.T) {
	path := writeTemp(t, "persona.md", "You are terse.")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("Chmod() = %v", err)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("the file is still readable, so this proves nothing (running as root?)")
	}

	if _, err := basePromptOf(t, "--base-prompt", path, "prompt"); err == nil {
		t.Fatal("basePromptFlag() with an unreadable file: want an error, got nil")
	} else if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("basePromptFlag() = %v, want the reason the file could not be read", err)
	}
}

// The flag replaces part of the system prompt, and "system prompt" is what
// that is called outside this repository. --help has to say so: a user reading
// it has no reason to know that agi calls the swappable half the base.
func TestHelpNamesTheSystemPrompt(t *testing.T) {
	out, err := run(t, "agi", "--help")
	if err != nil {
		t.Fatalf("agi --help: unexpected error: %v", err)
	}
	line := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "--base-prompt") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("agi --help does not mention --base-prompt: %q", out)
	}
	if !strings.Contains(line, "system prompt") {
		t.Errorf("--base-prompt is explained as %q, want it to say what it is: the system prompt", line)
	}
}
