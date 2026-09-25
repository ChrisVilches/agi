package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// skill writes a SKILL.md at the given path under dir, creating whatever
// folders it needs, and returns the folder the file ended up in.
func skill(t *testing.T, dir, path, content string) string {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("making the skill's folder: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", full, err)
	}
	return filepath.Dir(full)
}

// matter is the smallest frontmatter that passes, for a skill of a given name.
func frontmatterFor(name string) string {
	return "---\nname: " + name + "\ndescription: What " + name + " is for.\n---\n\nThe skill.\n"
}

func TestFindReadsASkill(t *testing.T) {
	root := t.TempDir()
	at := skill(t, root, "writing/SKILL.md", `---
name: writing
description: Write prose that says what it means.
version: 1.2.0
tags: [prose, editing]
---

Instructions the model reads.
`)

	found, err := Find([]string{root})
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("Find() returned %d skills, want 1", len(found))
	}

	s := found[0]
	if s.Name != "writing" {
		t.Errorf("Name = %q, want writing", s.Name)
	}
	if s.Description != "Write prose that says what it means." {
		t.Errorf("Description = %q", s.Description)
	}
	if s.Version != "1.2.0" {
		t.Errorf("Version = %q, want 1.2.0", s.Version)
	}
	if strings.Join(s.Tags, ",") != "prose,editing" {
		t.Errorf("Tags = %v, want [prose editing]", s.Tags)
	}
	// The path is the folder the SKILL.md is in, not the file, and it is
	// absolute so that nothing about it depends on where agi was run from.
	if !filepath.IsAbs(s.Path) {
		t.Errorf("Path = %q, want an absolute path", s.Path)
	}
	if s.Path != at {
		t.Errorf("Path = %q, want %q", s.Path, at)
	}
}

// A skill may be any number of folders down, since the folders on the way are
// only places skills are kept.
func TestFindDescendsUntilItReachesASkill(t *testing.T) {
	root := t.TempDir()
	skill(t, root, "go/testing/SKILL.md", frontmatterFor("testing"))
	skill(t, root, "prose/long/form/writing/SKILL.md", frontmatterFor("writing"))

	found, err := Find([]string{root})
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if got := names(found); got != "testing,writing" {
		t.Errorf("Find() found %s, want testing,writing", got)
	}
}

// The search stops at a skill: the folders under it are resources, and a
// resource that happens to be a directory is not looked into for a skill.
func TestFindStopsAtASkill(t *testing.T) {
	root := t.TempDir()
	skill(t, root, "writing/SKILL.md", frontmatterFor("writing"))
	if err := os.MkdirAll(filepath.Join(root, "writing", "examples", "deeply", "nested"), 0o755); err != nil {
		t.Fatalf("making the resources: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "writing", "examples", "notes.md"), []byte("notes"), 0o644); err != nil {
		t.Fatalf("writing a resource: %v", err)
	}

	found, err := Find([]string{root})
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("Find() returned %d skills, want 1", len(found))
	}
}

// The folder named on the command line may itself be a skill.
func TestFindAcceptsASkillAtTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "writing")
	skill(t, root, "SKILL.md", frontmatterFor("writing"))

	found, err := Find([]string{root})
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if len(found) != 1 || found[0].Name != "writing" {
		t.Fatalf("Find() found %v, want the skill writing", names(found))
	}
}

// A folder with nothing in it is not a mistake; it is a folder with no skills
// in it yet, which is a different thing from a folder that is not there.
func TestFindAcceptsAFolderWithNoSkills(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatalf("making a folder: %v", err)
	}

	found, err := Find([]string{root})
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("Find() returned %d skills, want none", len(found))
	}
}

func TestFindReadsEveryFolderGiven(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	skill(t, first, "writing/SKILL.md", frontmatterFor("writing"))
	skill(t, second, "testing/SKILL.md", frontmatterFor("testing"))

	found, err := Find([]string{first, second})
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	// The folders are read in the order they were given, and never sorted.
	if got := names(found); got != "writing,testing" {
		t.Errorf("Find() found %s, want writing,testing", got)
	}
}

func TestFindRefusesAFolderThatIsNotThere(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nowhere")

	_, err := Find([]string{missing})
	if err == nil {
		t.Fatal("Find() accepted a folder that is not there")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("Find() = %v, want the folder named", err)
	}
}

