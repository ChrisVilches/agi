package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/prompt"
	"github.com/ChrisVilches/agi/skills"
	"github.com/ChrisVilches/agi/tools/native"
	"github.com/urfave/cli/v3"
)

// skillFolder writes a folder holding one skill per name given, and returns
// the folder. A skill's name is the name of the folder it is in, so the two
// are written together.
func skillFolder(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		at := filepath.Join(dir, name)
		if err := os.MkdirAll(at, 0o755); err != nil {
			t.Fatalf("making the skill's folder: %v", err)
		}
		text := "---\nname: " + name + "\ndescription: What " + name +
			" is for.\ntags: [" + name + "]\n---\n\nHow " + name + " is done.\n"
		if err := os.WriteFile(filepath.Join(at, "SKILL.md"), []byte(text), 0o644); err != nil {
			t.Fatalf("writing the skill: %v", err)
		}
	}
	return dir
}

// oneSkill is a run's worth of skills, for a test that needs the tool
// registered and does not care which skill it loads.
func oneSkill(t *testing.T) []skills.Skill {
	t.Helper()
	found, err := skills.Find([]string{skillFolder(t, "writing")})
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	return found
}

// skillsOf runs the app far enough to read --skills and returns the skills the
// run would have been given. The action is replaced, so nothing is sent
// anywhere and no session is made: the flag is the whole of what is under test.
func skillsOf(t *testing.T, args ...string) ([]skills.Skill, error) {
	t.Helper()

	var got []skills.Skill
	var readErr error
	app := NewApp("test")
	app.Action = func(_ context.Context, cmd *cli.Command) error {
		got, readErr = skillsFlag(cmd)
		return nil
	}
	if err := app.Run(context.Background(), append([]string{"agi"}, args...)); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	return got, readErr
}

