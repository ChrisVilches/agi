package skills

import (
	"strings"
	"testing"
)

func TestDescribeRendersTheList(t *testing.T) {
	got := Describe([]Skill{
		{ID: "data-analyzer", Name: "Data Analyzer", Description: "Process CSV files and generate data visualizations.", Tags: []string{"csv", "visualization", "pandas", "analytics"}},
		{ID: "web-search", Name: "Web Search", Description: "Retrieve up-to-date real-time facts from the web.", Tags: []string{"news", "facts", "web", "search"}},
	})

	want := `<available_skills>
- **data-analyzer** "Data Analyzer": Process CSV files and generate data visualizations.
  * Tags: csv, visualization, pandas, analytics

- **web-search** "Web Search": Retrieve up-to-date real-time facts from the web.
  * Tags: news, facts, web, search
</available_skills>`
	if got != want {
		t.Errorf("Describe() =\n%s\n\nwant\n%s", got, want)
	}
}

// The id is what a skill is loaded by, and the title is prose beside it, so the
// entry has to show which is which.
func TestDescribeShowsTheIDAndTheTitleApart(t *testing.T) {
	got := Describe([]Skill{{
		ID:          "ssh-usage",
		Name:        "SSH Usage and Efficient Commands",
		Description: "Work over SSH without repeating yourself.",
	}})

	want := `<available_skills>
- **ssh-usage** "SSH Usage and Efficient Commands": Work over SSH without repeating yourself.
</available_skills>`
	if got != want {
		t.Errorf("Describe() =\n%s\n\nwant\n%s", got, want)
	}
}

// A title that is the id over again says nothing twice, and an entry showing
// both would have the model comparing two spellings of one word.
func TestDescribeLeavesOutATitleThatIsTheID(t *testing.T) {
	got := Describe([]Skill{{ID: "writing", Name: "writing", Description: "Write prose."}})

	want := "<available_skills>\n- **writing**: Write prose.\n</available_skills>"
	if got != want {
		t.Errorf("Describe() =\n%s\n\nwant\n%s", got, want)
	}
}

// Tags are optional, and a skill with none gets no line saying so.
func TestDescribeLeavesOutAnEmptyTagsLine(t *testing.T) {
	got := Describe([]Skill{{ID: "writing", Name: "Writing", Description: "Write prose."}})

	want := "<available_skills>\n- **writing** \"Writing\": Write prose.\n</available_skills>"
	if got != want {
		t.Errorf("Describe() =\n%s\n\nwant\n%s", got, want)
	}
}

// One skill is one entry, whatever its description looks like in the file it
// was written in.
func TestDescribeCollapsesADescriptionOntoOneLine(t *testing.T) {
	got := Describe([]Skill{{ID: "writing", Name: "writing", Description: "Write prose\nthat says   what\n\nit means."}})

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
		{ID: "zeta", Name: "Zeta", Description: "Last."},
		{ID: "alpha", Name: "Alpha", Description: "First."},
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
