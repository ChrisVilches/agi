package diff

import (
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/tools"
)

// Patches that only add lines or only take them away, with nothing changed.
// They are their own edge: there is no pair to mark, a run of them can be
// several lines long, and where they sit — the start of the file, its end, a
// file with no final newline — is where the numbering and the context run
// out. Each case is a patch, applied with patched from multiline_test.go.

// countRows reports how many rows of each kind a plain rendering has.
func countRows(t *testing.T, rendered string) (dels, ins, keeps int) {
	t.Helper()
	for _, r := range parse(t, rendered) {
		switch r.kind {
		case '-':
			dels++
		case '+':
			ins++
		case ' ':
			keeps++
		}
	}
	return dels, ins, keeps
}

// One line added between two others: one + row, and every line after it
// numbered one further on than it was.
func TestInsertOneLine(t *testing.T) {
	plain(t)
	got := patched(t, numbered(10), "line 5\nline 6\n", "line 5\ninserted\nline 6\n")
	wantRows(t, got,
		"  3   line 3",
		"  4   line 4",
		"  5   line 5",
		"  6 + inserted",
		"  7   line 6",
		"  8   line 7",
		"  9   line 8",
	)
}

// Two lines added together: two + rows in a row, the numbers after them
// moved on by two, which is where the column grows a digit.
func TestInsertTwoLines(t *testing.T) {
	plain(t)
	got := patched(t, numbered(10), "line 5\nline 6\n", "line 5\ninserted 1\ninserted 2\nline 6\n")
	wantRows(t, got,
		"   3   line 3",
		"   4   line 4",
		"   5   line 5",
		"   6 + inserted 1",
		"   7 + inserted 2",
		"   8   line 6",
		"   9   line 7",
		"  10   line 8",
	)
}

// Two lines added in two places in one patch, close enough to share their
// context: one block, with the line between them shown once.
func TestInsertTwoLinesInTwoPlaces(t *testing.T) {
	plain(t)
	got := patched(t, numbered(10), "line 4\nline 5\nline 6\n", "line 4\nA\nline 5\nB\nline 6\n")
	wantRows(t, got,
		"   2   line 2",
		"   3   line 3",
		"   4   line 4",
		"   5 + A",
		"   6   line 5",
		"   7 + B",
		"   8   line 6",
		"   9   line 7",
		"  10   line 8",
	)
}

// Two lines added far apart in one long old_str: two blocks, with the lines
// between them left out.
func TestInsertTwoLinesFarApart(t *testing.T) {
	plain(t)
	file := numbered(30)
	start, end := strings.Index(file, "line 2\n"), strings.Index(file, "line 26\n")
	old := file[start:end]
	new := strings.Replace(strings.Replace(old, "line 3\n", "line 3\nA\n", 1), "line 24\n", "line 24\nB\n", 1)
	got := patched(t, file, old, new)
	if !strings.Contains(got, "⋮") {
		t.Errorf("Render =\n%s\nwant two blocks", got)
	}
	if dels, ins, _ := countRows(t, got); dels != 0 || ins != 2 {
		t.Errorf("Render has %d - and %d + rows, want 0 and 2:\n%s", dels, ins, got)
	}
}

// A line added before the first: there is no context above it to show, and
// the line that was first is now second.
func TestInsertAtTheStartOfTheFile(t *testing.T) {
	plain(t)
	got := patched(t, numbered(10), "line 1\n", "new first\nline 1\n")
	wantRows(t, got,
		"  1 + new first",
		"  2   line 1",
		"  3   line 2",
		"  4   line 3",
	)
}

// A line added after the last, in a file ending with a newline: there is
// nothing below it to show.
func TestInsertAtTheEndOfTheFile(t *testing.T) {
	plain(t)
	got := patched(t, numbered(10), "line 10\n", "line 10\nline 11\n")
	wantRows(t, got,
		"   8   line 8",
		"   9   line 9",
		"  10   line 10",
		"  11 + line 11",
	)
}

// A line added after the last in a file with no final newline is not purely
// an addition: the old last line gains the newline it lacked, so it goes out
// and comes back, and each side says which has none — git draws it the same
// way.
func TestInsertAfterALastLineWithNoNewline(t *testing.T) {
	plain(t)
	got := patched(t, "a\nb", "b", "b\nc")
	wantRows(t, got,
		"  1   a",
		"  2 - b",
		"      (no newline at end of file)",
		"  2 + b",
		"  3 + c",
		"      (no newline at end of file)",
	)
}

// Blank lines added are rows like any other, with nothing after the marker.
func TestInsertBlankLines(t *testing.T) {
	plain(t)
	got := patched(t, numbered(6), "line 3\nline 4\n", "line 3\n\n\nline 4\n")
	wantRows(t, got,
		"  1   line 1",
		"  2   line 2",
		"  3   line 3",
		"  4 + ",
		"  5 + ",
		"  6   line 4",
		"  7   line 5",
		"  8   line 6",
	)
}

// A line added beside a line identical to it could be drawn as either one
// being new; whichever is chosen, exactly one + row is drawn and nothing is
// shown as taken away.
func TestInsertADuplicateOfANeighbour(t *testing.T) {
	plain(t)
	for _, c := range []struct{ file, old, new string }{
		{"if x {\n\ty()\n}\n}\n", "\ty()\n}\n}\n", "\ty()\n}\n}\n}\n"},
		{"a\n\nb\n", "a\n\nb", "a\n\n\nb"},
	} {
		got := patched(t, c.file, c.old, c.new)
		if dels, ins, _ := countRows(t, got); dels != 0 || ins != 1 {
			t.Errorf("Render has %d - and %d + rows, want 0 and 1:\n%s", dels, ins, got)
		}
	}
}

