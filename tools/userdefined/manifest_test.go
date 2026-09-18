package userdefined

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// folder writes a tools.json and returns the directory holding it. Scripts are
// added with script().
func folder(t *testing.T, declared string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(declared), 0o644); err != nil {
		t.Fatalf("writing %s: %v", FileName, err)
	}
	return dir
}

// script writes an executable file in dir and returns its path.
func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("making the script's directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("writing the script: %v", err)
	}
	return path
}

// echoArgs prints the one argument it was given, which is how a test sees what
// the protocol actually handed the script.
const echoArgs = "#!/bin/sh\nprintf '%s' \"$1\"\n"

const oneTool = `[
  {
    "name": "weather",
    "path": "weather.sh",
    "description": "Report the weather in a city.",
    "parameters": {
      "type": "object",
      "properties": {"city": {"type": "string", "description": "The city."}},
      "required": ["city"],
      "additionalProperties": false
    }
  }
]`

func TestLoadReadsTheManifest(t *testing.T) {
	dir := folder(t, oneTool)
	path := script(t, dir, "weather.sh", echoArgs)

	found, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("Load() returned %d tools, want 1", len(found))
	}

	tool := found[0]
	if tool.Name() != "weather" {
		t.Errorf("Name() = %q, want weather", tool.Name())
	}
	// The script is resolved to an absolute path, so nothing about a call
	// depends on where agi is being run from.
	if !filepath.IsAbs(tool.Script()) {
		t.Errorf("Script() = %q, want an absolute path", tool.Script())
	}
	if resolved, _ := filepath.EvalSymlinks(tool.Script()); resolved != mustEval(t, path) {
		t.Errorf("Script() = %q, want %q", tool.Script(), path)
	}

	// The name of the tool and the name of the file are different things.
	def := tool.Definition()
	if def.Name != "weather" || strings.Contains(def.Name, ".sh") {
		t.Errorf("Definition().Name = %q, want the manifest's name and not the file's", def.Name)
	}
	if def.Description != "Report the weather in a city." {
		t.Errorf("Definition().Description = %q", def.Description)
	}
	// The parameters reach the model as the manifest wrote them.
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok || props["city"] == nil {
		t.Errorf("Definition().Parameters = %v, want the declared properties", def.Parameters)
	}
}

// A folder with no tools.json declares no tools, which is not a mistake: the
// file is one of three a folder of resources may hold, and a folder of skills
// and hooks alone is an ordinary folder to name.
func TestLoadPassesOverAFolderWithNoToolsFile(t *testing.T) {
	found, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load() of a folder with no %s = %v", FileName, err)
	}
	if len(found) != 0 {
		t.Errorf("Load() found %d tools, want none", len(found))
	}
}

// A typo in a field name is a tool with no script or a folder with no tools,
// and both are worth a message rather than a silent omission.
func TestLoadRefusesUnknownFields(t *testing.T) {
	dir := folder(t, `[{"name": "x", "paths": "x.sh", "description": "d"}]`)
	script(t, dir, "x.sh", echoArgs)

	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "paths") {
		t.Errorf("Load() = %v, want the unknown field named", err)
	}
}

// The hooks a folder holds are in a file of their own and are none of this
// package's business. A tools.json that holds a hook is a tools.json with a
// tool in it that has nothing agi recognizes.
func TestLoadReadsNothingButTools(t *testing.T) {
	dir := folder(t, `[{"matcher": "bash", "command": "./guard.sh"}]`)

	if _, err := Load(dir); err == nil {
		t.Error("Load() of a file holding a hook = nil, want an error")
	}
}

// A file that is there and declares nothing is the opposite of a folder with
// no file: somebody wrote it and meant something by it.
// A file that is there and declares nothing says what no file says, and is
// equally fine — the same rule a hooks.json follows. Emptying a file is how
// somebody turns something off without deleting what they mean to write
// again.
func TestLoadAcceptsAFileDeclaringNoTools(t *testing.T) {
	found, err := Load(folder(t, `[]`))
	if err != nil {
		t.Fatalf("Load() of a file declaring nothing = %v, want no tools and no complaint", err)
	}
	if len(found) != 0 {
		t.Errorf("Load() found %d tools, want none", len(found))
	}
}

// Declaring nothing is a decision; a file that cannot be read as tools at all
// is a mistake, and those are still refused.
func TestLoadStillRefusesAFileItCannotRead(t *testing.T) {
	if _, err := Load(folder(t, `not json`)); err == nil {
		t.Error("Load() of a file that is not JSON = nil, want an error")
	}
	if _, err := Load(folder(t, `{"tools": []}`)); err == nil {
		t.Error("Load() of an object = nil, want an error; the file is an array of tools")
	}
}

