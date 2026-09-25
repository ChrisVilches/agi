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
// about it checked: the frontmatter is there and is YAML, it gives a name and
// a description, and the name is the name of the folder.
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

	name := strings.TrimSpace(m.Name)
	description := strings.TrimSpace(m.Description)
	folder := filepath.Base(dir)

	switch {
	case name == "":
		return Skill{}, fmt.Errorf("%s: its frontmatter gives no name; a skill is referred to "+
			"by one, and it is the name of the folder the skill is in, %q", where, folder)
	case description == "":
		return Skill{}, fmt.Errorf("%s: its frontmatter gives no description; it is how a "+
			"skill is chosen without reading it", where)
	case name != folder:
		return Skill{}, fmt.Errorf("%s: its frontmatter is named %q, but it is in a folder "+
			"called %q; a skill's name is the name of the folder it is in, so rename one to "+
			"match the other", where, name, folder)
	}

	return Skill{
		Name:        name,
		Description: description,
		Version:     strings.TrimSpace(m.Version),
		Tags:        m.Tags,
		Path:        dir,
	}, nil
}
