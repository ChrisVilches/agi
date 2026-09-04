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

func writeArgs(t *testing.T, path, content string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(WriteFileParams{Path: path, Content: content})
	if err != nil {
		t.Fatalf("marshalling args: %v", err)
	}
	return b
}

func create(t *testing.T, path, content string) (string, error) {
	t.Helper()
	return NewWriteFile().Call(context.Background(), writeArgs(t, path, content))
}

func TestWriteFileIsATool(t *testing.T) {
	var _ tools.Tool = NewWriteFile()
	var _ tools.Announcer = NewWriteFile()
}

// Workspace tools must never explain themselves in the system prompt; a run
// may not include them at all.
func TestWriteFileIsNotASystemPrompter(t *testing.T) {
	if _, ok := any(NewWriteFile()).(tools.SystemPrompter); ok {
		t.Error("writefile implements SystemPrompter, but only native tools may")
	}
}

// The description is all the model is told, so it has to carry the usage rules.
func TestWriteFileDefinition(t *testing.T) {
	def := NewWriteFile().Definition()
	if def.Name != "writefile" {
		t.Errorf("name = %q, want %q", def.Name, "writefile")
	}
	if len(strings.TrimSpace(def.Description)) < 100 {
		t.Error("description is too thin to be the only thing the model is told")
	}

	required, ok := def.Parameters["required"].([]string)
	if !ok {
		t.Fatalf("required = %T, want []string", def.Parameters["required"])
	}
	if strings.Join(required, ",") != "path,content" {
		t.Errorf("required = %v, want both", required)
	}
}

func TestWriteFileCreatesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "limits.go")

	out, err := create(t, path, "package limits\n")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(out, "limits.go") {
		t.Errorf("result = %q, want it to name the file", out)
	}
	if got, want := contents(t, path), "package limits\n"; got != want {
		t.Errorf("contents = %q, want %q", got, want)
	}
}

// Content is written byte for byte, so nothing is added at the end of a file
// that did not ask for it.
func TestWriteFileAddsNoTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "noeol.txt")

	if _, err := create(t, path, "last line"); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got, want := contents(t, path), "last line"; got != want {
		t.Errorf("contents = %q, want %q", got, want)
	}
}

// An empty file is a real thing to want, so empty content is not a mistake.
func TestWriteFileCreatesAnEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty")

	if _, err := create(t, path, ""); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := contents(t, path); got != "" {
		t.Errorf("contents = %q, want empty", got)
	}
}

// The refusal that matters: nothing this tool does can destroy work.
func TestWriteFileRefusesAnExistingFile(t *testing.T) {
	path := write(t, "server.yaml", "host: localhost\n")

	_, err := create(t, path, "clobbered\n")
	if err == nil {
		t.Fatal("Call() = nil, want a refusal for a path that already exists")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q, want it to say the file already exists", err)
	}
	if got, want := contents(t, path), "host: localhost\n"; got != want {
		t.Errorf("contents = %q, want the file left as it was (%q)", got, want)
	}
}

// A symbolic link is something at the path, whether or not it points anywhere,
// and following one would write through a link nobody asked about.
func TestWriteFileRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("writing target: %v", err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := create(t, link, "clobbered\n"); err == nil {
		t.Fatal("Call() = nil, want a refusal for a path that is a symbolic link")
	}
	if got, want := contents(t, target), "original\n"; got != want {
		t.Errorf("contents = %q, want the link's target left as it was (%q)", got, want)
	}
}

// A directory in the way is refused differently from a file, because it is a
// different mistake: the model wanted a path inside it, or a different name.
func TestWriteFileRefusesAnExistingDirectory(t *testing.T) {
	dir := t.TempDir()

	_, err := create(t, dir, "contents\n")
	if err == nil {
		t.Fatal("Call() = nil, want a refusal for a path that is a directory")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("error = %q, want it to say a directory is in the way", err)
	}
}

// A missing parent is named rather than created, so the model can tell a typo
// from a directory it really wants.
func TestWriteFileRefusesAMissingDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope", "deeper")
	path := filepath.Join(missing, "file.txt")

	_, err := create(t, path, "contents\n")
	if err == nil {
		t.Fatal("Call() = nil, want a refusal for a path whose directory is missing")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error = %q, want it to name the directory that is missing", err)
	}
	if !strings.Contains(err.Error(), "never creates directories") {
		t.Errorf("error = %q, want it to say no directory was created", err)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Errorf("%s was created, want the tool to create no directories", missing)
	}
}

// A file in the middle of the path is not a directory, and saying so is more
// use than reporting whatever errno comes back.
func TestWriteFileRefusesAFileAsADirectory(t *testing.T) {
	blocking := write(t, "blocking", "in the way\n")
	path := filepath.Join(blocking, "file.txt")

	_, err := create(t, path, "contents\n")
	if err == nil {
		t.Fatal("Call() = nil, want a refusal when a file stands in for a directory")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error = %q, want it to say the parent is a file", err)
	}
}

// Permission errors are explained rather than passed through bare, since the
// model has to decide what to do about them.
func TestWriteFileExplainsPermissionErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("making directory: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	path := filepath.Join(dir, "file.txt")
	_, err := create(t, path, "contents\n")
	if err == nil {
		t.Fatal("Call() = nil, want a refusal for a directory this user cannot write")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error = %q, want it to say permission was denied", err)
	}
	if !strings.Contains(err.Error(), "Nothing was written") {
		t.Errorf("error = %q, want it to say nothing was written", err)
	}
}

func TestWriteFileRequiresAPath(t *testing.T) {
	if _, err := create(t, "   ", "contents\n"); err == nil {
		t.Fatal("Call() = nil, want a refusal for an empty path")
	}
}

func TestWriteFileRejectsBadJSON(t *testing.T) {
	if _, err := NewWriteFile().Call(context.Background(), json.RawMessage(`{`)); err == nil {
		t.Fatal("Call() = nil, want a refusal for arguments that are not JSON")
	}
}

// The run may have been stopped between the checks and the write, and nothing
// has happened yet at that point.
func TestWriteFileStopsWhenCancelled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := NewWriteFile().Call(ctx, writeArgs(t, path, "contents\n")); err == nil {
		t.Fatal("Call() = nil, want the cancellation")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("the file was created, want a cancelled call to write nothing")
	}
}

// The announcement is the path and the first line going in, marked the way a
// diff marks an addition.
func TestWriteFileAnnounce(t *testing.T) {
	got := NewWriteFile().Announce(writeArgs(t, "a/b.txt", "first line\nsecond line\n"))
	if !strings.Contains(got, "a/b.txt") {
		t.Errorf("Announce() = %q, want it to name the file", got)
	}
	if !strings.Contains(got, "+ first line") {
		t.Errorf("Announce() = %q, want the first line marked as an addition", got)
	}
	if strings.Contains(got, "second line") {
		t.Errorf("Announce() = %q, want only the first line of the content", got)
	}

	// An empty file has no first line to show, and a bare + would have to be
	// explained to be read.
	if got := NewWriteFile().Announce(writeArgs(t, "a/b.txt", "")); got != "a/b.txt" {
		t.Errorf("Announce() = %q, want the path alone for an empty file", got)
	}

	if got := NewWriteFile().Announce(json.RawMessage(`{`)); got != "" {
		t.Errorf("Announce() = %q, want nothing for arguments that are not JSON", got)
	}
}