func TestLoadChecksEveryEntry(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		want     string
	}{
		{
			name:     "no name",
			manifest: `[{"path": "x.sh", "description": "d"}]`,
			want:     "has no name",
		},
		{
			name:     "a name a provider would refuse",
			manifest: `[{"name": "my tool", "path": "x.sh", "description": "d"}]`,
			want:     "tool name",
		},
		{
			name:     "no description",
			manifest: `[{"name": "x", "path": "x.sh", "description": "   "}]`,
			want:     "description",
		},
		{
			name:     "no path",
			manifest: `[{"name": "x", "description": "d"}]`,
			want:     "names no file",
		},
		{
			name:     "a file that is not there",
			manifest: `[{"name": "x", "path": "absent.sh", "description": "d"}]`,
			want:     "no such file",
		},
		{
			name:     "an absolute path",
			manifest: `[{"name": "x", "path": "/usr/bin/env", "description": "d"}]`,
			want:     "absolute path",
		},
		{
			name:     "a path out of the folder",
			manifest: `[{"name": "x", "path": "../x.sh", "description": "d"}]`,
			want:     "outside the folder",
		},
		{
			name:     "a directory",
			manifest: `[{"name": "x", "path": "sub", "description": "d"}]`,
			want:     "directory",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := folder(t, c.manifest)
			script(t, dir, "x.sh", echoArgs)
			if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
				t.Fatalf("making a directory: %v", err)
			}
			_, err := Load(dir)
			if err == nil {
				t.Fatalf("Load() = nil, want an error about %s", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("Load() = %v, want it to mention %q", err, c.want)
			}
		})
	}
}

// The executable bit is what makes a script runnable at all, since it is run
// directly rather than handed to an interpreter.
func TestLoadRefusesAScriptThatIsNotExecutable(t *testing.T) {
	dir := folder(t, oneTool)
	path := filepath.Join(dir, "weather.sh")
	if err := os.WriteFile(path, []byte(echoArgs), 0o644); err != nil {
		t.Fatalf("writing the script: %v", err)
	}

	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load() of a script without the executable bit = nil, want an error")
	}
	if !strings.Contains(err.Error(), "chmod +x") {
		t.Errorf("Load() = %v, want it to say how to fix it", err)
	}
}

// The file the script lives in and the name the model calls it by are
// deliberately separate, and a subdirectory inside the folder is fine.
func TestLoadTakesTheNameFromTheManifestAndNotTheFile(t *testing.T) {
	dir := folder(t, `[
		{"name": "weather", "path": "bin/fetch-weather-v2.sh", "description": "d"}
	]`)
	script(t, dir, "bin/fetch-weather-v2.sh", echoArgs)

	found, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if found[0].Name() != "weather" {
		t.Errorf("Name() = %q, want weather", found[0].Name())
	}
}

func TestLoadRefusesTwoToolsOfOneNameInOneManifest(t *testing.T) {
	dir := folder(t, `[
		{"name": "weather", "path": "x.sh", "description": "d"},
		{"name": "weather", "path": "x.sh", "description": "other"}
	]`)
	script(t, dir, "x.sh", echoArgs)

	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load() of a manifest with a repeated name = nil, want an error")
	}
	if !strings.Contains(err.Error(), "deduplicate") {
		t.Errorf("Load() = %v, want it to say what the user has to do", err)
	}
}

// Folders may be given in any order and are not rearranged; what is refused is
// two tools that answer to the same name, wherever they came from.
func TestLoadAllKeepsOrderAndRefusesCollisions(t *testing.T) {
	first := folder(t, `[
		{"name": "zebra", "path": "x.sh", "description": "d"},
		{"name": "alpha", "path": "x.sh", "description": "d"}
	]`)
	script(t, first, "x.sh", echoArgs)

	second := folder(t, `[{"name": "mike", "path": "y.sh", "description": "d"}]`)
	script(t, second, "y.sh", echoArgs)

	loaded, err := LoadAll([]string{first, second})
	if err != nil {
		t.Fatalf("LoadAll() = %v", err)
	}
	var names []string
	for _, tool := range loaded {
		names = append(names, tool.Definition().Name)
	}
	want := "zebra,alpha,mike"
	if strings.Join(names, ",") != want {
		t.Errorf("LoadAll() = %v, want %s", names, want)
	}

	clash := folder(t, `[{"name": "alpha", "path": "z.sh", "description": "d"}]`)
	script(t, clash, "z.sh", echoArgs)

	_, err = LoadAll([]string{first, clash})
	if err == nil {
		t.Fatal("LoadAll() with a name in two folders = nil, want an error")
	}
	// Both folders are named, since which one to change is the user's call.
	if !strings.Contains(err.Error(), first) || !strings.Contains(err.Error(), clash) {
		t.Errorf("LoadAll() = %v, want both manifests named", err)
	}
}

func TestLoadAllOfNothing(t *testing.T) {
	loaded, err := LoadAll(nil)
	if err != nil {
		t.Fatalf("LoadAll(nil) = %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("LoadAll(nil) = %v, want no tools", loaded)
	}
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolving %s: %v", path, err)
	}
	return resolved
}
