package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/tools"
)

// WriteFileParams is the input schema for the writefile tool.
type WriteFileParams struct {
	// Path names the file to create. It must not exist, and every directory
	// leading to it must.
	Path string `json:"path"`
	// Content is what the file will hold, written verbatim. It may be empty,
	// since an empty file is a real thing to want.
	Content string `json:"content"`
}

// WriteFile creates a file that is not there yet and writes it in one go.
//
// It only ever creates, which is the whole of what makes it safe: a path that
// already holds something is refused rather than overwritten, so no call here
// can destroy work. That is enforced by opening with O_EXCL rather than by the
// check that precedes it — the check is there to say which of the several
// things that can be wrong actually is, and the flag is there because between
// looking and writing anything could have happened.
//
// It does not create directories either. Making the parents of a path on the
// model's behalf turns a typo into a tree of empty directories nobody asked
// for, and the path that was meant is then still not written. A missing
// directory is reported by name so the model can decide whether it wants that
// directory or a different path.
type WriteFile struct{}

// NewWriteFile returns the tool.
func NewWriteFile() *WriteFile { return &WriteFile{} }

// Definition describes the tool to the model.
//
// This description is the only thing the model is told about writefile, so it
// carries the usage rules itself — the system prompt says nothing about it,
// and it may not lean on any other tool being registered.
func (w *WriteFile) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: "writefile",
		Description: strings.TrimSpace(`
Create a new file and write it in one call.

  {"path": "internal/limits/limits.go", "content": "package limits\n\nconst Max = 10\n"}

THE FILE MUST NOT EXIST YET. A path that already holds a file, a directory, or
anything else is refused and nothing there is touched, so this tool can never
destroy work. If you meant to change something that is already there, this is
not the call to make.

EVERY DIRECTORY IN THE PATH MUST ALREADY EXIST. No directory is created for
you: a missing one is refused and named back to you, so you can decide whether
that directory is really wanted or whether the path was wrong. Create the
directories first if you want them.

Content is written exactly as you give it, byte for byte, with nothing added.
There is no trailing newline unless you write one, so end the content with \n
for the file kinds that expect it. Write the whole file — content is what the
file will hold, not something appended to anything.

An empty content is allowed and makes an empty file, which is occasionally what
is wanted. A relative path is resolved against the working directory.`),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type": "string",
					"description": "Where to create the file. Nothing may exist at this " +
						"path, and every directory leading to it must already exist; " +
						"neither is created for you.",
				},
				"content": map[string]any{
					"type": "string",
					"description": "The complete contents of the new file, written byte " +
						"for byte with nothing added. May be empty to create an empty " +
						"file. Include a trailing newline if the file wants one.",
				},
			},
			"required":             []string{"path", "content"},
			"additionalProperties": false,
		},
	}
}

