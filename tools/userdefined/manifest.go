package userdefined

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ChrisVilches/agi/tools"
)

// ManifestName is the file a folder of tools is described by. It is a fixed
// name at the root of the folder rather than something the user points at,
// because what is being named on the command line is a folder of tools and a
// folder of tools is what this makes one.
const ManifestName = "manifest.json"

// nameSyntax is what a tool may be called. It is the intersection of what
// providers accept for a tool name, and agi holds to it so that a name which
// would be rejected in the middle of a run — as part of a request, with
// whatever the provider makes of it — is refused before the run starts.
var nameSyntax = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// manifest is the file's shape. A tool's name and the file that runs it are
// two different things and are named separately: what a script is called on
// disk is the user's business, and it is usually not what the tool should be
// called when the model reads a list of them.
//
// Hooks is read and thrown away, and it is the one field here that is not
// about tools at all. A folder's manifest.json describes everything the user
// keeps in that folder, and hooks are the other thing they may keep there;
// the flag that names the folder decides which half is read, so --tools takes
// the tools and never arms a guard the user did not ask for. But the decoder
// below refuses a field it does not know, because a typo with a silent
// version is worth a message, and a folder holding both would otherwise fail
// to load as either. So the field exists to be recognized and ignored, which
// is why it is json.RawMessage: this package has no business knowing what a
// hook looks like, only that the word is not a mistake.
type manifest struct {
	Tools []entry         `json:"tools"`
	Hooks json.RawMessage `json:"hooks"`
}