func TestFindRefusesAFileWhereAFolderWasNamed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills")
	if err := os.WriteFile(path, []byte("not a folder"), 0o644); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	_, err := Find([]string{path})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("Find() = %v, want a refusal naming what is wrong", err)
	}
}

// Space around a path arrived from a shell and is never part of the path.
func TestFindTrimsTheFolderItIsGiven(t *testing.T) {
	root := t.TempDir()
	skill(t, root, "writing/SKILL.md", frontmatterFor("writing"))

	found, err := Find([]string{"  " + root + "  "})
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("Find() returned %d skills, want 1", len(found))
	}
}

func TestFindRefusesANestedSkill(t *testing.T) {
	root := t.TempDir()
	skill(t, root, "writing/SKILL.md", frontmatterFor("writing"))
	nested := skill(t, root, "writing/examples/deeper/SKILL.md", frontmatterFor("deeper"))

	_, err := Find([]string{root})
	if err == nil {
		t.Fatal("Find() accepted a skill inside a skill")
	}
	if !strings.Contains(err.Error(), nested) {
		t.Errorf("Find() = %v, want the nested %s named", err, FileName)
	}
}

// The nested file is refused whatever it says, including when its own name
// matches the folder it is in: a folder under a skill is not a skill.
func TestFindRefusesANestedSkillThatWouldOtherwiseBeValid(t *testing.T) {
	root := t.TempDir()
	skill(t, root, "writing/SKILL.md", frontmatterFor("writing"))
	skill(t, root, "writing/editing/SKILL.md", frontmatterFor("editing"))

	if _, err := Find([]string{root}); err == nil {
		t.Fatal("Find() accepted a skill inside a skill")
	}
}

func TestFindRefusesTwoSkillsOfTheSameName(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	one := skill(t, first, "writing/SKILL.md", frontmatterFor("writing"))
	two := skill(t, second, "writing/SKILL.md", frontmatterFor("writing"))

	_, err := Find([]string{first, second})
	if err == nil {
		t.Fatal("Find() accepted two skills of the same name")
	}
	// Which one should win is the user's to decide, so both are named.
	if !strings.Contains(err.Error(), one) || !strings.Contains(err.Error(), two) {
		t.Errorf("Find() = %v, want both folders named", err)
	}
}

// A collision is only looked for once every folder has been read, so a broken
// skill is reported as broken rather than as a name clash.
func TestFindReportsAMalformedSkillBeforeACollision(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	skill(t, first, "writing/SKILL.md", frontmatterFor("writing"))
	skill(t, second, "writing/SKILL.md", "---\nname: writing\n---\n")

	_, err := Find([]string{first, second})
	if err == nil {
		t.Fatal("Find() accepted a skill with no description")
	}
	if !strings.Contains(err.Error(), "description") {
		t.Errorf("Find() = %v, want the missing description reported", err)
	}
}

func TestFindRefusesTheSameFolderTwice(t *testing.T) {
	root := t.TempDir()
	skill(t, root, "writing/SKILL.md", frontmatterFor("writing"))

	_, err := Find([]string{root, root})
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("Find() = %v, want the folder reported as named twice", err)
	}
}

// A symbolic link to a folder is not descended into, so the search is finite
// whatever the folder holds and a skill cannot be brought in from outside it.
func TestFindDoesNotFollowALinkedFolder(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	skill(t, elsewhere, "writing/SKILL.md", frontmatterFor("writing"))
	if err := os.Symlink(elsewhere, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symbolic links are not available here: %v", err)
	}

	found, err := Find([]string{root})
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("Find() followed a linked folder and found %s", names(found))
	}
}

// A directory called SKILL.md is not a skill: the name has to be a file.
func TestFindIgnoresADirectoryNamedLikeTheFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "writing", FileName), 0o755); err != nil {
		t.Fatalf("making the directory: %v", err)
	}

	found, err := Find([]string{root})
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("Find() returned %d skills, want none", len(found))
	}
}

func TestFindReturnsNothingForNoFolders(t *testing.T) {
	found, err := Find(nil)
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("Find() returned %d skills, want none", len(found))
	}
}

// names renders what was found, for a message about it.
func names(found []Skill) string {
	var got []string
	for _, s := range found {
		got = append(got, s.Name)
	}
	return strings.Join(got, ",")
}
