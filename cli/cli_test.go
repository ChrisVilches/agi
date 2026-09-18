package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
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

func TestRootCommandWithoutSubcommand(t *testing.T) {
	out, err := run(t, "agi")
	if err != nil {
		t.Fatalf("agi: unexpected error: %v", err)
	}
	if !strings.Contains(out, "agi: nothing to do yet") {
		t.Errorf("agi: got %q, want the root action output", out)
	}
	if strings.Contains(out, "compact") {
		t.Errorf("agi: root action must not run the compact subcommand, got %q", out)
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
		{"agi", "--verbose"},
		{"agi", "-v"},
		{"agi", "--verbose", "compact"},
		{"agi", "compact", "--verbose"},
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

func TestUnknownSubcommandFails(t *testing.T) {
	if _, err := run(t, "agi", "definitely-not-a-command"); err == nil {
		t.Error("agi definitely-not-a-command: want an error, got nil")
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
