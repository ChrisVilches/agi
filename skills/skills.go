// Package skills finds the skills kept in a folder.
//
// A skill is a folder holding a SKILL.md, and the file is what makes it one:
// nothing is declared anywhere, so a skill is added by writing it and removed
// by deleting it. Every file beside SKILL.md, and every folder under it, is
// that skill's own — its resources — and nothing there may be a skill of its
// own. That is the whole of the shape:
//
//	skills/
//	  writing/
//	    SKILL.md          the skill "writing"
//	    examples/         its resources, however deeply nested
//	  go/
//	    testing/
//	      SKILL.md        the skill "testing"
//
// A folder that is not a skill is only a place to keep them, and there may be
// any number of them on the way down, which is why "go/testing" above is found
// at all. The search stops at the first SKILL.md on each path, and anything
// below one is the skill's, not another skill's.
//
// SKILL.md begins with a YAML frontmatter block between --- lines. It must
// give a name and a description; it may also give a version and tags, and
// anything else it gives is left alone, since the file is written for whoever
// reads skills and not for agi alone.
//
// A skill therefore has two names, and they do different jobs. Its id is the
// name of the folder it is in, and that is what a skill is asked for by: it is
// short, it is on disk where anybody can see it, and no two skills may share
// one. Its name is what the frontmatter says, and it is a title written to be
// read — "SSH Usage and Efficient Commands" for the skill whose id is
// ssh-usage. Nothing is required of a title: two skills may carry the same
// one, and one that only repeats the folder's name costs nothing either.
//
// The frontmatter used to have to repeat the folder's name exactly, which made
// the two one thing and left the title no job to do. What made a matching name
// worth having was that a skill is referred to by one — and that is the id's
// work, which cannot drift from where the skill is, because it is where the
// skill is.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileName is what makes a folder a skill. It is a fixed name rather than
// something the user points at: a folder of skills is named on the command
// line, and the folders inside it announce themselves.
const FileName = "SKILL.md"

// A Skill is one folder's SKILL.md, read and checked.
//
// ID is the name of the folder the SKILL.md is in, and it is the skill's
// identity: it is what loading one asks for, what a subagent's task names, and
// what no two skills may share. Name is the title the frontmatter gave, which
// is prose for a person to read and is held to nothing.
//
// Path is where the skill is — the absolute path of the folder holding the
// SKILL.md, not of the file — because everything a skill carries besides its
// instructions is beside it, and the folder is what a reader needs to find it.
// It is not a second answer to the id: the id is the last segment of it.
type Skill struct {
	ID          string
	Name        string
	Description string
	Version     string
	Tags        []string
	Path        string
}

// Find reads every folder given and returns the skills under them, in the
// order the folders were named and, within a folder, the order the filesystem
// sorts them in. Reading one folder is calling it with one.
//
// The two things that can be wrong are checked in that order, and for a
// reason: a folder is read on its own terms first — every SKILL.md it holds is
// found and parsed — and only once all of them have been read is the one
// question that spans folders asked, which is whether two skills have the same
// id. Collisions reported before the
// folders were known to be sound would be reported against skills that might
// not be skills at all.
//
// The first thing found wrong ends the search and is the error returned. There
// is no partial result: a caller wanting the skills it can get is asking a
// different question, and would have to be told which ones were missing.
//
// The order is a guarantee and not an accident. Every skill under the first
// folder comes before every skill under the second, whatever either contains,
// and within a folder the order is the one the filesystem sorts names in, all
// the way down. It is a guarantee because the order reaches the model: this is
// the order the skills are listed to it in, and a list that came out
// differently from one run to the next would change the prompt without
// anything about the run having changed.
func Find(dirs []string) ([]Skill, error) {
	var found []Skill
	for _, dir := range dirs {
		inDir, err := find(dir)
		if err != nil {
			return nil, err
		}
		found = append(found, inDir...)
	}

	// Which folder each id was found in, so a collision can name both.
	where := make(map[string]string, len(found))
	for _, s := range found {
		if first, taken := where[s.ID]; taken {
			return nil, collision(s.ID, first, s.Path)
		}
		where[s.ID] = s.Path
	}
	return found, nil
}