// parseWrite decodes and validates the model's arguments, as far as they can
// be judged without looking at the filesystem.
func parseWrite(args json.RawMessage) (WriteFileParams, error) {
	var p WriteFileParams
	if err := json.Unmarshal(args, &p); err != nil {
		return p, fmt.Errorf("writefile: arguments are not valid JSON: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		return p, errors.New("writefile: the path parameter is required")
	}
	return p, nil
}

// Announce summarizes a pending call for the user: the file, then the first
// line going into it, marked the way a diff marks an addition. Both are
// arguments of the call, so neither has to wait for a result. An empty file
// shows the path alone, since there is no first line to show and a bare + sign
// would have to be explained to be read.
func (w *WriteFile) Announce(args json.RawMessage) string {
	p, err := parseWrite(args)
	if err != nil {
		return ""
	}
	if p.Content == "" {
		return p.Path
	}
	return p.Path + "\n" + tools.Detail("  + "+tools.SingleLine(p.Content))
}

// Call creates the file and reports what it wrote.
//
// The three ways this goes wrong — something is already there, a directory in
// the path is not, or the operating system said no — are reported apart from
// one another, because they are corrected differently: by choosing another
// path, by making the directory first, and by not writing there at all.
func (w *WriteFile) Call(ctx context.Context, args json.RawMessage) (string, error) {
	p, err := parseWrite(args)
	if err != nil {
		return "", err
	}

	if err := checkParent(p.Path); err != nil {
		return "", err
	}
	if err := checkAbsent(p.Path); err != nil {
		return "", err
	}

	// Everything up to here has left the filesystem alone, so this is the last
	// moment at which nothing has happened yet.
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// O_EXCL and not the check above is what guarantees nothing is
	// overwritten: the check says which thing went wrong, this says that
	// nothing appeared in the meantime.
	f, err := os.OpenFile(p.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", existsError(p.Path)
		}
		if errors.Is(err, fs.ErrPermission) {
			return "", fmt.Errorf("writefile: the operating system refused to create %s: "+
				"permission denied. The directory it goes in does not allow this user to "+
				"create files in it. Nothing was written; write somewhere this user owns "+
				"instead, and do not try to change the permissions of a directory the "+
				"user did not ask you to touch", p.Path)
		}
		return "", fmt.Errorf("writefile: creating %s: %w", p.Path, err)
	}

	if _, err := f.WriteString(p.Content); err != nil {
		f.Close()
		// The file was created by this call and holds at most part of what was
		// meant to go in it, so leaving it would leave a lie behind under a
		// name the model will believe it wrote.
		os.Remove(p.Path)
		return "", fmt.Errorf("writefile: writing %s: %w. The partly written file has "+
			"been removed, so the path is as it was before this call", p.Path, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(p.Path)
		return "", fmt.Errorf("writefile: writing %s: %w. The partly written file has "+
			"been removed, so the path is as it was before this call", p.Path, err)
	}

	return fmt.Sprintf("created %s and wrote %d bytes to it; nothing existed at that "+
		"path before", p.Path, len(p.Content)), nil
}

// checkParent reports whether the directory the file would go in is there and
// is a directory, so that a missing one is named rather than being created.
func checkParent(path string) error {
	dir := filepath.Dir(path)

	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("writefile: there is no directory at %s, so %s cannot be "+
			"created there. This tool never creates directories: making them for you "+
			"would turn a mistyped path into a tree of empty directories, and the file "+
			"you meant would still not be written. Check the path, or create the "+
			"directory first if it is really wanted", dir, path)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("writefile: the operating system refused to look at %s: "+
			"permission denied, so there is no way to tell whether %s can be created. "+
			"Nothing was written; write somewhere this user can reach instead", dir, path)
	case err != nil:
		return fmt.Errorf("writefile: %w", err)
	case !info.IsDir():
		return fmt.Errorf("writefile: %s is a file, not a directory, so nothing can be "+
			"created inside it; %s treats it as a directory in the middle of the path", dir, path)
	}
	return nil
}

// checkAbsent reports whether anything is already at the path, so that the
// refusal can say what is in the way. Lstat and not Stat: a symbolic link is
// something at the path, whether or not it points anywhere, and following it
// would let a call write through a link that was never asked about.
func checkAbsent(path string) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("writefile: the operating system refused to look at %s: "+
			"permission denied, so there is no way to tell whether it already exists. "+
			"Nothing was written; write somewhere this user can reach instead", path)
	case err != nil:
		return fmt.Errorf("writefile: %w", err)
	case info.IsDir():
		return fmt.Errorf("writefile: %s is an existing directory, not a file, so "+
			"nothing can be written there. It is untouched; choose a path inside it or "+
			"a different name", path)
	}
	return existsError(path)
}

// existsError is the refusal for a path that already holds something. It is
// one function because the check before the write and the O_EXCL that catches
// what appeared afterwards mean the same thing to the model.
func existsError(path string) error {
	return fmt.Errorf("writefile: %s already exists, and this tool only creates files "+
		"that are not there yet. It has not been touched and nothing was written. If "+
		"you meant to replace what is there, this is not the call to make; if you meant "+
		"a new file, choose a path that is free", path)
}
