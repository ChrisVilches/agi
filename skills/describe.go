package skills

import (
	"strings"
)

// Describe renders the skills for a model to choose from, and nothing for an
// empty list.
//
// What it produces is a summary and not the skills themselves: one line per
// skill giving the name to load it by and what it is for, and its tags under
// it when it has any. A catalogue that included the instructions would be the
// thing loading a skill is for, and the run would carry every skill's whole
// text whether it needed one or not.
//
// It is wrapped in <available_skills> because a model reads it inside a system
// prompt that is Markdown throughout, and a Markdown heading would dissolve
// into the prose around it. A tag will not: nothing else in the prompt is
// written that way, so where the list begins and ends is never in question.
//
// Each description is collapsed onto one line. A skill's description is prose
// somebody wrote in a YAML file and may arrive folded across several lines,
// and the list is only readable — by a model or by whoever is watching the run
// — while one skill is one entry.
func Describe(found []Skill) string {
	if len(found) == 0 {
		return ""
	}

	entries := make([]string, 0, len(found))
	for _, s := range found {
		entry := "- **" + s.Name + "**: " + oneLine(s.Description)
		if len(s.Tags) > 0 {
			entry += "\n  * Tags: " + strings.Join(s.Tags, ", ")
		}
		entries = append(entries, entry)
	}
	return "<available_skills>\n" + strings.Join(entries, "\n\n") + "\n</available_skills>"
}

// oneLine collapses every run of whitespace to a single space, so that a
// description written across several lines arrives as the one line the list
// gives it.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
