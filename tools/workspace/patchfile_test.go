package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
	return NewPatchFile("", "").Call(context.Background(), patchArgs(t, path, old, new))
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
	var _ tools.Tool = NewPatchFile("", "")
	var _ tools.Announcer = NewPatchFile("", "")
}

// Workspace tools must never explain themselves in the system prompt; a run
// may not include them at all.
func TestPatchFileIsNotASystemPrompter(t *testing.T) {
	if _, ok := any(NewPatchFile("", "")).(tools.SystemPrompter); ok {
		t.Error("patchfile implements SystemPrompter, but only control-flow tools may")
	}
}

// The description is all the model is told, so it has to carry the usage rules.
func TestPatchFileDefinition(t *testing.T) {
	def := NewPatchFile("", "").Definition()
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
	// The count is always one, so the result says the match was replaced and
	// the rest of the file left alone, rather than reporting a number that
	// could never have been anything else.
	if !strings.Contains(out, "server.yaml") || strings.Contains(out, "1 occurrence") {
		t.Errorf("result = %q, want it to name the file without counting matches", out)
	}
	if !strings.Contains(out, "rest of the file") {
		t.Errorf("result = %q, want it to say the rest of the file is untouched", out)
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
	if _, err := NewPatchFile("", "").Call(context.Background(), json.RawMessage(`{`)); err == nil {
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

	if _, err := NewPatchFile("", "").Call(ctx, patchArgs(t, path, "30", "120")); err == nil {
		t.Fatal("a cancelled run still patched the file")
	}
	if got, want := contents(t, path), "timeout: 30\n"; got != want {
		t.Errorf("contents = %q, want the file untouched", got)
	}
}

// Announce is display only and must not fail on arguments it cannot read.
func TestPatchFileAnnounce(t *testing.T) {
	p := NewPatchFile("", "")
	if got := p.Announce(patchArgs(t, "a.txt", "one\ntwo", "x")); !strings.Contains(got, "a.txt") {
		t.Errorf("Announce = %q, want it to name the file", got)
	}
	if got := p.Announce(json.RawMessage(`{`)); got != "" {
		t.Errorf("Announce of malformed arguments = %q, want empty", got)
	}
}

// Both texts are arguments of the call, so the announcement shows what is
// going out and what is coming in, without waiting for the call to happen.
func TestPatchFileAnnounceShowsBothSides(t *testing.T) {
	got := NewPatchFile("", "").Announce(patchArgs(t, "a.txt", "timeout: 30", "timeout: 120"))
	if !strings.Contains(got, "- timeout: 30") {
		t.Errorf("Announce = %q, want the text being replaced on a - line", got)
	}
	if !strings.Contains(got, "+ timeout: 120") {
		t.Errorf("Announce = %q, want the replacement on a + line", got)
	}
}

// A deletion has nothing coming in, and a diff of one has no + line either.
func TestPatchFileAnnounceOmitsAnEmptyReplacement(t *testing.T) {
	got := NewPatchFile("", "").Announce(patchArgs(t, "a.txt", "drop me\n", ""))
	if !strings.Contains(got, "- drop me") {
		t.Errorf("Announce = %q, want the deleted text", got)
	}
	if strings.Contains(got, "+") {
		t.Errorf("Announce = %q, want no + line for a deletion", got)
	}
}

// The tool keeps a copy of every file it changes, so that a patch made on a
// wrong guess can be undone. The copy is the file as it was, byte for byte,
// and the result says where it went.
func TestPatchFileKeepsACopy(t *testing.T) {
	backups := filepath.Join(t.TempDir(), "backups")
	path := write(t, "config.yaml", "timeout: 30\nretries: 2\n")

	p := NewPatchFile(backups, "main")
	result, err := p.Call(context.Background(), patchArgs(t, path, "timeout: 30", "timeout: 120"))
	if err != nil {
		t.Fatalf("Call() = %v", err)
	}

	kept := backupPath(t, result)
	if got := contents(t, kept); got != "timeout: 30\nretries: 2\n" {
		t.Errorf("the copy holds %q, want the file as it was before the patch", got)
	}
	if got := contents(t, path); got != "timeout: 120\nretries: 2\n" {
		t.Errorf("the file holds %q, want it patched", got)
	}
	if filepath.Dir(kept) != backups {
		t.Errorf("the copy went to %q, want it in %q", filepath.Dir(kept), backups)
	}
}

// The name is a timestamp, then the agent, then the file's own name. The
// timestamp leads because sorting the directory by name is how a user finds
// the earliest copy of a file — there is nothing else to tell them, and with
// no terminal they never saw the paths go past.
func TestBackupNamesSortIntoTheOrderTheyWereMade(t *testing.T) {
	backups := filepath.Join(t.TempDir(), "backups")
	path := write(t, "notes.txt", "one\n")
	p := NewPatchFile(backups, "main")

	var names []string
	for _, step := range [][2]string{{"one", "two"}, {"two", "three"}, {"three", "four"}} {
		result, err := p.Call(context.Background(), patchArgs(t, path, step[0], step[1]))
		if err != nil {
			t.Fatalf("Call() = %v", err)
		}
		names = append(names, filepath.Base(backupPath(t, result)))
	}

	if !slices.IsSorted(names) {
		t.Errorf("names = %v, want them to sort into the order they were made", names)
	}
	for i, name := range names {
		if !strings.HasSuffix(name, "-main-notes.txt") {
			t.Errorf("name %d = %q, want it to end with the agent and the file's own name", i, name)
		}
	}

	// The earliest copy is the file as the run found it, which is the whole
	// point of being able to sort them.
	if got := contents(t, filepath.Join(backups, names[0])); got != "one\n" {
		t.Errorf("the earliest copy holds %q, want the file as the run found it", got)
	}
}

// Subagents patch at the same time as each other, so the agent's name is in
// the copy's. It is the name the run records under, so a copy and the log line
// about it can be read together.
func TestBackupsOfDifferentAgentsDoNotCollide(t *testing.T) {
	backups := filepath.Join(t.TempDir(), "backups")
	path := write(t, "shared.txt", "a\nb\n")

	first, err := NewPatchFile(backups, "main").Call(context.Background(), patchArgs(t, path, "a\n", "A\n"))
	if err != nil {
		t.Fatalf("Call() = %v", err)
	}
	second, err := NewPatchFile(backups, "subagent-2").Call(context.Background(), patchArgs(t, path, "b\n", "B\n"))
	if err != nil {
		t.Fatalf("Call() = %v", err)
	}

	one, two := filepath.Base(backupPath(t, first)), filepath.Base(backupPath(t, second))
	if one == two {
		t.Fatalf("both agents wrote %q", one)
	}
	if !strings.Contains(one, "-main-") || !strings.Contains(two, "-subagent-2-") {
		t.Errorf("names = %q and %q, want each to carry the agent that made it", one, two)
	}
}

// A run with nowhere to keep copies keeps none, and says nothing about them.
// A promise this run cannot keep would be worse than no promise: it would have
// the model skip the copy it should have taken itself.
func TestNoBackupDirectoryMeansNoPromise(t *testing.T) {
	path := write(t, "config.yaml", "timeout: 30\n")

	p := NewPatchFile("", "main")
	if strings.Contains(strings.ToLower(p.Definition().Description), "copy of the file") {
		t.Error("the description promises a copy, but this run has nowhere to keep one")
	}

	result, err := p.Call(context.Background(), patchArgs(t, path, "30", "120"))
	if err != nil {
		t.Fatalf("Call() = %v", err)
	}
	if strings.Contains(result, backupLinePrefix) {
		t.Errorf("result = %q, want nothing about a copy", result)
	}
	if got := p.Report(result); got != "" {
		t.Errorf("Report() = %q, want nothing to report", got)
	}
}

// The description has to tell the model the copy is taken for it, or it will
// go on taking one itself before every patch.
func TestBackupDescriptionTellsTheModelNotToCopyFirst(t *testing.T) {
	desc := strings.ToLower(NewPatchFile(t.TempDir(), "main").Definition().Description)
	for _, want := range []string{"copy of the file", "do not copy the file anywhere yourself"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description does not mention %q", want)
		}
	}
}

// A refused patch changes nothing, so there is nothing worth keeping a copy
// of. Copies are taken after the call is certain to go ahead, or a run that
// guessed at a file ten times would leave ten identical copies of it.
func TestARefusedPatchKeepsNoCopy(t *testing.T) {
	backups := filepath.Join(t.TempDir(), "backups")
	path := write(t, "config.yaml", "a\na\n")
	p := NewPatchFile(backups, "main")

	for _, c := range []struct {
		name     string
		path     string
		old, new string
	}{
		{"no match", path, "nowhere", "x"},
		{"two matches", path, "a\n", "b\n"},
		{"no such file", filepath.Join(t.TempDir(), "missing.txt"), "a", "b"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := p.Call(context.Background(), patchArgs(t, c.path, c.old, c.new)); err == nil {
				t.Fatal("Call() = nil, want the patch refused")
			}
		})
	}

	if entries, err := os.ReadDir(backups); err == nil && len(entries) > 0 {
		t.Errorf("the backup directory holds %d file(s), want none kept for a patch that never happened", len(entries))
	}
}

