package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

// run executes the app with the given argv (including the "agi" argv[0]) and
// returns everything it wrote plus the error it returned.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer
	app := NewApp("test")
	app.Writer = &out
	app.ErrWriter = &out

	err := app.Run(context.Background(), args)
	return out.String(), err
}

func TestRootWithoutArgumentsShowsUsage(t *testing.T) {
	out, err := run(t, "agi")
	if err != nil {
		t.Fatalf("agi: unexpected error: %v", err)
	}
	if !strings.Contains(out, "give me a prompt") {
		t.Errorf("agi: got %q, want a usage hint", out)
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
