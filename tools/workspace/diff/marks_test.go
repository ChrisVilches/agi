package diff

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/tools"
)

// Which words of a run of changed lines are marked. The run is diffed as one
// stream of words on each side, so a line keeps its marks when enough of it
// was found on the other side, and a line put in or taken out whole is plain.

// A line added above a changed one — a comment, here — is plain, and the
// changed line is marked against what it became, though the comment shares
// a word with it.
func TestMarksSkipALineAddedAbove(t *testing.T) {
	got := marksByLine("start\na = 1\nend\n", "start\n// set a\na = 10\nend\n")
	wantMarks(t, got, "- a = 1", "1")
	wantMarks(t, got, "+ // set a")
	wantMarks(t, got, "+ a = 10", "10")
}

// A line removed above a changed one is plain, and the changed line is still
// marked.
func TestMarksSkipALineRemovedAbove(t *testing.T) {
	got := marksByLine("start\ndead()\na = 1\nend\n", "start\na = 10\nend\n")
	wantMarks(t, got, "- dead()")
	wantMarks(t, got, "- a = 1", "1")
	wantMarks(t, got, "+ a = 10", "10")
}

// Code wrapped in a new block: every line of it is marked where it gained its
// indentation, and the block's own lines are plain.
func TestMarksFollowCodeIntoANewBlock(t *testing.T) {
	got := marksByLine("start\nfoo()\nbar()\nend\n", "start\nif ok {\n\tfoo()\n\tbar()\n}\nend\n")
	wantMarks(t, got, "- foo()")
	wantMarks(t, got, "- bar()")
	wantMarks(t, got, "+ if ok {")
	wantMarks(t, got, "+     foo()", "    ")
	wantMarks(t, got, "+     bar()", "    ")
	wantMarks(t, got, "+ }")
}

// The reverse, a block unwrapped.
func TestMarksFollowCodeOutOfABlock(t *testing.T) {
	got := marksByLine("start\nif ok {\n\tfoo()\n\tbar()\n}\nend\n", "start\nfoo()\nbar()\nend\n")
	wantMarks(t, got, "- if ok {")
	wantMarks(t, got, "-     foo()", "    ")
	wantMarks(t, got, "-     bar()", "    ")
	wantMarks(t, got, "- }")
	wantMarks(t, got, "+ foo()")
	wantMarks(t, got, "+ bar()")
}

// Lines put in whole after the ones that grew do not take a bracket from
// them: each line that grew is marked where it grew and nowhere else.
func TestMarksKeepABracketOnItsOwnLine(t *testing.T) {
	got := marksByLine("foo(x)\nbar(y)\n", "foo(x, 1)\nbar(y, 2)\nbaz()\nqux()\n")
	wantMarks(t, got, "+ foo(x, 1)", ", 1")
	wantMarks(t, got, "+ bar(y, 2)", ", 2")
	wantMarks(t, got, "+ baz()")
	wantMarks(t, got, "+ qux()")
}

// Two lines that swapped places as well as changed share only their = with
// the line now beside them, which is too little to mark either.
func TestMarksDoNotFollowLinesThatSwapped(t *testing.T) {
	got := marksByLine("x = 1\ny = 2\n", "y = 20\nx = 10\n")
	for line, marks := range got {
		if len(marks) > 0 {
			t.Errorf("%q marks = %q, want none", line, marks)
		}
	}
}

// A run the size of the screen: every line changed in its value is marked,
// and a run with nothing in common marks nothing.
func TestMarksALargeRun(t *testing.T) {
	var before, after, unrelated strings.Builder
	for i := range maxDiffRows {
		fmt.Fprintf(&before, "v_%d = %d\n", i, i)
		fmt.Fprintf(&after, "v_%d = %d\n", i, i+1)
		fmt.Fprintf(&unrelated, "other_%d\n", i)
	}
	count := func(got map[string][]string) int {
		n := 0
		for _, marks := range got {
			if len(marks) > 0 {
				n++
			}
		}
		return n
	}
	if n := count(marksByLine(before.String(), after.String())); n != 2*maxDiffRows {
		t.Errorf("%d of %d changed lines marked, want all of them", n, 2*maxDiffRows)
	}
	if n := count(marksByLine(before.String(), unrelated.String())); n != 0 {
		t.Errorf("%d lines marked against unrelated ones, want none", n)
	}
}

// The marks reach the screen: the indented copy is marked where it gained
// its tab, the old line, which lost nothing, has no mark, and the block's own
// lines are plain.
func TestRenderMarksCodeMovedIntoABlock(t *testing.T) {
	emphasized(t)
	got := Render("start\nfoo()\nend\n", "start\nfor {\n\tfoo()\n}\nend\n")
	for _, want := range []string{
		tools.Removed("- foo()"),
		tools.Added("+ for {"),
		tools.Added("+ ") + tools.AddedEmphasis("    ") + tools.Added("foo()"),
		tools.Added("+ }"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render = %q, want it to contain %q", got, want)
		}
	}
}
