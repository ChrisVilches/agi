package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/tools"
)

func mktempArgs(t *testing.T, purpose string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(MkTempParams{Purpose: purpose})
	if err != nil {
		t.Fatalf("marshalling args: %v", err)
	}
	return b
}

func TestMkTempIsATool(t *testing.T) {
	var _ tools.Tool = NewMkTemp()
	var _ tools.Announcer = NewMkTemp()
}

// Workspace tools must never explain themselves in the system prompt; a run
// may not include them at all.
func TestMkTempIsNotASystemPrompter(t *testing.T) {
	if _, ok := any(NewMkTemp()).(tools.SystemPrompter); ok {
		t.Error("mktemp implements SystemPrompter, but only control-flow tools may")
	}
}

// The description is all the model is told, so it has to carry the usage rules.
func TestMkTempDefinition(t *testing.T) {
	def := NewMkTemp().Definition()
	if def.Name != "mktemp" {
		t.Errorf("name = %q, want %q", def.Name, "mktemp")
	}
	if len(strings.TrimSpace(def.Description)) < 100 {
		t.Error("description is too thin to be the only thing the model is told")
	}

	required, ok := def.Parameters["required"].([]string)
	if !ok {
		t.Fatalf("required = %T, want []string", def.Parameters["required"])
	}
	if strings.Join(required, ",") != "purpose" {
		t.Errorf("required = %v, want purpose", required)
	}
}

// The description has to tell the model the two things it cannot work out from
// the schema: that a copy taken beforehand is the only way back, and that it
// must not clean up after itself.
func TestMkTempDescriptionCarriesTheRules(t *testing.T) {
	desc := strings.ToLower(NewMkTemp().Definition().Description)
	for _, want := range []string{"before you change", "clean up"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description does not mention %q", want)
		}
	}
}

func TestMkTempCreatesAnEmptyDirectory(t *testing.T) {
	m := &MkTemp{Dir: t.TempDir()}

	out, err := m.Call(context.Background(), mktempArgs(t, "keep a copy of config.yaml"))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat %q: %v", out, err)
	}
	if !info.IsDir() {
		t.Errorf("%q is not a directory", out)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatalf("reading %q: %v", out, err)
	}
	if len(entries) != 0 {
		t.Errorf("new directory holds %d entries, want none", len(entries))
	}
}

// The prefix is how a user finds these again, since nothing else records where
// they are; the label is how they tell one from another.
func TestMkTempNamesTheDirectoryAfterThePurpose(t *testing.T) {
	m := &MkTemp{Dir: t.TempDir()}

	out, err := m.Call(context.Background(), mktempArgs(t, "Back up config.yaml!"))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	name := filepath.Base(out)
	if !strings.HasPrefix(name, scratchPrefix) {
		t.Errorf("name %q does not start with %q", name, scratchPrefix)
	}
	if !strings.Contains(name, "back-up-config-yaml") {
		t.Errorf("name %q does not carry the purpose", name)
	}
}

// Every call is a directory of its own: an agent that asked twice must not be
// handed something another call is already using.
func TestMkTempIsNewEveryTime(t *testing.T) {
	m := &MkTemp{Dir: t.TempDir()}

	first, err := m.Call(context.Background(), mktempArgs(t, "same purpose"))
	if err != nil {
		t.Fatalf("first Call: %v", err)
	}
	second, err := m.Call(context.Background(), mktempArgs(t, "same purpose"))
	if err != nil {
		t.Fatalf("second Call: %v", err)
	}
	if first == second {
		t.Errorf("both calls returned %q", first)
	}
}

// A directory an agent keeps a copy of the user's files in is as private as
// the session that produced it, on a machine whose temporary directory
// everyone can read.
func TestMkTempDirectoryIsPrivate(t *testing.T) {
	m := &MkTemp{Dir: t.TempDir()}

	out, err := m.Call(context.Background(), mktempArgs(t, "private"))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("permissions = %o, want 700", perm)
	}
}

func TestMkTempRequiresAPurpose(t *testing.T) {
	m := &MkTemp{Dir: t.TempDir()}

	for _, name := range []string{"empty", "blank"} {
		purpose := ""
		if name == "blank" {
			purpose = "   "
		}
		if _, err := m.Call(context.Background(), mktempArgs(t, purpose)); err == nil {
			t.Errorf("%s purpose was accepted", name)
		}
	}
}

func TestMkTempRejectsMalformedArguments(t *testing.T) {
	m := &MkTemp{Dir: t.TempDir()}
	if _, err := m.Call(context.Background(), json.RawMessage(`{`)); err == nil {
		t.Error("malformed JSON was accepted")
	}
}

// A purpose written entirely in characters a path should not carry leaves the
// prefix alone rather than an empty or dangling name.
func TestMkTempPurposeThatFoldsToNothing(t *testing.T) {
	if got := pattern("！！！"); got != scratchPrefix+"*" {
		t.Errorf("pattern = %q, want %q", got, scratchPrefix+"*")
	}
}

func TestLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"back up config.yaml", "back-up-config-yaml"},
		{"  Spaces  Around  ", "spaces-around"},
		{"punctuation!!!, lots???", "punctuation-lots"},
		{"CamelCase123", "camelcase123"},
		{"---", ""},
		{"", ""},
		{strings.Repeat("a", 100), strings.Repeat("a", maxLabel)},
	}
	for _, c := range cases {
		if got := label(c.in); got != c.want {
			t.Errorf("label(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Announce is display only and must not fail on arguments it cannot read.
func TestMkTempAnnounce(t *testing.T) {
	m := NewMkTemp()
	if got := m.Announce(mktempArgs(t, "keep a copy")); got != "keep a copy" {
		t.Errorf("Announce = %q, want %q", got, "keep a copy")
	}
	if got := m.Announce(json.RawMessage(`{`)); got != "" {
		t.Errorf("Announce of malformed arguments = %q, want empty", got)
	}
}
