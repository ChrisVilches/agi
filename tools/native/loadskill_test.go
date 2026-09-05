package native

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/skills"
)

// skillsIn writes a folder holding one skill per name and returns what Find
// makes of it, so a test works with the same values a run does.
func skillsIn(t *testing.T, names ...string) []skills.Skill {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		at := filepath.Join(dir, name)
		if err := os.MkdirAll(at, 0o755); err != nil {
			t.Fatalf("making the skill's folder: %v", err)
		}
		text := "---\nname: " + name + "\ndescription: What " + name +
			" is for.\n---\n\nHow " + name + " is done.\n"
		if err := os.WriteFile(filepath.Join(at, "SKILL.md"), []byte(text), 0o644); err != nil {
			t.Fatalf("writing the skill: %v", err)
		}
	}

	found, err := skills.Find([]string{dir})
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	return found
}

func load(t *testing.T, tool *LoadSkill, name string) (string, error) {
	t.Helper()
	return tool.Call(context.Background(), json.RawMessage(`{"name":`+quote(name)+`}`))
}

func quote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func TestLoadSkillReturnsTheInstructions(t *testing.T) {
	tool := NewLoadSkill(skillsIn(t, "writing"), nil)

	out, err := load(t, tool, "writing")
	if err != nil {
		t.Fatalf("Call() = %v", err)
	}
	if !strings.Contains(out, "How writing is done.") {
		t.Errorf("Call() = %q, want the skill's instructions", out)
	}
	// The text is wrapped so the model can tell where a document somebody else
	// wrote begins and ends, and the tag says which skill it is.
	if !strings.Contains(out, "<skill name=\"writing\">\n") || !strings.HasSuffix(out, "</skill>") {
		t.Errorf("Call() = %q, want the instructions wrapped", out)
	}
	// The frontmatter was read when the skill was found, and handing it back
	// would return the two fields the skill was chosen by.
	if strings.Contains(out, "description:") {
		t.Errorf("Call() = %q, want the frontmatter left out", out)
	}
}

// A skill loads once: the instructions stay in the conversation, so a second
// copy would say nothing the first did not.
func TestLoadSkillRefusesToLoadOneTwice(t *testing.T) {
	tool := NewLoadSkill(skillsIn(t, "writing"), nil)

	if _, err := load(t, tool, "writing"); err != nil {
		t.Fatalf("Call() = %v", err)
	}
	_, err := load(t, tool, "writing")
	if err == nil {
		t.Fatal("Call() loaded the same skill twice")
	}
	if !strings.Contains(err.Error(), "already loaded") {
		t.Errorf("Call() = %v, want the reason said plainly", err)
	}
	// The model is told where to reread it rather than left to wonder.
	if !strings.Contains(err.Error(), "earlier in this conversation") {
		t.Errorf("Call() = %v, want the model told where the skill is", err)
	}
}

// Each agent's bookkeeping is its own: two agents reading one skill have each
// read it once.
func TestLoadSkillCountsPerTool(t *testing.T) {
	found := skillsIn(t, "writing")
	first, second := NewLoadSkill(found, nil), NewLoadSkill(found, nil)

	if _, err := load(t, first, "writing"); err != nil {
		t.Fatalf("Call() = %v", err)
	}
	if _, err := load(t, second, "writing"); err != nil {
		t.Errorf("a second agent could not load the skill: %v", err)
	}
}

// Loading one skill says nothing about the others.
func TestLoadSkillTracksEachSkillSeparately(t *testing.T) {
	tool := NewLoadSkill(skillsIn(t, "writing", "testing"), nil)

	if _, err := load(t, tool, "writing"); err != nil {
		t.Fatalf("Call() = %v", err)
	}
	if _, err := load(t, tool, "testing"); err != nil {
		t.Errorf("Call() = %v, want the second skill loaded", err)
	}
}

func TestLoadSkillRefusesAnUnknownName(t *testing.T) {
	tool := NewLoadSkill(skillsIn(t, "writing", "testing"), nil)

	_, err := load(t, tool, "drawing")
	if err == nil {
		t.Fatal("Call() accepted a skill that does not exist")
	}
	// A model one word away from the right name is told what the names are.
	for _, name := range []string{"writing", "testing"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("Call() = %v, want %q among the names offered", err, name)
		}
	}
}

// A path is not a name, and naming one is refused like any other unknown
// skill: where a skill lives is the harness's business.
func TestLoadSkillRefusesAPath(t *testing.T) {
	found := skillsIn(t, "writing")
	tool := NewLoadSkill(found, nil)

	if _, err := load(t, tool, found[0].Path); err == nil {
		t.Error("Call() accepted a path where a name belongs")
	}
	if _, err := load(t, tool, filepath.Join(found[0].Path, "SKILL.md")); err == nil {
		t.Error("Call() accepted a file where a name belongs")
	}
}

func TestLoadSkillRefusesAnEmptyName(t *testing.T) {
	tool := NewLoadSkill(skillsIn(t, "writing"), nil)

	if _, err := load(t, tool, "   "); err == nil {
		t.Error("Call() accepted a call naming no skill")
	}
	if _, err := tool.Call(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("Call() accepted a call with no name at all")
	}
}

