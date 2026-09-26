package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/hooks"
	"github.com/ChrisVilches/agi/tools/userdefined"
	"github.com/urfave/cli/v3"
)

// resourcesOf runs the app far enough to read --resources and returns
// everything the run would have been given. The action is replaced, so nothing
// is sent anywhere and no session is made: the flag is the whole of what is
// under test.
func resourcesOf(t *testing.T, args ...string) (resources, error) {
	t.Helper()

	var got resources
	var readErr error
	app := NewApp("test")
	app.Action = func(_ context.Context, cmd *cli.Command) error {
		got, readErr = resourcesFlag(cmd)
		return nil
	}
	if err := app.Run(context.Background(), append([]string{"agi"}, args...)); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	return got, readErr
}

// write puts one file in a folder, making the folders on the way.
func write(t *testing.T, dir, name, body string, mode os.FileMode) {
	t.Helper()
	at := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		t.Fatalf("making %s: %v", filepath.Dir(at), err)
	}
	if err := os.WriteFile(at, []byte(body), mode); err != nil {
		t.Fatalf("writing %s: %v", at, err)
	}
}

// everything writes one folder holding all three kinds: a tool, a hook, and a
// skill. The names carry the suffix so two folders can be told apart.
func everything(t *testing.T, suffix string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, userdefined.FileName, `[{"name": "tool-`+suffix+
		`", "path": "run.sh", "description": "A tool used in a test."}]`, 0o644)
	write(t, dir, "run.sh", "#!/bin/sh\necho done\n", 0o755)
	write(t, dir, hooks.FileName, `{"PreToolUse": [{"command": "./guard-`+suffix+`.sh", "args": []}]}`, 0o644)
	write(t, dir, "guard-"+suffix+".sh", "#!/bin/sh\nexit 0\n", 0o755)
	write(t, dir, "skill-"+suffix+"/SKILL.md",
		"---\nname: A skill\ndescription: What it is for.\n---\n\nHow it is done.\n", 0o644)
	return dir
}

// One folder holds all three kinds, and naming it once brings in all three.
// That is the whole point of the flag: before it there were three, and a user
// with one folder had to name it to each of them.
func TestResourcesBringsInEveryKindAtOnce(t *testing.T) {
	brought, err := resourcesOf(t, "--resources", everything(t, "a"), "prompt")
	if err != nil {
		t.Fatalf("resourcesFlag() = %v", err)
	}
	if got := names(brought.tools); len(got) != 1 || got[0] != "tool-a" {
		t.Errorf("tools = %v, want the one the tools.json declares", got)
	}
	if brought.hooks.Len() != 1 {
		t.Errorf("hooks = %d, want the one the hooks.json declares", brought.hooks.Len())
	}
	if len(brought.skills) != 1 || brought.skills[0].ID != "skill-a" {
		t.Errorf("skills = %v, want the one kept under the folder", skillNames(brought.skills))
	}
}

// The order is the order the folders were given, for every kind at once. It
// decides two things: hooks run in sequence and one hook's output is the next
// one's input, and the skills are listed to the model in this order.
func TestResourcesKeepsTheOrderTheFoldersWereGivenIn(t *testing.T) {
	first, second := everything(t, "a"), everything(t, "b")

	brought, err := resourcesOf(t, "--resources", second, "--resources", first, "prompt")
	if err != nil {
		t.Fatalf("resourcesFlag() = %v", err)
	}
	if got := strings.Join(names(brought.tools), ","); got != "tool-b,tool-a" {
		t.Errorf("tools = %s, want the folders read in the order given", got)
	}
	if got := skillNames(brought.skills); got != "skill-b,skill-a" {
		t.Errorf("skills = %s, want the folders read in the order given", got)
	}
	ran := brought.hooks.All()
	if len(ran) != 2 || !strings.HasSuffix(ran[0].Command(), "guard-b.sh") ||
		!strings.HasSuffix(ran[1].Command(), "guard-a.sh") {
		t.Errorf("hooks run %v, want the folders read in the order given", ran)
	}
}

// A folder need not hold all three. Naming one that holds only skills is an
// ordinary thing to do, and the two files it does not have are not missing.
func TestResourcesAcceptsAFolderHoldingOneKind(t *testing.T) {
	brought, err := resourcesOf(t, "--resources", skillFolder(t, "writing"), "prompt")
	if err != nil {
		t.Fatalf("resourcesFlag() = %v", err)
	}
	if len(brought.skills) != 1 {
		t.Fatalf("skills = %v, want the one in the folder", skillNames(brought.skills))
	}
	if len(brought.tools) != 0 || brought.hooks.Len() != 0 {
		t.Errorf("a folder of skills brought %d tools and %d hooks, want none",
			len(brought.tools), brought.hooks.Len())
	}
}

// What it may not do is hold none of them. Naming a folder is asking for what
// is in it, and a folder with nothing in it is the wrong path far more often
// than it is a deliberate request for nothing.
func TestResourcesRefusesAFolderThatBringsNothing(t *testing.T) {
	_, err := resourcesOf(t, "--resources", t.TempDir(), "prompt")
	if err == nil {
		t.Fatal("resourcesFlag() = nil, want a folder holding nothing refused")
	}
	for _, want := range []string{userdefined.FileName, hooks.FileName} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("resourcesFlag() = %v, want %s named as something it could have held", err, want)
		}
	}
}

// A name matching nothing is a mistake — a typo, a path from another machine,
// a value the shell mangled — and so is a name matching a file.
func TestResourcesRefusesWhatIsNotAFolder(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nowhere")
	if _, err := resourcesOf(t, "--resources", missing, "prompt"); err == nil {
		t.Error("resourcesFlag() accepted a folder that is not there")
	}

	dir := t.TempDir()
	write(t, dir, "a-file", "not a folder\n", 0o644)
	_, err := resourcesOf(t, "--resources", filepath.Join(dir, "a-file"), "prompt")
	if err == nil {
		t.Fatal("resourcesFlag() accepted a file")
	}
	if !strings.Contains(err.Error(), "--resources") {
		t.Errorf("resourcesFlag() = %v, want the flag named", err)
	}
}
