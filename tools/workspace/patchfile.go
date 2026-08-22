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
	"unicode/utf8"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/tools"
)

// maxPatchSize caps the file this tool will read into memory. A file larger
// than this is one the model could not have read either, so a patch against it
// was written from a guess.
const maxPatchSize = 5 << 20

// TODO: a patch is not reversible, and this is where reversing one belongs.
// The tool has the old contents in hand already, so it can keep them itself —
// deterministically, without the model reasoning about how to undo anything,
// which is the only version of undo that was ever going to work. It is
// deliberately not the mktemp directory: that one is the agent's to use as it
// likes, and a harness that wrote into it would be writing into a place the
// model believes is its own.

// PatchFileParams is the input schema for the patchfile tool.
type PatchFileParams struct {
	// Path names the file to change. It must exist.
	Path string `json:"path"`
	// OldStr is the text to find, which must appear exactly once.
	OldStr string `json:"old_str"`
	// NewStr is what replaces it, and may be empty to delete the text.
	NewStr string `json:"new_str"`
}

// PatchFile replaces one stretch of text in a file with another.
//
// The whole of its safety is that old_str has to match exactly once. A patch
// that matches nothing means the model is working from a stale picture of the
// file, and a patch that matches twice means it has not said which place it
// meant; either way, changing the file would be acting on a guess. Both are
// refused, with different advice, because the two mistakes are corrected in
// different ways.
//
// Matching is over the file's bytes and not its lines, which is what makes the
// awkward edits possible: adding a newline to a file that ends without one is
// an ordinary patch of the last line, and nothing here adds or removes a
// trailing newline on its own.
type PatchFile struct{}

// NewPatchFile returns the tool.
func NewPatchFile() *PatchFile { return &PatchFile{} }

// Definition describes the tool to the model.
//
// This description is the only thing the model is told about patchfile, so it
// carries the usage rules itself — the system prompt says nothing about it,
// and it may not lean on any other tool being registered.
func (p *PatchFile) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: "patchfile",
		Description: strings.TrimSpace(`
Replace one stretch of text in an existing file with another.

  {"path": "config/server.yaml", "old_str": "  timeout: 30\n", "new_str": "  timeout: 120\n"}

OLD_STR MUST APPEAR IN THE FILE EXACTLY ONCE. That is the whole of what makes
this safe to use, and the call is refused when it appears twice or not at all:
matching twice means you have not said which place you meant, and matching
nothing means the file is not what you think it is.

The text is matched byte for byte. Indentation, trailing spaces, tabs against
spaces, and line endings all count. So copy old_str out of what you actually
read from the file rather than retyping it from memory, which is where nearly
every failed patch comes from.

To make a match unique, give more of the text around it — whole lines before
and after the part you are changing — and repeat those lines unchanged in
new_str. Do not try to make it unique by matching less.

An empty new_str deletes the text. There is no way to pass an empty old_str: it
would match everywhere. To insert, put the line you are inserting next to in
old_str and repeat it in new_str with your addition alongside it.

Matching is over the file's bytes, not its lines, so text that spans several
lines is one old_str, and a file that ends without a newline is changed by
patching its last line. Nothing is added or removed at the end of the file on
its own.

This tool only changes a file that already exists, and changes one place in it
per call. Make several calls to change several places, and read the file again
between calls if one changed what another was going to match.`),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type": "string",
					"description": "The file to change. It must already exist; this tool " +
						"never creates one.",
				},
				"old_str": map[string]any{
					"type": "string",
					"description": "The exact text to find, copied from what you read " +
						"out of the file. It must appear in the file exactly once, " +
						"matched byte for byte including indentation and line endings. " +
						"Required, and never empty.",
				},
				"new_str": map[string]any{
					"type": "string",
					"description": "What to put in its place. Empty deletes the text. " +
						"Repeat unchanged any surrounding lines you included in old_str " +
						"to make the match unique.",
				},
			},
			"required":             []string{"path", "old_str", "new_str"},
			"additionalProperties": false,
		},
	}
}