type entry struct {
	Name        string         `json:"name"`
	Path        string         `json:"path"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// LoadAll reads every folder given and returns the tools they declare, in the
// order the folders were named and the order each manifest lists them in.
//
// The order is kept and never rearranged, because it is not agi's to decide
// and nothing depends on it: the registry offers tools to the model in name
// order whatever order they arrived in.
//
// Two tools of the same name fail the whole thing, wherever the two came from.
// The model addresses a tool by its name and would have no way of telling them
// apart, and neither would a person reading the run afterwards, so the choice
// of which one wins is not agi's to make quietly. Deduplicating them is the
// user's: rename one, or leave one of the folders out.
func LoadAll(dirs []string) ([]tools.Tool, error) {
	var loaded []tools.Tool
	// Which manifest each name came from, so a collision can name both.
	from := make(map[string]string)

	for _, dir := range dirs {
		found, err := Load(dir)
		if err != nil {
			return nil, err
		}
		where := filepath.Join(dir, ManifestName)
		for _, t := range found {
			if first, taken := from[t.name]; taken {
				return nil, duplicate(t.name, first, where)
			}
			from[t.name] = where
			loaded = append(loaded, t)
		}
	}
	return loaded, nil
}

// Load reads one folder's manifest and returns the tools it declares, with
// everything about them already checked: the script exists and is executable,
// the description says something, and the parameters are a schema a call can
// be checked against.
//
// All of that is done here, when the folder is read, rather than when a tool
// is first called. A manifest naming a file that is not there is a mistake in
// how the run was invoked, and it costs a message at the start instead of a
// failed call somewhere in the middle of a conversation the user is paying
// for.
func Load(dir string) ([]*Tool, error) {
	where := filepath.Join(dir, ManifestName)
	raw, err := os.ReadFile(where)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("there is no %s in %s; a folder of tools is described by one",
				ManifestName, dir)
		}
		return nil, fmt.Errorf("reading %s: %w", where, err)
	}

	m, err := decode(raw, where)
	if err != nil {
		return nil, err
	}
	if len(m.Tools) == 0 {
		// A folder holding only hooks is a folder somebody named to the wrong
		// flag, and saying which flag reads which half turns the message into
		// the fix. Saying it only when there are hooks keeps it out of the way
		// of the ordinary case, which is a manifest that declares nothing.
		if len(m.Hooks) > 0 {
			return nil, fmt.Errorf("%s declares hooks but no tools; --tools reads the tools "+
				"a folder declares and --hooks reads its hooks", where)
		}
		return nil, fmt.Errorf("%s declares no tools", where)
	}

	found := make([]*Tool, 0, len(m.Tools))
	seen := make(map[string]bool, len(m.Tools))
	for i, e := range m.Tools {
		t, err := e.compile(dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", where, describes(e, i), err)
		}
		if seen[t.name] {
			return nil, duplicate(t.name, where, where)
		}
		seen[t.name] = true
		found = append(found, t)
	}
	return found, nil
}

// decode reads the manifest, refusing a field it does not know.
//
// An unknown field is refused rather than ignored because every one of them is
// a mistake with a silent version: "paths" instead of "path" would otherwise
// be a tool with no script, and "tool" instead of "tools" a folder that
// declares nothing. Both are typos, and both are worth a message.
func decode(raw []byte, where string) (*manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var m manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("reading %s: %w", where, err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("reading %s: more than one JSON value", where)
	}
	return &m, nil
}

// compile checks one entry and returns the tool it declares.
func (e entry) compile(dir string) (*Tool, error) {
	name := strings.TrimSpace(e.Name)
	if name == "" {
		return nil, errors.New("has no name")
	}
	if !nameSyntax.MatchString(name) {
		return nil, fmt.Errorf("is named %q, which a provider will not accept as a tool name; "+
			"use letters, digits, underscores and hyphens", name)
	}

	description := strings.TrimSpace(e.Description)
	if description == "" {
		return nil, errors.New("has no description; it is the only thing the model is ever " +
			"told about a tool, so a tool without one cannot be called on purpose")
	}

	script, err := resolve(dir, e.Path)
	if err != nil {
		return nil, err
	}

	params, check, err := compileParams(e.Parameters)
	if err != nil {
		return nil, err
	}

	return &Tool{
		name:        name,
		description: description,
		script:      script,
		params:      params,
		check:       check,
	}, nil
}

// resolve turns the path a manifest gave into the executable it names, and
// refuses everything that is not one.
//
// The path is relative to the folder, and only ever inside it. A folder of
// tools is a thing that can be moved, copied, and handed to somebody else, and
// a manifest reaching out to an absolute path or climbing out with .. would be
// one that works on the machine it was written on and nowhere else.
func resolve(dir, raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", errors.New("names no file to run")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("names the absolute path %s; a manifest names files inside its own folder", path)
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("names %s, which is outside the folder the manifest is in", path)
	}

	full := filepath.Join(dir, clean)
	absolute, err := filepath.Abs(full)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", full, err)
	}

	// Stat rather than Lstat: a symbolic link to a script is an ordinary way
	// to keep one file in two places, and what matters is what it points at.
	info, err := os.Stat(absolute)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("names %s, and there is no such file", absolute)
	case err != nil:
		return "", fmt.Errorf("reading %s: %w", absolute, err)
	case info.IsDir():
		return "", fmt.Errorf("names %s, which is a directory", absolute)
	case !info.Mode().IsRegular():
		return "", fmt.Errorf("names %s, which is not a regular file", absolute)
	case info.Mode().Perm()&0o111 == 0:
		// The script is run directly rather than handed to an interpreter, so
		// the executable bit is what makes it runnable at all. Saying so now
		// is worth more than the kernel's refusal mid-run.
		return "", fmt.Errorf("names %s, which is not executable; run chmod +x on it", absolute)
	}
	return absolute, nil
}

// describes names an entry in a message about it: by its name when it has one,
// and by its position when it does not, which is the only thing left to go on.
func describes(e entry, i int) string {
	if name := strings.TrimSpace(e.Name); name != "" {
		return fmt.Sprintf("the tool %q", name)
	}
	return fmt.Sprintf("tool %d", i+1)
}

// duplicate phrases the one thing a user has to fix themselves. Which of two
// tools of the same name should win is not something agi can decide quietly,
// so it says where both are and stops.
func duplicate(name, first, second string) error {
	if first == second {
		return fmt.Errorf("%s declares two tools named %q; deduplicate them, since the model "+
			"addresses a tool by its name and could not tell them apart", first, name)
	}
	return fmt.Errorf("two tools are named %q, one in %s and one in %s; deduplicate them — "+
		"rename one, or leave out one of the folders — since the model addresses a tool by "+
		"its name and could not tell them apart", name, first, second)
}
