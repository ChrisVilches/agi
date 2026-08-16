package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

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

// A prompt typed at a terminal ends with Ctrl-D, which is not something a
// cursor on its own says. The branch that prints it needs a terminal, so what
// is checked here is the invitation itself.
func TestInvitationSaysHowToSendThePrompt(t *testing.T) {
	var errOut bytes.Buffer
	invite(&errOut)

	if !strings.Contains(errOut.String(), "Ctrl-D") {
		t.Errorf("invitation = %q, want it to name the key that sends the prompt", errOut.String())
	}
}

// The invitation is for a person typing a prompt, so a piped run must not get
// it: on stdout it would corrupt the answer, and it is noise either way.
func TestPipedPromptIsNotInvited(t *testing.T) {
	t.Setenv("LITELLM_URL", "")

	out, _ := runStdin(t, "hello\n", "agi")
	if strings.Contains(out, "Ctrl-D") {
		t.Errorf("output = %q, want no invitation when the prompt was piped in", out)
	}
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
	t.Setenv("LITELLM_URL", "")

	_, err := runStdin(t, "  summarize this computer's status\n\n", "agi")
	if err == nil {
		t.Fatal("agi with a piped prompt: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "LITELLM_URL") {
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
	t.Setenv("LITELLM_URL", "")

	_, err := runStdin(t, "how many cores?\n", "agi", "--schema", `cores:int:"CPU cores"`)
	if err == nil {
		t.Fatal("agi --schema with a piped prompt: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "LITELLM_URL") {
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
			got, err := promptFrom(c.args, strings.NewReader(c.stdin))
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
	_, err := promptFrom(nil, failingReader{})
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

// With no gateway configured, a prompt must fail with actionable advice rather
// than attempt a call.
func TestPromptWithoutGatewayIsActionable(t *testing.T) {
	t.Setenv("LITELLM_URL", "")

	_, err := run(t, "agi", "hello")
	if err == nil {
		t.Fatal("agi with no gateway: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "LITELLM_URL") {
		t.Errorf("error = %v, want it to name the variable to set", err)
	}
}

func TestCompactSubcommand(t *testing.T) {
	out, err := run(t, "agi", "compact")
	if err != nil {
		t.Fatalf("agi compact: unexpected error: %v", err)
	}
	if !strings.Contains(out, "agi compact: nothing to do yet") {
		t.Errorf("agi compact: got %q, want the compact action output", out)
	}
}

func TestCompactDryRun(t *testing.T) {
	out, err := run(t, "agi", "compact", "--dry-run")
	if err != nil {
		t.Fatalf("agi compact --dry-run: unexpected error: %v", err)
	}
	if !strings.Contains(out, "dry run") {
		t.Errorf("agi compact --dry-run: got %q, want the dry-run output", out)
	}
}

// The verbose flag lives on the root command; it must be usable both before and
// after the subcommand name.
func TestVerboseFlagPositions(t *testing.T) {
	for _, args := range [][]string{
		{"agi", "--verbose", "compact"},
		{"agi", "compact", "--verbose"},
		{"agi", "-v", "compact"},
	} {
		out, err := run(t, args...)
		if err != nil {
			t.Fatalf("%v: unexpected error: %v", args, err)
		}
		if !strings.Contains(out, "(verbose)") {
			t.Errorf("%v: got %q, want verbose output", args, out)
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

// The command tree is the contract the scaffold exists to establish.
func TestCommandTreeShape(t *testing.T) {
	app := NewApp("test")
	if app.Name != "agi" {
		t.Errorf("root name: got %q, want %q", app.Name, "agi")
	}
	if app.Action == nil {
		t.Error("root command has no action, so bare `agi` would print help instead")
	}
	if got := len(app.Commands); got != 1 {
		t.Fatalf("subcommands: got %d, want 1", got)
	}
	if name := app.Commands[0].Name; name != "compact" {
		t.Errorf("subcommand name: got %q, want %q", name, "compact")
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