// One line taken out: one - row under its old number, and every line after
// it numbered one fewer.
func TestDeleteOneLine(t *testing.T) {
	plain(t)
	got := patched(t, numbered(10), "line 5\nline 6\n", "line 6\n")
	wantRows(t, got,
		"  2   line 2",
		"  3   line 3",
		"  4   line 4",
		"  5 - line 5",
		"  5   line 6",
		"  6   line 7",
		"  7   line 8",
	)
}

// Two lines taken out together, each under its own old number.
func TestDeleteTwoLines(t *testing.T) {
	plain(t)
	got := patched(t, numbered(10), "line 5\nline 6\nline 7\n", "line 7\n")
	wantRows(t, got,
		"  2   line 2",
		"  3   line 3",
		"  4   line 4",
		"  5 - line 5",
		"  6 - line 6",
		"  5   line 7",
		"  6   line 8",
		"  7   line 9",
	)
}

// Two lines taken out with one kept between them: the numbers on the two
// sides drift a line further apart after each.
func TestDeleteTwoLinesInTwoPlaces(t *testing.T) {
	plain(t)
	got := patched(t, numbered(10), "line 4\nline 5\nline 6\n", "line 5\n")
	wantRows(t, got,
		"  1   line 1",
		"  2   line 2",
		"  3   line 3",
		"  4 - line 4",
		"  4   line 5",
		"  6 - line 6",
		"  5   line 7",
		"  6   line 8",
		"  7   line 9",
	)
}

// Two lines taken out far apart in one long old_str: two blocks.
func TestDeleteTwoLinesFarApart(t *testing.T) {
	plain(t)
	file := numbered(30)
	start, end := strings.Index(file, "line 2\n"), strings.Index(file, "line 26\n")
	old := file[start:end]
	new := strings.Replace(strings.Replace(old, "line 3\n", "", 1), "line 24\n", "", 1)
	got := patched(t, file, old, new)
	if !strings.Contains(got, "⋮") {
		t.Errorf("Render =\n%s\nwant two blocks", got)
	}
	if dels, ins, _ := countRows(t, got); dels != 2 || ins != 0 {
		t.Errorf("Render has %d - and %d + rows, want 2 and 0:\n%s", dels, ins, got)
	}
}

// The first line taken out: nothing above to show, and the second line is
// now first.
func TestDeleteTheFirstLine(t *testing.T) {
	plain(t)
	got := patched(t, numbered(10), "line 1\nline 2\n", "line 2\n")
	wantRows(t, got,
		"  1 - line 1",
		"  1   line 2",
		"  2   line 3",
		"  3   line 4",
	)
}

// The last line taken out of a file that ends with a newline: nothing below
// to show.
func TestDeleteTheLastLine(t *testing.T) {
	plain(t)
	got := patched(t, numbered(10), "line 9\nline 10\n", "line 9\n")
	wantRows(t, got,
		"   7   line 7",
		"   8   line 8",
		"   9   line 9",
		"  10 - line 10",
	)
}

// The last line taken out of a file with no final newline, by taking the
// newline before it along: the line before is left last and without one, so
// it goes out and comes back as well, the mirror of adding after such a line.
func TestDeleteALastLineWithNoNewline(t *testing.T) {
	plain(t)
	got := patched(t, "a\nb\nc", "\nc", "")
	wantRows(t, got,
		"  1   a",
		"  2 - b",
		"  3 - c",
		"      (no newline at end of file)",
		"  2 + b",
		"      (no newline at end of file)",
	)
}

// Blank lines taken out are rows like any other.
func TestDeleteBlankLines(t *testing.T) {
	plain(t)
	got := patched(t, "a\n\n\n\nb\n", "a\n\n\n\nb", "a\n\nb")
	if dels, ins, _ := countRows(t, got); dels != 2 || ins != 0 {
		t.Errorf("Render has %d - and %d + rows, want 2 and 0:\n%s", dels, ins, got)
	}
}

// Every line of a file taken out but one.
func TestDeleteAllButOneLine(t *testing.T) {
	plain(t)
	got := patched(t, "a\nb\nc\n", "a\nb\nc\n", "b\n")
	wantRows(t, got,
		"  1 - a",
		"  1   b",
		"  3 - c",
	)
}

// Nothing to pair means nothing to mark: a line added next to a near copy of
// itself, and a line taken out next to one, are drawn whole in their color,
// because the near copy is unchanged context and not the line's partner.
func TestPureInsertionsAndDeletionsAreNotMarked(t *testing.T) {
	emphasized(t)
	got := patched(t, "x = 1\nend\n", "x = 1\n", "x = 1\nx = 10\n")
	if want := tools.Added("+ x = 10"); !strings.Contains(got, want) {
		t.Errorf("Render = %q, want the added line whole: %q", got, want)
	}
	got = patched(t, "x = 1\nx = 10\nend\n", "x = 1\nx = 10\n", "x = 1\n")
	if want := tools.Removed("- x = 10"); !strings.Contains(got, want) {
		t.Errorf("Render = %q, want the removed line whole: %q", got, want)
	}
}
