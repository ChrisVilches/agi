package diff

import (
	"strings"
	"testing"
)

// A paragraph reworded and wrapped again moves words from one line to the
// next. Only the words that really changed are marked: a word that moved
// across a line break is not, and a line whose words were spread over two
// others is still marked rather than left plain.

// reflowBefore and reflowAfter are a paragraph of Markdown wrapped at 80
// columns, before and after it was reworded.
const reflowBefore = `## Culture and heritage

Luxembourg’s culture reflects its position between the Germanic and
Romance-speaking worlds, as well as its diverse population. Its historic
capital, with its fortifications and old quarters, is a **UNESCO World Heritage
Site**. Museums in Luxembourg City cover national history, art, and modern
culture; the National Museum of Military History in Diekirch is particularly
associated with the Battle of the Bulge.

Luxembourg’s cuisine blends French and German traditions.
`

const reflowAfter = `## Culture and heritage

Luxembourg's culture draws on both the Germanic and Romance-speaking worlds,
alongside influences from its diverse population. The capital's historic
fortifications and old quarters are a **UNESCO World Heritage Site**. Museums
in Luxembourg City explore national history, art, and modern culture, while the
National Museum of Military History in Diekirch is closely linked to the
Battle of the Bulge.

Luxembourg’s cuisine blends French and German traditions.
`

// marksByLine returns, for every changed line of the diff of two texts, the
// text of each stretch marked on it, keyed by "-" or "+" and the line.
func marksByLine(before, after string) map[string][]string {
	a, b := splitLines(before), splitLines(after)
	ops := collapse(diffLines(a, b))
	out := map[string][]string{}
	for i, m := range wordMarks(ops, a, b) {
		o := ops[i]
		switch {
		case o == nil || o.kind == opKeep:
		case o.kind == opDelete:
			out["- "+display(a[o.ai])] = marked(display(a[o.ai]), m)
		default:
			out["+ "+display(b[o.bi])] = marked(display(b[o.bi]), m)
		}
	}
	return out
}

func wantMarks(t *testing.T, got map[string][]string, line string, want ...string) {
	t.Helper()
	marks, ok := got[line]
	if !ok {
		t.Fatalf("no changed line %q", line)
	}
	if strings.Join(marks, "|") != strings.Join(want, "|") {
		t.Errorf("%q marks = %q, want %q", line, marks, want)
	}
}

func TestReflowedParagraphMarksOnlyWhatChanged(t *testing.T) {
	got := marksByLine(reflowBefore, reflowAfter)

	// Romance-speaking worlds moved up a line and is not marked on either.
	wantMarks(t, got, "+ Luxembourg's culture draws on both the Germanic and Romance-speaking worlds,",
		"'", "draws on both")
	wantMarks(t, got, "- Luxembourg’s culture reflects its position between the Germanic and",
		"’", "reflects its position between")
	// "Its historic capital" became "The capital's historic", so one of the
	// two words has to be marked as moved; git keeps historic and this keeps
	// capital, and both are as short a diff as there is.
	wantMarks(t, got, "- Romance-speaking worlds, as well as its diverse population. Its historic",
		"as well as", "Its historic")
	wantMarks(t, got, "+ alongside influences from its diverse population. The capital's historic",
		"alongside influences from", "The", "'s historic")

	// The end of the paragraph moved down a line whole, and is plain where
	// it landed; only the words taken out around it are marked.
	wantMarks(t, got, "- associated with the Battle of the Bulge.", "associated with")
	wantMarks(t, got, "+ Battle of the Bulge.")
	wantMarks(t, got, "+ National Museum of Military History in Diekirch is closely linked to the",
		"closely linked to")
}

// A line break and a single space are the same thing to a paragraph, so a
// paragraph wrapped differently and otherwise unchanged has nothing to mark.
func TestRewrappingAloneMarksNothing(t *testing.T) {
	got := marksByLine("one two three\nfour five\n", "one two\nthree four five\n")
	for line, marks := range got {
		if len(marks) > 0 {
			t.Errorf("%q marks = %q, want none", line, marks)
		}
	}
}
