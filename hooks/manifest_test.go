package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// folder writes a hooks.json and returns the directory holding it.
func folder(t *testing.T, declared string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(declared), 0o644); err != nil {
		t.Fatalf("writing %s: %v", FileName, err)
	}
	return dir
}

// one loads a folder that is expected to be sound, and fails the test if it
// is not. Most tests are about what was read rather than about reading.
func one(t *testing.T, declared string) *Set {
	t.Helper()
	s, err := Load([]string{folder(t, declared)}, Config{})
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	return s
}

const guard = `{
  "PreToolUse": [
    {
      "matcher": "bash",
      "if": {"command": "^(git push|npm publish)"},
      "command": "./scripts/guard-rails.sh"
    }
  ]
}`

func TestLoadReadsTheFile(t *testing.T) {
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
	first := folder(t, `{"PreToolUse": [
	  {"command": "a"}, {"command": "b"}
	]}`)
	second := folder(t, `{"PreToolUse": [{"command": "c"}]}`)

	s, err := Load([]string{first, second}, Config{})
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
	s := one(t, `{
	  "PostToolUse": [{"command": "after"}],
	  "PreToolUse":  [{"command": "before"}]
	}`)

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
	_, err := Load([]string{folder(t, `{"Stop": [{"command": "x"}]}`)}, Config{})
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
		`{"PreToolUse": [{"match": "bash", "command": "x"}]}`,
		`{"hook": {"PreToolUse": [{"command": "x"}]}}`,
	} {
		if _, err := Load([]string{folder(t, manifest)}, Config{}); err == nil {
			t.Errorf("Load(%s) = nil, want an error", manifest)
		}
	}
}

func TestLoadRefusesAPatternThatDoesNotCompile(t *testing.T) {
	_, err := Load([]string{folder(t, `{"PreToolUse": [
	  {"matcher": "bash", "if": {"command": "("}, "command": "x"}
	]}`)}, Config{})
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
	if _, err := Load([]string{folder(t, `{"PreToolUse": [{"matcher": "bash"}]}`)}, Config{}); err == nil {
		t.Error("Load() of an entry naming no program = nil, want an error")
	}
}

// The tools a folder holds are in a file of their own, so every top-level
// name here is an event and one that is not is refused as one. hooks.json is
// not a half of a file this package shares with anybody.
func TestLoadReadsNothingButHooks(t *testing.T) {
	_, err := Load([]string{folder(t, `{"tools": []}`)}, Config{})
	if err == nil {
		t.Fatal("Load() of a file naming tools = nil, want an error")
	}
	if !strings.Contains(err.Error(), `"tools"`) {
		t.Errorf("Load() = %v, want the name that is not an event quoted", err)
	}
}

// A folder with no hooks.json declares no hooks, which is not a mistake: the
// file is one of three a folder of resources may hold, and a folder of tools
// and skills alone is an ordinary folder to name. A hooks.json that is there
// and declares nothing is the opposite, and is refused.
func TestLoadPassesOverAFolderWithNoHooksFile(t *testing.T) {
	s, err := Load([]string{t.TempDir()}, Config{})
	if err != nil {
		t.Fatalf("Load() of a folder with no %s = %v", FileName, err)
	}
	if s.Len() != 0 {
		t.Errorf("Load() found %d hooks, want none", s.Len())
	}
}

// A file that is there and declares nothing says what no file says, and is
// equally fine. Emptying it is how a person turns their guards off for an
// afternoon, and refusing that would make them delete a file they mean to
// write again — and would fail the whole run over a folder whose tools and
// skills were perfectly good.
func TestLoadAcceptsAFileDeclaringNoHooks(t *testing.T) {
	for _, empty := range []string{`{}`, `{"PreToolUse": []}`, `{"PreToolUse": [], "PostToolUse": []}`} {
		s, err := Load([]string{folder(t, empty)}, Config{})
		if err != nil {
			t.Errorf("Load(%s) = %v, want no hooks and no complaint", empty, err)
			continue
		}
		if s.Len() != 0 {
			t.Errorf("Load(%s) found %d hooks, want none", empty, s.Len())
		}
	}
}

// Declaring nothing is a decision; a file that cannot be read as hooks at all
// is a mistake, and those are still refused.
func TestLoadStillRefusesAFileItCannotRead(t *testing.T) {
	for _, broken := range []string{`not json`, `{"PreToolsUse": []}`, `{"PreToolUse": [{}]}`} {
		if _, err := Load([]string{folder(t, broken)}, Config{}); err == nil {
			t.Errorf("Load(%s) = nil, want an error", broken)
		}
	}
}

// A relative path is resolved against the folder the manifest is in, which is
// what makes a folder of hooks something that can be moved. A bare name is
// left for PATH to resolve.
func TestLoadResolvesTheProgramAgainstItsFolder(t *testing.T) {
	dir := folder(t, `{"PreToolUse": [
	  {"command": "./scripts/check.sh", "args": []},
	  {"command": "node", "args": ["./audit.js"]},
	  {"command": "/usr/local/bin/audit", "args": []}
	]}`)

	s, err := Load([]string{dir}, Config{})
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
	s := one(t, `{"PreToolUse": [{"command": "`+line+`"}]}`)

	if got := s.All()[0].Command(); got != line {
		t.Errorf("command = %q, want the line as written", got)
	}
}

// Giving args alongside a command only a shell could run is the two forms
// mixed up. Left alone it would ask the kernel to run a file whose name
// contains a pipe, and that failure would arrive mid-run, where a hook that
// cannot start stops the agent.
func TestLoadRefusesShellSyntaxInTheExecForm(t *testing.T) {
	_, err := Load([]string{folder(t, `{"PreToolUse": [
	  {"command": "jq -r .x | grep y", "args": ["z"]}
	]}`)}, Config{})
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