func TestLoadSkillRefusesArgumentsThatAreNotJSON(t *testing.T) {
	tool := NewLoadSkill(skillsIn(t, "writing"), nil)

	if _, err := tool.Call(context.Background(), json.RawMessage(`{"name":`)); err == nil {
		t.Error("Call() accepted arguments that are not JSON")
	}
}

// A name is trimmed before it is looked up, since whitespace around it is
// never part of what the model meant.
func TestLoadSkillTrimsTheName(t *testing.T) {
	tool := NewLoadSkill(skillsIn(t, "writing"), nil)

	if _, err := load(t, tool, "  writing\n"); err != nil {
		t.Errorf("Call() = %v, want the name trimmed and found", err)
	}
}

// A skill that could not be read is not counted as loaded, so it can be asked
// for again once whatever was wrong with it is fixed.
func TestLoadSkillCanBeRetriedAfterAFailure(t *testing.T) {
	found := skillsIn(t, "writing")
	tool := NewLoadSkill(found, nil)

	file := filepath.Join(found[0].Path, "SKILL.md")
	kept, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("reading the skill: %v", err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatalf("removing the skill: %v", err)
	}
	if _, err := load(t, tool, "writing"); err == nil {
		t.Fatal("Call() accepted a skill whose file is gone")
	}

	if err := os.WriteFile(file, kept, 0o644); err != nil {
		t.Fatalf("restoring the skill: %v", err)
	}
	if _, err := load(t, tool, "writing"); err != nil {
		t.Errorf("Call() = %v, want the restored skill loaded", err)
	}
}

// The name is the whole of a call, and it is what the user watching needs to
// see the run reach for.
func TestLoadSkillAnnouncesTheName(t *testing.T) {
	tool := NewLoadSkill(skillsIn(t, "writing"), nil)

	if got := tool.Announce(json.RawMessage(`{"name":"writing"}`)); got != "writing" {
		t.Errorf("Announce() = %q, want writing", got)
	}
	// Display must not fail, whatever it is handed.
	if got := tool.Announce(json.RawMessage(`{`)); got != "" {
		t.Errorf("Announce() = %q, want empty for unreadable arguments", got)
	}
}

// What the model is told about the tool must not describe a path, since it has
// none to give and the harness resolves it.
func TestLoadSkillTakesOnlyAName(t *testing.T) {
	def := NewLoadSkill(skillsIn(t, "writing"), nil).Definition()

	if def.Name != LoadSkillTool {
		t.Errorf("Name = %q, want %q", def.Name, LoadSkillTool)
	}
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Parameters has no properties: %v", def.Parameters)
	}
	if len(props) != 1 {
		t.Errorf("the tool takes %d arguments, want only the name", len(props))
	}
	if _, ok := props["name"]; !ok {
		t.Errorf("the tool does not take a name: %v", props)
	}
}

// A skill's text mentions the files beside it by a relative path, so the
// result says which directory those paths are relative to.
func TestLoadSkillNamesTheSkillsDirectory(t *testing.T) {
	found := skillsIn(t, "writing")
	tool := NewLoadSkill(found, nil)

	out, err := load(t, tool, "writing")
	if err != nil {
		t.Fatalf("Call() = %v", err)
	}
	if !strings.Contains(out, skillDirPrefix+found[0].Path+"\n") {
		t.Errorf("Call() = %q, want the skill's own directory named", out)
	}
	// The path has to be one the model can use from anywhere, since nothing
	// says what a run's working directory is.
	if !filepath.IsAbs(found[0].Path) {
		t.Errorf("the skill is at %q, want an absolute path", found[0].Path)
	}
	// It is agi speaking rather than the skill's author, so it is outside the
	// document the model was handed.
	if strings.Contains(instructionsIn(out), skillDirPrefix) {
		t.Errorf("Call() = %q, want the directory said outside the skill", out)
	}
}

// instructionsIn returns what the result wrapped, which is the skill's own
// text and nothing agi said around it.
func instructionsIn(result string) string {
	_, wrapped, _ := strings.Cut(result, "<skill name=")
	_, text, _ := strings.Cut(wrapped, ">\n")
	return text
}

// The user watching sees where the instructions came from, since the skill's
// own text is the whole of the result and says nothing about it.
func TestLoadSkillReportsTheDirectory(t *testing.T) {
	found := skillsIn(t, "writing")
	tool := NewLoadSkill(found, nil)

	out, err := load(t, tool, "writing")
	if err != nil {
		t.Fatalf("Call() = %v", err)
	}
	if !strings.Contains(tool.Report(out), found[0].Path) {
		t.Errorf("Report() = %q, want the skill's directory", tool.Report(out))
	}
	// A result with no such line has nothing to report, and reporting must not
	// fail whatever it is given.
	if line := tool.Report("something else entirely"); line != "" {
		t.Errorf("Report() = %q, want nothing for a result that names no directory", line)
	}
}
