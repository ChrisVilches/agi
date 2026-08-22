package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/tools"
)

// scratchPrefix begins the name of every directory this tool creates.
//
// It is there so the directories can be found again. Nothing records where
// they are — the path is in the conversation and in the log, and that is all —
// so a user looking for what an agent kept has one thing to go on, which is
// that the names all start the same way and sit together in one directory.
const scratchPrefix = "agi-scratch-"

// MkTempParams is the input schema for the mktemp tool.
type MkTempParams struct {
	// Purpose is why the agent wants the directory.
	//
	// It is required for the reason bash requires an explanation: asking for
	// the reason alongside the request makes the model state what it is about
	// to do before it does it, which is worth something whether or not anyone
	// reads it.
	//
	// It does not reach the directory's name. A purpose is a phrase written
	// for a person to read as the call happens, and folding one into a path
	// segment gave names long enough to be worse than no name at all. What a
	// listing has to do is show which directories are agi's, which the shared
	// prefix does; what each one was for is in the conversation and the log.
	Purpose string `json:"purpose"`
}

// MkTemp creates empty directories for an agent to keep things in.
//
// It exists rather than leaving this to bash because bash may not be
// registered, and because the harness decides two things a shell command
// would leave to chance: that the names share a prefix, so they can be found,
// and that the directory is new, so nothing already in it can be mistaken for
// the agent's own work.
//
// Nothing here removes a directory it made. Cleanup would mean deciding when
// an agent is finished with something it was never asked to account for, and
// guessing wrong destroys the one copy of a file the run was relying on.
type MkTemp struct {
	// Dir is where the directories are created. Empty means the system's
	// temporary directory, which is what TMPDIR names.
	Dir string
}

// NewMkTemp returns the tool.
func NewMkTemp() *MkTemp { return &MkTemp{} }

// Definition describes the tool to the model.
//
// This description is the only thing the model is told about mktemp, so it
// carries the usage rules itself — the system prompt says nothing about it,
// and it may not lean on any other tool being registered.
func (m *MkTemp) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: "mktemp",
		Description: strings.TrimSpace(`
Create a new empty directory for your own use and return its path.

  {"purpose": "keep a copy of config.yaml before editing it"}

Use it whenever you need somewhere to put something that is not part of what
the user asked for: a copy of a file you are about to change, output you want
to look at in a later step, a working copy of something you mean to experiment
on. Keeping that material out of the user's own directories is the point —
nothing you leave here can be mistaken for the work itself.

BEFORE YOU CHANGE OR DELETE A FILE YOU DID NOT CREATE, CONSIDER COPYING IT HERE
FIRST. Nothing else in this run can undo what you do to the user's files, so a
copy made beforehand is the only way back if the change turns out to be wrong.
Copy first, then change; a copy made afterwards preserves the mistake.

The directory is new and empty on every call, and its path comes back as the
result. Use that path exactly as it was returned. Do not guess at one, do not
build one out of a prefix you saw here, and do not write to a path from an
earlier call unless this tool is what gave it to you.

Nothing ever removes these directories, so you do not have to clean up after
yourself and should not try. They are made in the machine's temporary
directory, which on many machines is emptied when it restarts, so they are the
right place for something you may want later in this run and the wrong place
for anything the user is meant to keep. Whatever the user asked for belongs
where they asked for it.`),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"purpose": map[string]any{
					"type": "string",
					"description": "A few words, in the second person, saying what you " +
						"are going to keep in this directory and why. It is shown to " +
						"the user as the directory is made.",
				},
			},
			"required":             []string{"purpose"},
			"additionalProperties": false,
		},
	}
}

// parseMkTemp decodes and validates the model's arguments.
func parseMkTemp(args json.RawMessage) (MkTempParams, error) {
	var p MkTempParams
	if err := json.Unmarshal(args, &p); err != nil {
		return p, fmt.Errorf("mktemp: arguments are not valid JSON: %w", err)
	}
	if strings.TrimSpace(p.Purpose) == "" {
		return p, errors.New("mktemp: the purpose parameter is required; say in a few " +
			"words what you mean to keep in the directory, since it names the directory " +
			"and is the only record of what it is for")
	}
	return p, nil
}

// Announce summarizes a pending call for the user: the reason the agent gave.
// The tool's name is added by the registry.
func (m *MkTemp) Announce(args json.RawMessage) string {
	p, err := parseMkTemp(args)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(p.Purpose)
}

// Report shows the directory the call made, dimmed under the purpose the agent
// gave for it. The path is the whole of the result, and it is worth a line of
// its own: it is the one thing about a run that nothing else records, and the
// user watching has no other way to find what an agent kept.
func (m *MkTemp) Report(result string) string {
	path := strings.TrimSpace(result)
	if path == "" {
		return ""
	}
	return tools.Detail("  " + path)
}

// Call creates the directory and returns its path, which is the whole of what
// the model needs: it has to write the path back verbatim, so the result is
// the path and nothing around it.
func (m *MkTemp) Call(_ context.Context, args json.RawMessage) (string, error) {
	// The purpose is not used here, but a call without one is still refused:
	// it is shown to the user as the directory is made, and asking for it is
	// what makes the model say why before it is given anywhere to write.
	if _, err := parseMkTemp(args); err != nil {
		return "", err
	}

	dir, err := os.MkdirTemp(m.Dir, scratchPrefix+"*")
	if err != nil {
		return "", fmt.Errorf("mktemp: %w", err)
	}
	return dir, nil
}
