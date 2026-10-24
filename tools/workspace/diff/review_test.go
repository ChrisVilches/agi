package diff

import (
	"fmt"
	"strings"
	"testing"
)

// The width of the number column is the widest number shown, not the widest
// number in rows the cap left out.
func TestRenderWidthCountsOnlyShownRows(t *testing.T) {
	plain(t)
	before := numbered(5000)
	got := Render(before, "")
	if !strings.HasPrefix(got, "   1 - line 1\n") {
		t.Errorf("first row = %q, want it padded to the two digits of the rows shown",
			strings.SplitN(got, "\n", 2)[0])
	}
}

// Control characters are drawn the way cat -v draws them rather than sent to
// the terminal, where an escape sequence would restyle or move what follows
// and a backspace would eat the character before it. A tab is left alone: it
// is ordinary content and the terminal draws it as space.
func TestRenderShowsControlCharacters(t *testing.T) {
	plain(t)
	for line, want := range map[string]string{
		"\x1b[31mred\x1b[0m": "^[[31mred^[[0m",
		"back\bspace":        "back^Hspace",
		"bell\a":             "bell^G",
		"nul\x00":            "nul^@",
		"del\x7f":            "del^?",
		"tab\there":          "tab\there",
	} {
		rows := parse(t, Render(line+"\n", "other\n"))
		if len(rows) == 0 || rows[0].text != want {
			t.Errorf("line %q drawn as %q, want %q", line, rows[0].text, want)
		}
	}
}

// A word diff gives up past a bound of its own, much smaller than the line
// diff's: a line that changed in more places than that is not worth marking,
// and the search for the smallest diff of two long lines is the expensive
// part. Giving up declines the marks rather than drawing wrong ones.
func TestEmphasisGivesUpOnALineThatChangedEverywhere(t *testing.T) {
	var old, new strings.Builder
	for i := range maxWordEdits {
		fmt.Fprintf(&old, "a%d b ", i)
		fmt.Fprintf(&new, "c%d b ", i)
	}
	if _, _, ok := emphasis(old.String(), new.String()); ok {
		t.Error("emphasis marked a line with more changed words than it searches for")
	}
	if _, _, ok := emphasis(old.String()+"x", old.String()+"y"); !ok {
		t.Error("emphasis declined a long line that changed in one word")
	}
}

// What the cap leaves out is still counted correctly in the note, now that
// the rows past it are never paired or measured.
func TestRenderCountsWhatTheCapLeavesOut(t *testing.T) {
	plain(t)
	rows := strings.Split(Render(numbered(5000), ""), "\n")
	if want := fmt.Sprintf("… %d more lines", 5000-maxDiffRows); !strings.HasSuffix(rows[len(rows)-1], want) {
		t.Errorf("last row = %q, want it to end %q", rows[len(rows)-1], want)
	}
}

func BenchmarkRenderRewrittenFile(b *testing.B) {
	var before, after strings.Builder
	for i := range 20000 {
		fmt.Fprintf(&before, "\tresult_%d := compute(alpha, beta, %d) // keep\n", i, i)
		fmt.Fprintf(&after, "\tvalue_%d := evaluate(gamma, delta, %d) // new\n", i, i*7)
	}
	for b.Loop() {
		Render(before.String(), after.String())
	}
}

func BenchmarkRenderMinifiedLines(b *testing.B) {
	var before, after strings.Builder
	for range 30 {
		for j := range 3000 {
			fmt.Fprintf(&before, "f%d(x%d);", j, j)
			fmt.Fprintf(&after, "g%d(x%d);", j, j)
		}
		before.WriteString("\n")
		after.WriteString("\n")
	}
	for b.Loop() {
		Render(before.String(), after.String())
	}
}

// When the cap falls on the last line before a gap, the gap goes with what
// was left out: a ⋮ with nothing under it but the count would separate
// nothing.
func TestRenderCapDropsAGapItWouldEndOn(t *testing.T) {
	plain(t)
	var before strings.Builder
	for i := 1; i <= 400; i++ {
		fmt.Fprintf(&before, "line %d\n", i)
	}
	// Changes every 10 lines: each block is a change and three lines either
	// side, with gaps between, so some block ends exactly on the cap.
	after := before.String()
	for i := 5; i <= 400; i += 10 {
		after = strings.Replace(after, fmt.Sprintf("line %d\n", i), fmt.Sprintf("LINE %d\n", i), 1)
	}
	rows := strings.Split(Render(before.String(), after), "\n")
	last := rows[len(rows)-1]
	if !strings.Contains(last, "more line") {
		t.Fatalf("last row = %q, want the count of what was left out", last)
	}
	if strings.TrimSpace(rows[len(rows)-2]) == "⋮" {
		t.Errorf("a gap comes straight before the count:\n%s", strings.Join(rows[len(rows)-4:], "\n"))
	}
}
