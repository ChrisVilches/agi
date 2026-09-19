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

// The order the folders were given and the order each file lists its entries
// are two different rules, and one test holding one of each cannot tell them
// apart: a reader that reversed a file's entries would still pass the test
// above, because a folder with one hook in it reads the same either way.
//
// This pins both at once, through the flag, with two hooks in each of two
// folders. It is the combination that matters — hooks run in sequence and one
// hook's output is the next one's input, so what a tool is finally called
// with depends on all four being in the right places.
func TestResourcesKeepsBothOrdersAtOnce(t *testing.T) {
	first, second := twoHooks(t, "a"), twoHooks(t, "b")

	brought, err := resourcesOf(t, "--resources", second, "--resources", first, "prompt")
	if err != nil {
		t.Fatalf("resourcesFlag() = %v", err)
	}

	var got []string
	for _, h := range brought.hooks.All() {
		got = append(got, filepath.Base(h.Command()))
	}
	want := []string{"b-one.sh", "b-two.sh", "a-one.sh", "a-two.sh"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("hooks = %v, want %v: the folders in the order given, and within each "+
			"folder the order hooks.json lists them", got, want)
	}
}

// twoHooks writes a folder whose hooks.json declares two hooks, so that the
// order within one file can be told from the order between folders.
func twoHooks(t *testing.T, suffix string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, hooks.FileName, `{"PreToolUse": [
	  {"command": "./`+suffix+`-one.sh", "args": []},
	  {"command": "./`+suffix+`-two.sh", "args": []}
	]}`, 0o644)
	for _, name := range []string{suffix + "-one.sh", suffix + "-two.sh"} {
		write(t, dir, name, "#!/bin/sh\nexit 0\n", 0o755)
	}
	return dir
}

// A hook whose matcher accepts no tool this run has could never fire, and
// nothing else about the run would ever say so: it loads, it is listed, and
// the call it was written to guard goes straight through. `^Bash$` for a tool
// called `bash` is one keystroke from correct and produces silence, which is
// worse than no guard at all.
func TestResourcesRefusesAHookThatCouldNeverRun(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, hooks.FileName, `{"PreToolUse": [
	  {"matcher": "^Bash$", "command": "./guard.sh", "args": []}
	]}`, 0o644)
	write(t, dir, "guard.sh", "#!/bin/sh\nexit 0\n", 0o755)

	_, err := resourcesOf(t, "--resources", dir, "prompt")
	if err == nil {
		t.Fatal("resourcesFlag() = nil, want a hook that watches nothing refused")
	}
	// The matcher is quoted back and the names there are are listed, since
	// the whole mistake is that the user thinks one of them is the other.
	for _, want := range []string{"^Bash$", "bash"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("resourcesFlag() = %v, want it to mention %q", err, want)
		}
	}
}

// The dead-matcher check is asked only of a hook whose event is about a tool
// call. A MessageDisplay hook has no matcher — writing one is refused where
// the file is read — and one written without a matcher must not be caught
// here by luck, since Hook.Watches answers true for an absent matcher
// whatever the event.
func TestResourcesAcceptsAHookOnAnEventWithNoTool(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, hooks.FileName, `{"MessageDisplay": [
	  {"command": "./render.sh", "args": []}
	]}`, 0o644)
	write(t, dir, "render.sh", "#!/bin/sh\nexit 0\n", 0o755)

	got, err := resourcesOf(t, "--resources", dir, "prompt")
	if err != nil {
		t.Fatalf("resourcesFlag() = %v, want a display hook accepted", err)
	}
	if got.hooks.Len() != 1 {
		t.Errorf("Len() = %d, want the hook loaded", got.hooks.Len())
	}
}

// Writing a matcher on that event is refused, and the message is about
// matchers rather than about the tools this run has: a display hook watches
// no tool and was never going to.
func TestResourcesRefusesAMatcherOnAnEventWithNoTool(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, hooks.FileName, `{"MessageDisplay": [
	  {"matcher": "^bash$", "command": "./render.sh", "args": []}
	]}`, 0o644)
	write(t, dir, "render.sh", "#!/bin/sh\nexit 0\n", 0o755)

	_, err := resourcesOf(t, "--resources", dir, "prompt")
	if err == nil {
		t.Fatal("resourcesFlag() = nil, want a matcher on a display hook refused")
	}
	if !strings.Contains(err.Error(), "matcher") {
		t.Errorf("resourcesFlag() = %v, want the message to name the matcher", err)
	}
	if strings.Contains(err.Error(), "could never run") {
		t.Errorf("resourcesFlag() = %v, want a message about matchers rather than about "+
			"the tools the run has", err)
	}
}

// The same matcher spelled correctly is accepted, so the check is about the
// name and not about matchers in general.
func TestResourcesAcceptsAHookThatWatchesARealTool(t *testing.T) {
	for _, matcher := range []string{`"matcher": "^bash$",`, `"matcher": "bash|writefile",`, `"matcher": "*",`, ``} {
		dir := t.TempDir()
		write(t, dir, hooks.FileName, `{"PreToolUse": [
		  {`+matcher+` "command": "./guard.sh", "args": []}
		]}`, 0o644)
		write(t, dir, "guard.sh", "#!/bin/sh\nexit 0\n", 0o755)

		if _, err := resourcesOf(t, "--resources", dir, "prompt"); err != nil {
			t.Errorf("resourcesFlag() with matcher %s = %v, want it accepted", matcher, err)
		}
	}
}

// A hook may watch a tool the same folder brought, which is the ordinary way
// a folder of resources is written: the guard and the thing it guards arrive
// together.
func TestResourcesAcceptsAHookWatchingAToolFromTheSameFolder(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, userdefined.FileName,
		`[{"name": "deploy", "path": "run.sh", "description": "Ship it."}]`, 0o644)
	write(t, dir, "run.sh", "#!/bin/sh\necho done\n", 0o755)
	write(t, dir, hooks.FileName, `{"PreToolUse": [
	  {"matcher": "^deploy$", "command": "./guard.sh", "args": []}
	]}`, 0o644)
	write(t, dir, "guard.sh", "#!/bin/sh\nexit 0\n", 0o755)

	if _, err := resourcesOf(t, "--resources", dir, "prompt"); err != nil {
		t.Errorf("resourcesFlag() = %v, want a hook watching this folder's own tool accepted", err)
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