// parsePatch decodes and validates the model's arguments, as far as they can
// be judged without looking at the file.
func parsePatch(args json.RawMessage) (PatchFileParams, error) {
	var p PatchFileParams
	if err := json.Unmarshal(args, &p); err != nil {
		return p, fmt.Errorf("patchfile: arguments are not valid JSON: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		return p, errors.New("patchfile: the path parameter is required")
	}
	if p.OldStr == "" {
		return p, errors.New("patchfile: old_str is required and cannot be empty, " +
			"since an empty string matches everywhere in the file; to insert text, put " +
			"the line it goes next to in old_str and repeat that line in new_str with " +
			"your addition alongside it")
	}
	if p.OldStr == p.NewStr {
		return p, errors.New("patchfile: old_str and new_str are identical, so this " +
			"call would change nothing; if the file already reads the way you want it " +
			"to, there is nothing to patch")
	}
	return p, nil
}

// Announce summarizes a pending call for the user: the file, and the first
// line of what is being replaced. The tool's name is added by the registry.
func (p *PatchFile) Announce(args json.RawMessage) string {
	params, err := parsePatch(args)
	if err != nil {
		return ""
	}
	return params.Path + "\n" + tools.Detail("  - "+singleLine(params.OldStr))
}

// Call makes the replacement and reports what it did.
//
// There is only ever one place to report, since a call that matched anywhere
// else was refused above, so the result says that the match was replaced and
// the rest of the file left alone. A count would imply the number could have
// been something other than one.
func (p *PatchFile) Call(ctx context.Context, args json.RawMessage) (string, error) {
	params, err := parsePatch(args)
	if err != nil {
		return "", err
	}

	path, info, err := resolve(params.Path)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("patchfile: %w", err)
	}

	// TODO: this refuses anything that is not UTF-8, which includes text files
	// in an encoding that was perfectly ordinary where they were written —
	// Shift_JIS, GB18030, a Latin-1 configuration file. The right answer is to
	// detect the encoding, decode to UTF-8 for matching, and encode back on
	// write so the file keeps the form it had. Until then those files cannot
	// be patched at all, which is at least not silently wrong.
	if !utf8.Valid(data) {
		return "", fmt.Errorf("patchfile: %s is not valid UTF-8, and this tool only "+
			"changes text; patching it byte by byte would be as likely to corrupt it "+
			"as to change it", params.Path)
	}

	content := string(data)
	switch n := strings.Count(content, params.OldStr); n {
	case 1:
		// The one case that goes ahead.
	case 0:
		return "", fmt.Errorf("patchfile: old_str does not appear in %s. The text is "+
			"matched byte for byte, so a difference in indentation, a trailing space, a "+
			"tab where you wrote spaces, or a carriage return at the end of a line is "+
			"enough to miss. Read the file again and copy old_str out of what you read, "+
			"rather than writing it out from memory", params.Path)
	default:
		return "", fmt.Errorf("patchfile: old_str appears %d times in %s, so there is no "+
			"way to tell which one you meant. Include more of the text around the part "+
			"you are changing — whole lines before and after it — until what you pass "+
			"appears only once, and repeat those lines unchanged in new_str. Do not "+
			"match less text to get around this", n, params.Path)
	}

	patched := strings.Replace(content, params.OldStr, params.NewStr, 1)

	// The run may have been stopped while the file was being read. Everything
	// up to here has left the file alone, so this is the last moment at which
	// nothing has happened yet.
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if err := replaceFile(path, []byte(patched), info.Mode().Perm()); err != nil {
		return "", err
	}
	return fmt.Sprintf("patched %s: the one place old_str matched now reads as "+
		"new_str, and the rest of the file is byte for byte as it was", params.Path), nil
}

// resolve turns the path the model gave into the file to write, and the
// information needed to write it in the same shape it was found.
//
// Symbolic links are followed rather than refused, and the link itself is left
// alone: a repository full of linked configuration files is ordinary, and a
// rename over the link would quietly turn it into a regular file. The mode
// comes from the file the link points at, which is the one being rewritten.
func resolve(path string) (string, fs.FileInfo, error) {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return "", nil, fmt.Errorf("patchfile: there is no file at %s; this tool changes "+
			"a file that already exists and never creates one, so check the path, or "+
			"make the file first if it is meant to be new", path)
	}

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, fmt.Errorf("patchfile: %w", err)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("patchfile: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("patchfile: %s is not a regular file", path)
	}
	if info.Size() > maxPatchSize {
		return "", nil, fmt.Errorf("patchfile: %s is %d bytes, more than the %d this "+
			"tool will read; a file that large is one you could not have read either, "+
			"so a patch against it would be written from a guess", path, info.Size(), maxPatchSize)
	}
	return resolved, info, nil
}

// replaceFile writes data over path, keeping the mode it had.
//
// The file is written alongside its destination and renamed into place, the
// way a session snapshot is, so a run stopped midway through cannot leave a
// half-written file where a whole one used to be. The mode is carried across
// deliberately: os.CreateTemp makes a private file, and renaming that over a
// script would take the execute bit off it.
func replaceFile(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".agi.*")
	if err != nil {
		return fmt.Errorf("patchfile: %w", err)
	}
	defer os.Remove(tmp.Name()) // A no-op once the rename below has happened.

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("patchfile: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("patchfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("patchfile: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("patchfile: %w", err)
	}
	return nil
}