// What the user is shown is what the model was told: Report reads the path
// back out of the result rather than remembering it.
func TestPatchFileReportsWhereTheCopyWent(t *testing.T) {
	backups := filepath.Join(t.TempDir(), "backups")
	path := write(t, "config.yaml", "timeout: 30\n")

	p := NewPatchFile(backups, "main")
	result, err := p.Call(context.Background(), patchArgs(t, path, "30", "120"))
	if err != nil {
		t.Fatalf("Call() = %v", err)
	}

	report := p.Report(result)
	if !strings.Contains(report, backupPath(t, result)) {
		t.Errorf("Report() = %q, want it to show where the copy went", report)
	}
	if strings.Count(report, "\n") > 1 {
		t.Errorf("Report() = %q, want a line or two and not the whole result", report)
	}
}

// A file whose name is longer than a filesystem will accept once a timestamp
// and an agent have been put in front of it is backed up under a shortened
// name. Backing it up under a shorter name is better than not backing it up.
func TestALongNameIsShortenedRatherThanRefused(t *testing.T) {
	backups := filepath.Join(t.TempDir(), "backups")
	path := write(t, strings.Repeat("n", 200)+".txt", "before\n")

	result, err := NewPatchFile(backups, "main").Call(context.Background(), patchArgs(t, path, "before", "after"))
	if err != nil {
		t.Fatalf("Call() = %v", err)
	}
	if got := contents(t, backupPath(t, result)); got != "before\n" {
		t.Errorf("the copy holds %q, want the file as it was", got)
	}
}

// backupPath pulls the copy's path out of a result, failing the test if the
// result does not name one.
func backupPath(t *testing.T, result string) string {
	t.Helper()
	for _, line := range strings.Split(result, "\n") {
		if path, ok := strings.CutPrefix(line, backupLinePrefix); ok {
			return path
		}
	}
	t.Fatalf("result = %q, want it to say where the copy went", result)
	return ""
}
