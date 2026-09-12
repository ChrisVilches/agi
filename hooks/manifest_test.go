package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// folder writes a manifest and returns the directory holding it.
func folder(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing the manifest: %v", err)
	}
	return dir
}

// one loads a folder that is expected to be sound, and fails the test if it
// is not. Most tests are about what was read rather than about reading.
func one(t *testing.T, manifest string) *Set {
	t.Helper()
	s, err := Load([]string{folder(t, manifest)})
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	return s
}

const guard = `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "bash",
        "if": {"command": "^(git push|npm publish)"},
        "command": "./scripts/guard-rails.sh"
      }
    ]
  }
}`

func TestLoadReadsTheManifest(t *testing.T) {
	s := one(t, guard)

	if s.Len() != 1 {
		t.Fatalf("Load() found %d hooks, want 1", s.Len())
	}
	h := s.All()[0]
	if h.Event() != PreToolUse {
		t.Errorf("event = %q, want %q", h.Event(), PreToolUse)
	}
	if h.Matcher() != "bash" {
		t.Errorf("matcher = %q, want the one the manifest wrote", h.Matcher())
	}
	if !strings.HasSuffix(h.Command(), "scripts/guard-rails.sh") {
		t.Errorf("command = %q, want the script the manifest named", h.Command())
	}
}

// The order hooks run in is the order the folders were given and the order
// each manifest lists them, because one hook's output is the next one's
// input. A map's iteration order would make it differ from run to run.
func TestLoadKeepsTheOrderGiven(t *testing.T) {
	first := folder(t, `{"hooks": {"PreToolUse": [
	  {"command": "a"}, {"command": "b"}
	]}}`)
	second := folder(t, `{"hooks": {"PreToolUse": [{"command": "c"}]}}`)

	s, err := Load([]string{first, second})
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	var got []string
	for _, h := range s.All() {
		got = append(got, h.Command())
	}
	if want := []string{"a", "b", "c"}; !equal(got, want) {
		t.Errorf("commands = %v, want %v", got, want)
	}
}

// Both events are read, and a manifest may use either or both.
func TestLoadReadsBothEvents(t *testing.T) {
	s := one(t, `{"hooks": {
	  "PostToolUse": [{"command": "after"}],
	  "PreToolUse":  [{"command": "before"}]
	}}`)

	if s.Len() != 2 {
		t.Fatalf("Load() found %d hooks, want 2", s.Len())
	}
	// Events come out in the order they happen, whatever order the manifest
	// wrote them in, since a map hands them over in no order at all.
	if got := s.All()[0].Event(); got != PreToolUse {
		t.Errorf("first event = %q, want %q", got, PreToolUse)
	}
	if got := s.All()[1].Event(); got != PostToolUse {
		t.Errorf("second event = %q, want %q", got, PostToolUse)
	}
}

// A manifest written against Claude Code's full list of events is the
// likeliest way to name one agi does not have, so the refusal says which two
// it does.
func TestLoadRefusesAnUnknownEvent(t *testing.T) {
	_, err := Load([]string{folder(t, `{"hooks": {"Stop": [{"command": "x"}]}}`)})
	if err == nil {
		t.Fatal("Load() of an unknown event = nil, want an error")
	}
	if !strings.Contains(err.Error(), "Stop") || !strings.Contains(err.Error(), "PreToolUse") {
		t.Errorf("Load() = %v, want the unknown event and the ones there are", err)
	}
}

// A typo in a field name is a hook that guards nothing, and both spellings
// below have a silent version: "match" would match every tool, "commands"
// would name no program.
func TestLoadRefusesUnknownFields(t *testing.T) {
	for _, manifest := range []string{
		`{"hooks": {"PreToolUse": [{"match": "bash", "command": "x"}]}}`,
		`{"hook": {"PreToolUse": [{"command": "x"}]}}`,
	} {
		if _, err := Load([]string{folder(t, manifest)}); err == nil {
			t.Errorf("Load(%s) = nil, want an error", manifest)
		}
	}
}

