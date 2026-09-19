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

func patchArgs(t *testing.T, path, old, new string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(PatchFileParams{Path: path, OldStr: old, NewStr: new})
	if err != nil {
		t.Fatalf("marshalling args: %v", err)
	}
	return b
}

// write puts content in a file under a fresh directory and returns its path.
func write(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func patch(t *testing.T, path, old, new string) (string, error) {
	t.Helper()
	return NewPatchFile().Call(context.Background(), patchArgs(t, path, old, new))
}

func contents(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

func TestPatchFileIsATool(t *testing.T) {
	var _ tools.Tool = NewPatchFile()
	var _ tools.Announcer = NewPatchFile()
}

// Workspace tools must never explain themselves in the system prompt; a run
// may not include them at all.
func TestPatchFileIsNotASystemPrompter(t *testing.T) {
	if _, ok := any(NewPatchFile()).(tools.SystemPrompter); ok {
		t.Error("patchfile implements SystemPrompter, but only control-flow tools may")
	}
}

// The description is all the model is told, so it has to carry the usage rules.
func TestPatchFileDefinition(t *testing.T) {
	def := NewPatchFile().Definition()
	if def.Name != "patchfile" {
		t.Errorf("name = %q, want %q", def.Name, "patchfile")
	}
	if len(strings.TrimSpace(def.Description)) < 100 {
		t.Error("description is too thin to be the only thing the model is told")
	}

	required, ok := def.Parameters["required"].([]string)
	if !ok {
		t.Fatalf("required = %T, want []string", def.Parameters["required"])
	}
	if strings.Join(required, ",") != "path,old_str,new_str" {
		t.Errorf("required = %v, want all three", required)
	}
}

func TestPatchFileReplacesTheOneMatch(t *testing.T) {
	path := write(t, "server.yaml", "host: localhost\ntimeout: 30\nport: 80\n")

	out, err := patch(t, path, "timeout: 30", "timeout: 120")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(out, "1 occurrence") {
		t.Errorf("result = %q, want it to say what it did", out)
	}
	if got, want := contents(t, path), "host: localhost\ntimeout: 120\nport: 80\n"; got != want {
		t.Errorf("contents = %q, want %q", got, want)
	}
}

// An empty new_str is a deletion, which is a legitimate patch.
func TestPatchFileDeletesWithEmptyNewStr(t *testing.T) {
	path := write(t, "list.txt", "keep\ndrop\nkeep too\n")

	if _, err := patch(t, path, "drop\n", ""); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got, want := contents(t, path), "keep\nkeep too\n"; got != want {
		t.Errorf("contents = %q, want %q", got, want)
	}
}

// Matching is over the file's bytes and not its lines, which is the whole
// reason the awkward edits are possible at all: a file that ends without a
// newline is given one by patching its last line, and nothing adds or removes
// a trailing newline on its own.
func TestPatchFileAddsATrailingNewline(t *testing.T) {
	path := write(t, "noeol.txt", "first\nlast line")

	if _, err := patch(t, path, "last line", "last line\n"); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got, want := contents(t, path), "first\nlast line\n"; got != want {
		t.Errorf("contents = %q, want %q", got, want)
	}
}

func TestPatchFileRemovesATrailingNewline(t *testing.T) {
	path := write(t, "eol.txt", "first\nlast line\n")

	if _, err := patch(t, path, "last line\n", "last line"); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got, want := contents(t, path), "first\nlast line"; got != want {
		t.Errorf("contents = %q, want %q", got, want)
	}
}

func TestPatchFileMatchesAcrossLines(t *testing.T) {
	path := write(t, "block.go", "func a() {\n\treturn 1\n}\n")

	if _, err := patch(t, path, "func a() {\n\treturn 1\n}", "func a() {\n\treturn 2\n}"); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got, want := contents(t, path), "func a() {\n\treturn 2\n}\n"; got != want {
		t.Errorf("contents = %q, want %q", got, want)
	}
}

// The two ways a match can be wrong are corrected differently, so they are
// refused differently: one says read the file again, the other says include
// more of it.
func TestPatchFileRefusesAnAmbiguousMatch(t *testing.T) {
	path := write(t, "dup.txt", "value: 1\nvalue: 1\n")

	_, err := patch(t, path, "value: 1", "value: 2")
	if err == nil {
		t.Fatal("an ambiguous patch was accepted")
	}
	if !strings.Contains(err.Error(), "2 times") {
		t.Errorf("error %q does not say how many times it matched", err)
	}
	if !strings.Contains(err.Error(), "more of the text") {
		t.Errorf("error %q does not say how to disambiguate", err)
	}
	if got, want := contents(t, path), "value: 1\nvalue: 1\n"; got != want {
		t.Errorf("a refused patch changed the file to %q", got)
	}
}

func TestPatchFileRefusesAMissingMatch(t *testing.T) {
	path := write(t, "config.yaml", "timeout: 30\n")

	_, err := patch(t, path, "timeout:  30", "timeout: 120")
	if err == nil {
		t.Fatal("a patch matching nothing was accepted")
	}
	if !strings.Contains(err.Error(), "Read the file again") {
		t.Errorf("error %q does not tell the model to re-read the file", err)
	}
	if got, want := contents(t, path), "timeout: 30\n"; got != want {
		t.Errorf("a refused patch changed the file to %q", got)
	}
}

func TestPatchFileRefusesBadArguments(t *testing.T) {
	path := write(t, "any.txt", "hello\n")

	cases := []struct {
		name, old, new, want string
	}{
		{"empty old_str", "", "x", "matches everywhere"},
		{"identical strings", "hello", "hello", "identical"},
	}
	for _, c := range cases {
		_, err := patch(t, path, c.old, c.new)
		if err == nil {
			t.Errorf("%s was accepted", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q does not mention %q", c.name, err, c.want)
		}
	}

	if _, err := patch(t, "   ", "a", "b"); err == nil {
		t.Error("a blank path was accepted")
	}
	if _, err := NewPatchFile().Call(context.Background(), json.RawMessage(`{`)); err == nil {
		t.Error("malformed JSON was accepted")
	}
}

func TestPatchFileRefusesAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nothing.txt")

	_, err := patch(t, path, "a", "b")
	if err == nil {
		t.Fatal("a patch against a missing file was accepted")
	}
	if !strings.Contains(err.Error(), "never creates one") {
		t.Errorf("error %q does not say the tool will not create the file", err)
	}
}

func TestPatchFileRefusesADirectory(t *testing.T) {
	if _, err := patch(t, t.TempDir(), "a", "b"); err == nil {
		t.Error("a patch against a directory was accepted")
	}
}

// TODO in the tool: other encodings should be decoded rather than refused.
func TestPatchFileRefusesNonUTF8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary.dat")
	if err := os.WriteFile(path, []byte{0x68, 0xff, 0xfe, 0x69}, 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}

	_, err := patch(t, path, "h", "H")
	if err == nil {
		t.Fatal("a patch against a non-text file was accepted")
	}
	if !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("error %q does not say why it was refused", err)
	}
}

