package skills

import (
	"strings"
	"testing"
)

// one writes a single skill folder of the given name and reads it back, which
// is the shortest way to a Skill in a test about what its frontmatter said.
func one(t *testing.T, folder, content string) (Skill, error) {
	t.Helper()
	root := t.TempDir()
	skill(t, root, folder+"/"+FileName, content)

	found, err := Find([]string{root})
	if err != nil {
		return Skill{}, err
	}
	if len(found) != 1 {
		t.Fatalf("Find() returned %d skills, want 1", len(found))
	}
	return found[0], nil
}

func TestTheOptionalFieldsMayBeLeftOut(t *testing.T) {
	s, err := one(t, "writing", frontmatterFor("writing"))
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if s.Version != "" {
		t.Errorf("Version = %q, want empty", s.Version)
	}
	if len(s.Tags) != 0 {
		t.Errorf("Tags = %v, want none", s.Tags)
	}
}

// "tags: draft" and "tags: [draft]" mean the same thing to whoever wrote the
// file, so one tag written as a string is read as a list of one.
func TestTagsMayBeWrittenAsOneString(t *testing.T) {
	s, err := one(t, "writing", `---
name: writing
description: Write prose.
tags: draft
---
`)
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if len(s.Tags) != 1 || s.Tags[0] != "draft" {
		t.Errorf("Tags = %v, want [draft]", s.Tags)
	}
}

func TestTagsMayBeWrittenAsABlockList(t *testing.T) {
	s, err := one(t, "writing", `---
name: writing
description: Write prose.
tags:
  - prose
  - editing
---
`)
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if strings.Join(s.Tags, ",") != "prose,editing" {
		t.Errorf("Tags = %v, want [prose editing]", s.Tags)
	}
}

// A tag is a word a skill is found by, so space around it is not part of it
// and a blank one is nothing to find anything by.
func TestTagsAreTrimmedAndBlanksDropped(t *testing.T) {
	s, err := one(t, "writing", `---
name: writing
description: Write prose.
tags: ["  prose  ", "", "   ", editing]
---
`)
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if strings.Join(s.Tags, ",") != "prose,editing" {
		t.Errorf("Tags = %v, want [prose editing]", s.Tags)
	}
}

func TestTagsRefuseAnythingElse(t *testing.T) {
	_, err := one(t, "writing", `---
name: writing
description: Write prose.
tags:
  prose: yes
---
`)
	if err == nil {
		t.Fatal("Find() accepted a mapping under tags")
	}
	if !strings.Contains(err.Error(), "tags") {
		t.Errorf("Find() = %v, want the field named", err)
	}
}

// A SKILL.md is written for whoever reads skills, not for agi alone, so a
// field agi does not know is a field somebody else reads rather than a typo.
func TestAFieldThatIsNotKnownIsLeftAlone(t *testing.T) {
	s, err := one(t, "writing", `---
name: writing
description: Write prose.
license: MIT
allowed-tools: [bash]
---
`)
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if s.Name != "writing" {
		t.Errorf("Name = %q, want writing", s.Name)
	}
}

// The name in the frontmatter is the skill's title and is held to nothing: it
// is written to be read, and the id the skill is loaded by is the folder.
func TestTheNameNeedNotMatchTheFolder(t *testing.T) {
	s, err := one(t, "ssh-usage", "---\nname: SSH Usage and Efficient Commands\ndescription: Work over SSH.\n---\n")
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if s.ID != "ssh-usage" {
		t.Errorf("ID = %q, want ssh-usage", s.ID)
	}
	if s.Name != "SSH Usage and Efficient Commands" {
		t.Errorf("Name = %q, want the title the frontmatter gave", s.Name)
	}
}

// A title differing from the folder only in case is a title like any other.
func TestTheNameMayDifferFromTheFolderInCase(t *testing.T) {
	s, err := one(t, "writing", frontmatterFor("Writing"))
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if s.ID != "writing" || s.Name != "Writing" {
		t.Errorf("ID = %q, Name = %q, want writing and Writing", s.ID, s.Name)
	}
}

// A title is a name, and a name with a line break in it is not one, so it
// arrives as the one line everything that shows it wants.
func TestTheNameIsCollapsedOntoOneLine(t *testing.T) {
	s, err := one(t, "writing", "---\nname: \"  Writing\\n  Prose  \"\ndescription: Write prose.\n---\n")
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if s.Name != "Writing Prose" {
		t.Errorf("Name = %q, want Writing Prose", s.Name)
	}
}

func TestTheNameIsRequired(t *testing.T) {
	_, err := one(t, "writing", "---\ndescription: Write prose.\n---\n")
	if err == nil || !strings.Contains(err.Error(), "no name") {
		t.Errorf("Find() = %v, want the missing name reported", err)
	}
}

func TestTheDescriptionIsRequired(t *testing.T) {
	_, err := one(t, "writing", "---\nname: writing\n---\n")
	if err == nil || !strings.Contains(err.Error(), "no description") {
		t.Errorf("Find() = %v, want the missing description reported", err)
	}
}

// A field that is there and blank is a field that is not there.
func TestABlankFieldIsNoField(t *testing.T) {
	_, err := one(t, "writing", "---\nname: writing\ndescription: \"   \"\n---\n")
	if err == nil || !strings.Contains(err.Error(), "no description") {
		t.Errorf("Find() = %v, want the blank description reported", err)
	}
}

func TestFrontmatterIsRequired(t *testing.T) {
	_, err := one(t, "writing", "# Writing\n\nInstructions with nothing in front of them.\n")
	if err == nil || !strings.Contains(err.Error(), "no frontmatter") {
		t.Errorf("Find() = %v, want the missing frontmatter reported", err)
	}
}

// An opening --- with no closing one is a frontmatter nobody can read, and it
// is reported the same way, since the message says what the shape is.
func TestFrontmatterMustBeClosed(t *testing.T) {
	_, err := one(t, "writing", "---\nname: writing\ndescription: Write prose.\n\nInstructions.\n")
	if err == nil || !strings.Contains(err.Error(), "frontmatter") {
		t.Errorf("Find() = %v, want the unterminated frontmatter reported", err)
	}
}

func TestMalformedYAMLIsRefused(t *testing.T) {
	_, err := one(t, "writing", "---\nname: writing\n  description: [unclosed\n---\n")
	if err == nil || !strings.Contains(err.Error(), "YAML") {
		t.Errorf("Find() = %v, want the malformed YAML reported", err)
	}
}

// A field of the wrong type is a mistake in the file rather than something to
// coerce: "name: [writing]" is not a name.
func TestAFieldOfTheWrongTypeIsRefused(t *testing.T) {
	if _, err := one(t, "writing", "---\nname: [writing]\ndescription: Write prose.\n---\n"); err == nil {
		t.Error("Find() accepted a list where the name should be")
	}
}

// Only the frontmatter is read. What follows it is the skill itself, and this
// package's business is finding skills rather than reading what they say.
func TestWhatFollowsTheFrontmatterIsNotRead(t *testing.T) {
	s, err := one(t, "writing", `---
name: writing
description: Write prose.
---

---
name: something else
---

Prose that looks like frontmatter and is not.
`)
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if s.Description != "Write prose." {
		t.Errorf("Description = %q, want the frontmatter's", s.Description)
	}
}