// find reads one folder.
//
// The folder has to be there. A name matching nothing is a mistake — a typo, a
// path from another machine, a value the shell mangled — and returning no
// skills for it would answer as though the folder had been empty, which is a
// different thing and a thing the user would act on differently.
//
// The path is trimmed first, because it arrived as a shell argument and space
// around a path is never part of the path: left alone, a leading space turns
// an absolute path into a relative one.
func find(dir string) ([]Skill, error) {
	root, err := filepath.Abs(strings.TrimSpace(dir))
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", dir, err)
	}

	info, err := os.Stat(root)
	switch {
	case err != nil:
		return nil, fmt.Errorf("reading %s: %w", root, err)
	case !info.IsDir():
		return nil, fmt.Errorf("%s is not a directory; skills are kept in one", root)
	}

	var found []Skill
	if err := walk(root, &found); err != nil {
		return nil, err
	}
	return found, nil
}

// walk descends until it reaches a skill, and stops there.
//
// A folder holding a SKILL.md is a skill and nothing under it is looked
// through again for one — refuseNested does that, and says so when it finds
// one. A folder that holds no SKILL.md is only somewhere skills are kept, and
// every folder inside it is descended into.
//
// A symbolic link is not descended into, since ReadDir reports one as a link
// rather than a directory. That keeps the search finite whatever the folder
// contains, and it keeps a skill from being smuggled in from outside the
// folder the user named.
func walk(dir string, found *[]Skill) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading %s: %w", dir, err)
	}

	if holdsSkill(entries) {
		s, err := read(dir)
		if err != nil {
			return err
		}
		if err := refuseNested(dir); err != nil {
			return err
		}
		*found = append(*found, s)
		return nil
	}

	for _, e := range entries {
		if e.IsDir() {
			if err := walk(filepath.Join(dir, e.Name()), found); err != nil {
				return err
			}
		}
	}
	return nil
}

// refuseNested reports the first SKILL.md anywhere under a skill.
//
// It is an error rather than something ignored because the two readings of it
// are both wrong. Read as a skill, it is one whose name cannot be the name of
// the folder the user would look for it in; read as a resource, it is a file
// that says it is a skill and is treated as prose. Which the author meant is
// not agi's to guess, and the fix — move it out, or rename it — is theirs.
func refuseNested(skill string) error {
	entries, err := os.ReadDir(skill)
	if err != nil {
		return fmt.Errorf("reading %s: %w", skill, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		under := filepath.Join(skill, e.Name())
		inside, err := os.ReadDir(under)
		if err != nil {
			return fmt.Errorf("reading %s: %w", under, err)
		}
		if holdsSkill(inside) {
			return fmt.Errorf("%s is a skill, and there is another %s in %s; a folder under a "+
				"skill holds that skill's resources and cannot be a skill of its own",
				skill, FileName, under)
		}
		if err := refuseNested(under); err != nil {
			return err
		}
	}
	return nil
}

// holdsSkill reports whether a folder's entries include a SKILL.md.
//
// A directory of that name is not one: the name has to be a file to be read.
// Anything else is, a symbolic link to a file included, since keeping one
// SKILL.md in two places is the author's business and reading it follows the
// link either way.
func holdsSkill(entries []os.DirEntry) bool {
	for _, e := range entries {
		if e.Name() == FileName && !e.IsDir() {
			return true
		}
	}
	return false
}

// collision phrases the one thing the user has to fix themselves. Which of two
// skills with the same id should win is not something agi can decide quietly,
// so it says where both are and stops.
//
// Only the id is checked. Two skills may carry the same title, because a title
// is prose and prose repeats; what could not be told apart is two folders of
// one name.
func collision(id, first, second string) error {
	if first == second {
		return fmt.Errorf("the skill %q in %s was found twice; name the folder once", id, first)
	}
	return fmt.Errorf("two skills are in a folder called %q, one in %s and one in %s; rename "+
		"one of the folders, or leave out one of the folders they were found under, since a "+
		"skill is loaded by the name of the folder it is in and these could not be told apart",
		id, first, second)
}