func TestLoadRefusesAPatternThatDoesNotCompile(t *testing.T) {
	_, err := Load([]string{folder(t, `{"hooks": {"PreToolUse": [
	  {"matcher": "bash", "if": {"command": "("}, "command": "x"}
	]}}`)})
	if err == nil {
		t.Fatal("Load() of a broken condition = nil, want an error")
	}
	// The parameter is named, since a manifest may hold several conditions
	// and the regular expression alone would not say which one was wrong.
	if !strings.Contains(err.Error(), "command") {
		t.Errorf("Load() = %v, want the condition's parameter named", err)
	}
}

func TestLoadRefusesAnEntryWithNoCommand(t *testing.T) {
	if _, err := Load([]string{folder(t, `{"hooks": {"PreToolUse": [{"matcher": "bash"}]}}`)}); err == nil {
		t.Error("Load() of an entry naming no program = nil, want an error")
	}
}

// Naming a folder of tools to --hooks is a mistake the message can fix.
func TestLoadSaysWhenAFolderHoldsOnlyTools(t *testing.T) {
	_, err := Load([]string{folder(t, `{"tools": [{"name": "x", "path": "x.sh", "description": "d"}]}`)})
	if err == nil {
		t.Fatal("Load() of a folder declaring only tools = nil, want an error")
	}
	if !strings.Contains(err.Error(), "--tools") {
		t.Errorf("Load() = %v, want the flag that reads tools named", err)
	}
}

func TestLoadRefusesAFolderWithNoManifest(t *testing.T) {
	if _, err := Load([]string{t.TempDir()}); err == nil {
		t.Error("Load() of a folder with no manifest = nil, want an error")
	}
}

// A relative path is resolved against the folder the manifest is in, which is
// what makes a folder of hooks something that can be moved. A bare name is
// left for PATH to resolve.
func TestLoadResolvesTheProgramAgainstItsFolder(t *testing.T) {
	dir := folder(t, `{"hooks": {"PreToolUse": [
	  {"command": "./scripts/check.sh", "args": []},
	  {"command": "node", "args": ["./audit.js"]},
	  {"command": "/usr/local/bin/audit", "args": []}
	]}}`)

	s, err := Load([]string{dir})
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolving the folder: %v", err)
	}

	if got, want := s.All()[0].Command(), filepath.Join(root, "scripts/check.sh"); got != want {
		t.Errorf("relative command = %q, want %q", got, want)
	}
	if got := s.All()[1].Command(); got != "node" {
		t.Errorf("bare command = %q, want it left for PATH", got)
	}
	if got := s.All()[2].Command(); got != "/usr/local/bin/audit" {
		t.Errorf("absolute command = %q, want it unchanged", got)
	}
}

// A shell command line is the user's own text and nothing in it is resolved:
// a path may be anywhere in it, and what makes a relative one work is that
// the program runs in the folder its manifest is in.
func TestLoadLeavesAShellCommandLineAlone(t *testing.T) {
	line := "./scripts/check.sh | grep -q ok"
	s := one(t, `{"hooks": {"PreToolUse": [{"command": "`+line+`"}]}}`)

	if got := s.All()[0].Command(); got != line {
		t.Errorf("command = %q, want the line as written", got)
	}
}

// Giving args alongside a command only a shell could run is the two forms
// mixed up. Left alone it would ask the kernel to run a file whose name
// contains a pipe, and that failure would arrive mid-run, where a hook that
// cannot start stops the agent.
func TestLoadRefusesShellSyntaxInTheExecForm(t *testing.T) {
	_, err := Load([]string{folder(t, `{"hooks": {"PreToolUse": [
	  {"command": "jq -r .x | grep y", "args": ["z"]}
	]}}`)})
	if err == nil {
		t.Fatal("Load() of mixed forms = nil, want an error")
	}
	if !strings.Contains(err.Error(), "args") {
		t.Errorf("Load() = %v, want the fix named", err)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