func TestSkillsFlagFindsTheSkills(t *testing.T) {
	dir := skillFolder(t, "writing")

	found, err := skillsOf(t, "--skills", dir, "prompt")
	if err != nil {
		t.Fatalf("skillsFlag() = %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("skillsFlag() found %d skills, want 1", len(found))
	}
	if found[0].Name != "writing" {
		t.Errorf("found %q, want writing", found[0].Name)
	}
	if found[0].Path != filepath.Join(dir, "writing") {
		t.Errorf("Path = %q, want %q", found[0].Path, filepath.Join(dir, "writing"))
	}
}

// A run that names no folder of skills has none, and nothing about it changes.
func TestSkillsFlagIsOptional(t *testing.T) {
	found, err := skillsOf(t, "prompt")
	if err != nil {
		t.Fatalf("skillsFlag() = %v", err)
	}
	if len(found) != 0 {
		t.Errorf("skillsFlag() found %d skills, want none", len(found))
	}
}

// The flag is repeatable, and the order the folders were given in is the order
// the skills come out in, because that is the order the model is shown them.
func TestSkillsFlagKeepsTheOrderOfTheFolders(t *testing.T) {
	first := skillFolder(t, "writing")
	second := skillFolder(t, "testing")

	found, err := skillsOf(t, "--skills", first, "--skills", second, "prompt")
	if err != nil {
		t.Fatalf("skillsFlag() = %v", err)
	}
	if got := skillNames(found); got != "writing,testing" {
		t.Errorf("skillsFlag() found %s, want writing,testing", got)
	}

	swapped, err := skillsOf(t, "--skills", second, "--skills", first, "prompt")
	if err != nil {
		t.Fatalf("skillsFlag() = %v", err)
	}
	if got := skillNames(swapped); got != "testing,writing" {
		t.Errorf("skillsFlag() found %s, want testing,writing", got)
	}
}

// A mistake in a folder costs a message rather than a run, so it is found
// before a gateway is contacted or a session is created.
func TestSkillsFlagRefusesAFolderThatIsNotThere(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nowhere")

	if _, err := skillsOf(t, "--skills", missing, "prompt"); err == nil {
		t.Error("skillsFlag() accepted a folder that is not there")
	}
}

func TestSkillsFlagRefusesAMalformedSkill(t *testing.T) {
	dir := t.TempDir()
	at := filepath.Join(dir, "writing")
	if err := os.MkdirAll(at, 0o755); err != nil {
		t.Fatalf("making the folder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(at, "SKILL.md"), []byte("---\nname: editing\ndescription: x\n---\n\ntext\n"), 0o644); err != nil {
		t.Fatalf("writing the skill: %v", err)
	}

	_, err := skillsOf(t, "--skills", dir, "prompt")
	if err == nil {
		t.Fatal("skillsFlag() accepted a skill named after another folder")
	}
	if !strings.Contains(err.Error(), "editing") {
		t.Errorf("skillsFlag() = %v, want the name reported", err)
	}
}

func TestSkillsFlagRefusesNothingButSpaces(t *testing.T) {
	_, err := skillsOf(t, "--skills", "   ", "prompt")
	if err == nil || !strings.Contains(err.Error(), "--skills") {
		t.Errorf("skillsFlag() = %v, want a refusal naming the flag", err)
	}
}

// The path arrived as a shell argument, and space around it is never part of
// the path meant.
func TestSkillsFlagTrimsTheFolder(t *testing.T) {
	dir := skillFolder(t, "writing")

	found, err := skillsOf(t, "--skills", "  "+dir+"  ", "prompt")
	if err != nil {
		t.Fatalf("skillsFlag() = %v", err)
	}
	if len(found) != 1 {
		t.Errorf("skillsFlag() found %d skills, want 1", len(found))
	}
}

// The tool that loads a skill exists only in a run that has skills to load.
func TestLoadSkillIsRegisteredOnlyWithSkills(t *testing.T) {
	without, err := defaultRegistry(quiet(), &scriptedClient{}, nolog(), "", nil, nil, defaultMaxTurns)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	if _, ok := without.Lookup(native.LoadSkillTool); ok {
		t.Errorf("a run with no skills was given the %s tool", native.LoadSkillTool)
	}

	with, err := defaultRegistry(quiet(), &scriptedClient{}, nolog(), "", nil, oneSkill(t), defaultMaxTurns)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	if _, ok := with.Lookup(native.LoadSkillTool); !ok {
		t.Errorf("a run with skills was not given the %s tool", native.LoadSkillTool)
	}
}

// The skills section is the last thing in the prompt, after everything the
// tools explain, because it is a catalogue rather than an instruction.
func TestSkillsComeLastInTheSystemPrompt(t *testing.T) {
	found := oneSkill(t)
	r, err := defaultRegistry(quiet(), &scriptedClient{}, nolog(), "", nil, found, defaultMaxTurns)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}

	got := systemPromptFor(prompt.Base(), r, skillsSection(found))
	section := strings.Index(got, "=== skills ===")
	if section < 0 {
		t.Fatalf("the system prompt has no skills section:\n%s", got)
	}
	if terminate := strings.Index(got, prompt.Terminate()); terminate < 0 || terminate > section {
		t.Errorf("the skills section is not after the terminate guidance")
	}
	if !strings.HasSuffix(got, skills.Describe(found)) {
		t.Errorf("the system prompt does not end with the list of skills:\n%s", got)
	}
}

// The explanation and the list are one section: neither is any use alone.
func TestTheSkillsSectionExplainsAndThenLists(t *testing.T) {
	found := oneSkill(t)

	got := skillsSection(found)
	if !strings.HasPrefix(got, "=== skills ===") {
		t.Errorf("the section has no delimiter:\n%s", got)
	}
	explanation := strings.Index(got, prompt.Skills())
	list := strings.Index(got, "<available_skills>")
	switch {
	case explanation < 0:
		t.Error("the section does not explain what a skill is")
	case list < 0:
		t.Error("the section does not list the skills")
	case explanation > list:
		t.Error("the section lists the skills before explaining them")
	}
	if !strings.Contains(got, "- **writing**: What writing is for.") {
		t.Errorf("the section does not describe the skill:\n%s", got)
	}
}

// A run with no skills says nothing about them, which is the rule that keeps
// an unregistered tool out of the system prompt.
func TestARunWithNoSkillsSaysNothingAboutThem(t *testing.T) {
	if got := skillsSection(nil); got != "" {
		t.Errorf("skillsSection(nil) = %q, want empty", got)
	}

	r, err := defaultRegistry(quiet(), &scriptedClient{}, nolog(), "", nil, nil, defaultMaxTurns)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	got := systemPromptFor(prompt.Base(), r, skillsSection(nil))
	for _, unwanted := range []string{"=== skills ===", "<available_skills>", native.LoadSkillTool} {
		if strings.Contains(got, unwanted) {
			t.Errorf("a run with no skills mentions %q in its system prompt", unwanted)
		}
	}
}

// A subagent is given no skills, so its prompt says nothing about them either.
func TestASubagentIsToldNothingAboutSkills(t *testing.T) {
	r, err := workspaceRegistry("", subagentName(native.Brief{Number: 1}), nil)
	if err != nil {
		t.Fatalf("workspaceRegistry() = %v", err)
	}

	got := subagentSystemPrompt(r, nil)
	if strings.Contains(got, "available_skills") || strings.Contains(got, native.LoadSkillTool) {
		t.Errorf("a subagent's prompt mentions skills:\n%s", got)
	}
}

// The same folders read again produce the same prompt, which is the invariant
// that lets a snapshot store no prompt at all.
func TestTheSkillsSectionIsTheSameEveryRun(t *testing.T) {
	dir := skillFolder(t, "writing", "testing")

	first, err := skills.Find([]string{dir})
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	second, err := skills.Find([]string{dir})
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if skillsSection(first) != skillsSection(second) {
		t.Error("two readings of one folder produced different sections")
	}
}

// skillNames renders what was found, for a message about it.
func skillNames(found []skills.Skill) string {
	var got []string
	for _, s := range found {
		got = append(got, s.Name)
	}
	return strings.Join(got, ",")
}

// A subagent is given the skills its task named, and the list and the tool
// arrive together: the section is built from the same skills the tool is.
func TestASubagentGetsTheSkillsItsTaskNamed(t *testing.T) {
	found := oneSkill(t)
	r, err := workspaceRegistry("", subagentName(native.Brief{Number: 1}), nil)
	if err != nil {
		t.Fatalf("workspaceRegistry() = %v", err)
	}
	if err := r.Register(native.NewLoadSkill(found)); err != nil {
		t.Fatalf("Register() = %v", err)
	}

	got := subagentSystemPrompt(r, found)
	if !strings.Contains(got, "=== skills ===") {
		t.Errorf("a subagent given skills is not told about them:\n%s", got)
	}
	if !strings.HasSuffix(got, skills.Describe(found)) {
		t.Errorf("a subagent's prompt does not end with the skills it was given:\n%s", got)
	}
	// It is still a subagent: the prompt it reads is the subagent's own.
	if !strings.HasPrefix(got, prompt.Subagent()) {
		t.Errorf("a subagent's prompt does not start with the subagent instruction:\n%s", got)
	}
	// And it still cannot spawn subagents or end the run.
	for _, name := range []string{native.SubagentsTool, "terminate"} {
		if _, ok := r.Lookup(name); ok {
			t.Errorf("a subagent was given the %s tool", name)
		}
	}
}

// The subagents tool offers the skills field only in a run that has skills,
// which is the same rule that registers loadskill only then.
func TestTheSubagentsToolOffersSkillsOnlyWhenThereAreSome(t *testing.T) {
	without, err := defaultRegistry(quiet(), &scriptedClient{}, nolog(), "", nil, nil, defaultMaxTurns)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	spawner, ok := without.Lookup(native.SubagentsTool)
	if !ok {
		t.Fatal("the run has no subagents tool")
	}
	if strings.Contains(spawner.Definition().Description, "skills") {
		t.Error("a run with no skills describes giving a subagent one")
	}

	with, err := defaultRegistry(quiet(), &scriptedClient{}, nolog(), "", nil, oneSkill(t), defaultMaxTurns)
	if err != nil {
		t.Fatalf("defaultRegistry() = %v", err)
	}
	spawner, ok = with.Lookup(native.SubagentsTool)
	if !ok {
		t.Fatal("the run has no subagents tool")
	}
	if !strings.Contains(spawner.Definition().Description, "skills") {
		t.Error("a run with skills does not say a subagent can be given one")
	}
}
