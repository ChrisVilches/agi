package skills

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/frontmatter"
	"gopkg.in/yaml.v2"
)

// yamlOnly is the one frontmatter format a SKILL.md may be written in: the
// fields between an opening --- line and a closing one.
//
// The library offers TOML and JSON as well, and they are deliberately left
// out. A SKILL.md is a file people write for whatever reads skills, and every
// one of them writes YAML; accepting two more spellings would mean two more
// ways for a file to be read differently here than it is everywhere else — and
// the one field whose YAML has a shape of its own, tags, would quietly stop
// working in the other two.
var yamlOnly = frontmatter.NewFormat("---", "---", yaml.Unmarshal)

// matter is what a SKILL.md's frontmatter is read into.
//
// A field it does not name is left alone rather than refused, which is the
// opposite of what a user-defined tool's manifest does with an unknown field
// and is the right way round for this. A manifest is written for agi, so a
// field agi does not know is a typo. A SKILL.md is written for whoever reads
// skills, so a field agi does not know is a field somebody else reads.
type matter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Version     string `yaml:"version"`
	Tags        tags   `yaml:"tags"`
}

// tags is a list of tags that may have been written as one.
//
// "tags: draft" and "tags: [draft]" mean the same thing to anybody writing the
// file, and YAML gives no reason to think otherwise, so both are read as a
// list. Anything that is neither is refused rather than ignored: a mapping
// under tags is a file whose author meant something this does not support.
type tags []string

// UnmarshalYAML reads either spelling. The signature is the one yaml.v2 asks
// for: unmarshal is tried against each shape in turn, and the first that fits
// is what was written.
func (t *tags) UnmarshalYAML(unmarshal func(any) error) error {
	var one string
	if err := unmarshal(&one); err == nil {
		*t = clean([]string{one})
		return nil
	}

	var many []string
	if err := unmarshal(&many); err != nil {
		return errors.New("tags must be a string or a list of strings")
	}
	*t = clean(many)
	return nil
}

// clean trims each tag and drops the ones left empty. A tag is a word used to
// find a skill by, so space around it is never part of it, and a blank one is
// nothing to find anything by.
func clean(raw []string) []string {
	var kept []string
	for _, tag := range raw {
		if trimmed := strings.TrimSpace(tag); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return kept
}

// read turns the SKILL.md in dir into the skill it declares, with everything
// about it checked: the frontmatter is there and is YAML, and it gives a name
// and a description.
//
// The name it gives is the skill's title and is not checked against anything.
// The skill's id — what it is loaded by — is the folder's own name, which is
// why nothing here has to agree with anything: a title is written to be read,
// and holding it to the folder's spelling would make the two one thing again.
//
// Only the frontmatter is read. What follows it is the skill itself — the
// instructions somebody wrote — and this package's business is finding skills
// and saying where they are, not reading what they say.
func read(dir string) (Skill, error) {
	where := filepath.Join(dir, FileName)
	raw, err := os.ReadFile(where)
	if err != nil {
		return Skill{}, fmt.Errorf("reading %s: %w", where, err)
	}

	var m matter
	if _, err := frontmatter.MustParse(bytes.NewReader(raw), &m, yamlOnly); err != nil {
		if errors.Is(err, frontmatter.ErrNotFound) {
			return Skill{}, fmt.Errorf("%s has no frontmatter; it begins with a --- line, the "+
				"fields describing the skill, and a closing --- line", where)
		}
		return Skill{}, fmt.Errorf("%s: its frontmatter is not valid YAML: %w", where, err)
	}

	// The title is collapsed onto one line here rather than wherever it is
	// shown. A description is prose and may well have been written across
	// several lines on purpose, so it is stored as it was written and
	// collapsed by whatever renders it; a title is a name, and a name with a
	// line break in it is not one — every reader of it, the catalogue and the
	// line the user watches alike, wants the same one line.
	name := oneLine(m.Name)
	description := strings.TrimSpace(m.Description)
	folder := filepath.Base(dir)

	switch {
	case name == "":
		return Skill{}, fmt.Errorf("%s: its frontmatter gives no name; it is the skill's "+
			"title, written to be read, and it is what a person and a model see beside the "+
			"id this skill is loaded by, %q", where, folder)
	case description == "":
		return Skill{}, fmt.Errorf("%s: its frontmatter gives no description; it is how a "+
			"skill is chosen without reading it", where)
	}

	return Skill{
		ID:          folder,
		Name:        name,
		Description: description,
		Version:     strings.TrimSpace(m.Version),
		Tags:        m.Tags,
		Path:        dir,
	}, nil
}

// Instructions returns what the skill's SKILL.md says: everything after the
// frontmatter, trimmed.
//
// The frontmatter is left out because it has already been read. Its name and
// description are how the skill was chosen in the first place, and handing
// them back with the instructions would tell whoever asked for the skill the
// two things they used to ask for it.
//
// The file is read now rather than kept from when it was found. A skill is a
// document on the user's disk, and the one that matters is the one that is
// there when it is asked for; keeping a copy would mean a skill edited during
// a long run was loaded as it used to be.
//
// A file with nothing after its frontmatter fails rather than returning
// nothing, since a skill that says nothing cannot be loaded in any useful
// sense, and silence is the one answer that would look like success.
func (s Skill) Instructions() (string, error) {
	where := filepath.Join(s.Path, FileName)
	raw, err := os.ReadFile(where)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", where, err)
	}

	var m matter
	rest, err := frontmatter.MustParse(bytes.NewReader(raw), &m, yamlOnly)
	if err != nil {
		return "", fmt.Errorf("reading the frontmatter of %s: %w", where, err)
	}

	text := strings.TrimSpace(string(rest))
	if text == "" {
		return "", fmt.Errorf("%s says nothing after its frontmatter; a skill is the "+
			"instructions in that file, and there are none to load", where)
	}
	return text, nil
}
