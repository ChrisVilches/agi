package skills

import (
	"strings"
	"testing"
)

func TestDescribeRendersTheList(t *testing.T) {
	got := Describe([]Skill{
		{Name: "data-analyzer", Description: "Process CSV files and generate data visualizations.", Tags: []string{"csv", "visualization", "pandas", "analytics"}},
		{Name: "web-search", Description: "Retrieve up-to-date real-time facts from the web.", Tags: []string{"news", "facts", "web", "search"}},
	})

	want := `<available_skills>
- **data-analyzer**: Process CSV files and generate data visualizations.
  * Tags: csv, visualization, pandas, analytics

- **web-search**: Retrieve up-to-date real-time facts from the web.
  * Tags: news, facts, web, search
</available_skills>`
	if got != want {
		t.Errorf("Describe() =\n%s\n\nwant\n%s", got, want)
	}
}

// Tags are optional, and a skill with none gets no line saying so.
func TestDescribeLeavesOutAnEmptyTagsLine(t *testing.T) {
	got := Describe([]Skill{{Name: "writing", Description: "Write prose."}})

	want := "<available_skills>\n- **writing**: Write prose.\n</available_skills>"
	if got != want {
		t.Errorf("Describe() =\n%s\n\nwant\n%s", got, want)
	}
}

// One skill is one entry, whatever its description looks like in the file it
// was written in.
func TestDescribeCollapsesADescriptionOntoOneLine(t *testing.T) {
	got := Describe([]Skill{{Name: "writing", Description: "Write prose\nthat says   what\n\nit means."}})

	if !strings.Contains(got, "- **writing**: Write prose that says what it means.") {
		t.Errorf("Describe() =\n%s", got)
	}
	if lines := strings.Count(got, "\n"); lines != 2 {
		t.Errorf("Describe() spans %d lines, want the entry on one", lines+1)
	}
}

// The order is the order it was given, since that is the guarantee Find makes.
func TestDescribeKeepsTheOrderItWasGiven(t *testing.T) {
	got := Describe([]Skill{
		{Name: "zeta", Description: "Last."},
		{Name: "alpha", Description: "First."},
	})

	if strings.Index(got, "zeta") > strings.Index(got, "alpha") {
		t.Errorf("Describe() reordered the skills:\n%s", got)
	}
}

// Nothing to list is nothing to say, so that a run with no skills has no
// skills section in its prompt rather than an empty one.
func TestDescribeSaysNothingAboutNoSkills(t *testing.T) {
	if got := Describe(nil); got != "" {
		t.Errorf("Describe(nil) = %q, want empty", got)
	}
	if got := Describe([]Skill{}); got != "" {
		t.Errorf("Describe([]) = %q, want empty", got)
	}
}
