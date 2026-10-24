package diff

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/tools"
)

// How lines taken out are matched with the lines put in that they became.
// Each removed line, in order, takes the first added line after the last pair
// that is similar enough to mark, looking no further than pairLookahead.

// pairsOf matches the lines of the diff of two texts and returns each pair as
// "old => new", in order, so a test can say which lines were matched. It also
// holds every pairing to its shape: a removed row with an added row after it,
// and neither side used twice or out of order.
func pairsOf(t *testing.T, before, after string) []string {
	t.Helper()
	a, b := splitLines(before), splitLines(after)
	ops := collapse(diffLines(a, b))
	var pairs []string
	lastOld, lastNew := -1, -1
	for _, p := range pairLines(ops, a, b) {
		if ops[p.old].kind != opDelete || ops[p.new].kind != opInsert {
			t.Fatalf("pair %d => %d is not a removed row and an added one", p.old, p.new)
		}
		if p.old <= lastOld || p.new <= lastNew {
			t.Fatalf("pair %d => %d crosses or repeats the one before", p.old, p.new)
		}
		lastOld, lastNew = p.old, p.new
		pairs = append(pairs, display(a[ops[p.old].ai])+" => "+display(b[ops[p.new].bi]))
	}
	return pairs
}

func wantPairs(t *testing.T, before, after string, want ...string) {
	t.Helper()
	if got := pairsOf(t, before, after); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("pairs =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A line added above a changed one — a comment, here — is skipped over, and
// the changed line is matched with what it became.
func TestPairingSkipsALineAddedAbove(t *testing.T) {
	wantPairs(t, "start\na = 1\nend\n", "start\n// set a\na = 10\nend\n",
		"a = 1 => a = 10")
}

// A line removed above a changed one has no partner and is left plain, and
// the changed line still finds its own.
func TestPairingSkipsALineRemovedAbove(t *testing.T) {
	wantPairs(t, "start\ndead()\na = 1\nend\n", "start\na = 10\nend\n",
		"a = 1 => a = 10")
}

// Code wrapped in a new block: every line of it is matched with its indented
// self, past the opening line, and the closing line is left plain.
func TestPairingFollowsCodeIntoANewBlock(t *testing.T) {
	wantPairs(t, "start\nfoo()\nbar()\nbaz()\nend\n", "start\nif ok {\n\tfoo()\n\tbar()\n\tbaz()\n}\nend\n",
		"foo() => \tfoo()", "bar() => \tbar()", "baz() => \tbaz()")
}

// The reverse, a block unwrapped: the opening and closing lines go out alone
// and every line inside is matched with its unindented self.
func TestPairingFollowsCodeOutOfABlock(t *testing.T) {
	wantPairs(t, "start\nif ok {\n\tfoo()\n\tbar()\n}\nend\n", "start\nfoo()\nbar()\nend\n",
		"\tfoo() => foo()", "\tbar() => bar()")
}

// Pairs never cross. Two lines that swapped places as well as changed can
// only both be matched by pairing out of order, so the first is matched and
// the second, whose partner is now behind it, is left plain.
func TestPairingKeepsOrder(t *testing.T) {
	wantPairs(t, "x = 1\ny = 2\n", "y = 20\nx = 10\n",
		"x = 1 => x = 10")
}

// Each added line is matched at most once: the second of two similar removed
// lines goes to the second of two similar added lines and not back to the
// first.
func TestPairingUsesAnAddedLineOnce(t *testing.T) {
	wantPairs(t, "f(a)\nf(b)\n", "f(a, 1)\nf(b, 2)\n",
		"f(a) => f(a, 1)", "f(b) => f(b, 2)")
}

// The search reaches pairLookahead lines ahead and no further: a partner
// pairLookahead-1 lines past the start is found, and one pairLookahead lines
// past it is not.
func TestPairingLookaheadIsExact(t *testing.T) {
	partnerAfter := func(skipped int) string {
		var b strings.Builder
		for i := range skipped {
			fmt.Fprintf(&b, "unrelated_%d()\n", i)
		}
		return b.String() + "value = 10\n"
	}
	wantPairs(t, "value = 1\n", partnerAfter(pairLookahead-1), "value = 1 => value = 10")
	wantPairs(t, "value = 1\n", partnerAfter(pairLookahead))
}

// A removed line with no similar added line anywhere takes none, and leaves
// every added line free for the removed lines after it.
func TestPairingAnUnmatchedLineTakesNothing(t *testing.T) {
	wantPairs(t, "gone()\na = 1\nb = 2\n", "a = 10\nb = 20\n",
		"a = 1 => a = 10", "b = 2 => b = 20")
}

// A run the size of a rewritten file: every line changed in its value pairs
// with its own replacement, all of them in order, and a run with nothing in
// common pairs nothing — both found by looking a few lines ahead rather than
// by comparing every line with every other.
func TestPairingALargeRun(t *testing.T) {
	var before, after, unrelated strings.Builder
	for i := range 2000 {
		fmt.Fprintf(&before, "v_%d = %d\n", i, i)
		fmt.Fprintf(&after, "v_%d = %d\n", i, i+1)
		fmt.Fprintf(&unrelated, "other_%d\n", i)
	}
	if got := pairsOf(t, before.String(), after.String()); len(got) != 2000 {
		t.Errorf("%d of 2000 changed lines paired, want all of them", len(got))
	}
	if got := pairsOf(t, before.String(), unrelated.String()); len(got) != 0 {
		t.Errorf("%d lines paired with unrelated ones, want none: %q", len(got), got[:min(3, len(got))])
	}
}

// The pairing reaches the screen: the indented copy is marked where it gained
// its tab, the old line, which lost nothing, has no mark, and the block's own
// lines are plain.
func TestRenderMarksCodeMovedIntoABlock(t *testing.T) {
	emphasized(t)
	got := Render("start\nfoo()\nend\n", "start\nfor {\n\tfoo()\n}\nend\n")
	for _, want := range []string{
		tools.Removed("- foo()"),
		tools.Added("+ for {"),
		tools.Added("+ ") + tools.AddedEmphasis("\t") + tools.Added("foo()"),
		tools.Added("+ }"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render = %q, want it to contain %q", got, want)
		}
	}
}
