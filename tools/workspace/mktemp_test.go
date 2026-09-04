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
	var _ tools.Reporter = NewMkTemp()
}

// Workspace tools must never explain themselves in the system prompt; a run
// may not include them at all.
func TestMkTempIsNotASystemPrompter(t *testing.T) {
	if _, ok := any(NewMkTemp()).(tools.SystemPrompter); ok {
		t.Error("mktemp implements SystemPrompter, but only native tools may")
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
// the schema: that a copy taken beforehand is the only way back from something
// irreversible, and that it must not clean up after itself.
//
// It says "cannot be undone" rather than naming the changes that can be, and
// that wording is the point: which other tools are registered decides what is
// reversible, and this description may not lean on any of them being there.
func TestMkTempDescriptionCarriesTheRules(t *testing.T) {
	desc := strings.ToLower(NewMkTemp().Definition().Description)
	for _, want := range []string{"cannot be undone", "clean up"} {
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

// The shared prefix is how a user finds these again, since nothing else
// records where they are. The purpose is not in the name: it is a phrase meant
// to be read as the call happens, and folding one into a path segment gave
// names long enough to be worse than no name at all.
func TestMkTempNamesAreRecognizableAndShort(t *testing.T) {
	m := &MkTemp{Dir: t.TempDir()}

	purpose := "back up the entire configuration directory before rewriting it"
	out, err := m.Call(context.Background(), mktempArgs(t, purpose))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	name := filepath.Base(out)
	if !strings.HasPrefix(name, scratchPrefix) {
		t.Errorf("name %q does not start with %q", name, scratchPrefix)
	}
	if len(name) > len(scratchPrefix)+16 {
		t.Errorf("name %q is longer than the prefix and a unique suffix", name)
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

// The path is the one thing about a scratch directory that nothing else
// records, so it is shown to the user as the call finishes rather than only
// reaching the model.
func TestMkTempReportsThePath(t *testing.T) {
	m := NewMkTemp()
	if got := m.Report("/tmp/agi-scratch-123\n"); !strings.Contains(got, "/tmp/agi-scratch-123") {
		t.Errorf("Report() = %q, want it to carry the path", got)
	}
	if got := m.Report("  \n"); got != "" {
		t.Errorf("Report() of nothing = %q, want empty", got)
	}
}