// Rename-into-place loses the original mode unless it is carried across, which
// would quietly take the execute bit off every script that got patched.
func TestPatchFileKeepsTheMode(t *testing.T) {
	path := write(t, "run.sh", "#!/bin/sh\necho old\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if _, err := patch(t, path, "echo old", "echo new"); err != nil {
		t.Fatalf("Call: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("permissions = %o, want 755", perm)
	}
}

// A linked file is patched through the link, and the link survives: renaming
// over it would quietly turn it into a regular file.
func TestPatchFileFollowsASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := patch(t, link, "old", "new"); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got, want := contents(t, target), "new\n"; got != want {
		t.Errorf("target contents = %q, want %q", got, want)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced by a regular file")
	}
}

// A refused patch must leave nothing behind either.
func TestPatchFileLeavesNoTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("a\nb\n"), 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}

	if _, err := patch(t, path, "a", "c"); err != nil {
		t.Fatalf("Call: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only the file itself", len(entries))
	}
}

// A run stopped before anything was written must not write anything.
func TestPatchFileStopsWhenTheRunDoes(t *testing.T) {
	path := write(t, "config.yaml", "timeout: 30\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := NewPatchFile().Call(ctx, patchArgs(t, path, "30", "120")); err == nil {
		t.Fatal("a cancelled run still patched the file")
	}
	if got, want := contents(t, path), "timeout: 30\n"; got != want {
		t.Errorf("contents = %q, want the file untouched", got)
	}
}

// Announce is display only and must not fail on arguments it cannot read.
func TestPatchFileAnnounce(t *testing.T) {
	p := NewPatchFile()
	if got := p.Announce(patchArgs(t, "a.txt", "one\ntwo", "x")); !strings.Contains(got, "a.txt") {
		t.Errorf("Announce = %q, want it to name the file", got)
	}
	if got := p.Announce(json.RawMessage(`{`)); got != "" {
		t.Errorf("Announce of malformed arguments = %q, want empty", got)
	}
}
