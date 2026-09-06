package skills

import (
	"strings"
)

// Describe renders the skills for a model to choose from, and nothing for an
// empty list.
//
// What it produces is a summary and not the skills themselves: one line per
// skill giving the id to load it by, the title it was given, and what it is
// for, with its tags under it when it has any. A catalogue that included the
// instructions would be the thing loading a skill is for, and the run would
// carry every skill's whole text whether it needed one or not.
//
// The id comes first and is the only thing on the line in bold, because it is
// the only part of the entry a call may be made with; the title follows in
// quotation marks, which is how a line of prose says it is naming something
// rather than saying it. A title that is the id over again is left out
// entirely — an entry reading `writing` ("writing") would have the model
// comparing two spellings of one word to find out whether they differ.
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
		entry := "- **" + s.ID + "**"
		if s.Name != "" && s.Name != s.ID {
			entry += ` "` + s.Name + `"`
		}
		entry += ": " + oneLine(s.Description)
		if len(s.Tags) > 0 {
			entry += "\n  * Tags: " + strings.Join(s.Tags, ", ")
		}
		entries = append(entries, entry)
	}
	return "<available_skills>\n" + strings.Join(entries, "\n\n") + "\n</available_skills>"
}

// oneLine collapses every run of whitespace to a single space, and trims what
// is left, so that something written across several lines in a YAML file
// arrives as the one line whatever shows it gives it.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
