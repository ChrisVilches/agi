package diff

import (
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/tools"
	"github.com/fatih/color"
)

// These are the shape of diff patchfile actually draws: old_str and new_str
// span several lines, carry context the model repeats unchanged, and change
// more than one line between them. Each case is written as a patch — a file,
// the text replaced and the text replacing it — so it reads as the call it
// stands for.

// patched applies one replacement the way patchfile does and renders it,
// failing the test if old_str is not in the file exactly once.
func patched(t *testing.T, file, old, new string) string {
	t.Helper()
	if n := strings.Count(file, old); n != 1 {
		t.Fatalf("old_str appears %d times in the file, want once", n)
	}
	after := strings.Replace(file, old, new, 1)

	// The guarantees are checked by reading the rows back, which needs them
	// plain, whatever the test itself is looking at.
	previous := color.NoColor
	color.NoColor = true
	checkRender(t, file, after)
	color.NoColor = previous

	return Render(file, after)
}

func wantRows(t *testing.T, got string, rows ...string) {
	t.Helper()
	if want := strings.Join(rows, "\n"); got != want {
		t.Errorf("Render =\n%s\nwant\n%s", got, want)
	}
}

const goFile = "func main() {\n\ta := 1\n\tb := 2\n\tc := 3\n\tfmt.Println(a, b, c)\n}\n"

// Several lines in a row changed: all of them go out, then all of them come
// in, each under its own number.
func TestMultilineAdjacentLinesChanged(t *testing.T) {
	plain(t)
	got := patched(t, goFile,
		"\ta := 1\n\tb := 2\n\tc := 3\n",
		"\ta := 10\n\tb := 20\n\tc := 30\n")
	wantRows(t, got,
		"  1   func main() {",
		"  2 -     a := 1",
		"  3 -     b := 2",
		"  4 -     c := 3",
		"  2 +     a := 10",
		"  3 +     b := 20",
		"  4 +     c := 30",
		"  5       fmt.Println(a, b, c)",
		"  6   }",
	)
}

// Each of those lines is marked against its replacement, and each one
// marks only its own value.
func TestMultilineAdjacentLinesAreEachMarked(t *testing.T) {
	emphasized(t)
	got := patched(t, goFile,
		"\ta := 1\n\tb := 2\n\tc := 3\n",
		"\ta := 10\n\tb := 20\n\tc := 30\n")
	for _, want := range []string{
		tools.Removed("-     a := ") + tools.RemovedEmphasis("1"),
		tools.Removed("-     b := ") + tools.RemovedEmphasis("2"),
		tools.Removed("-     c := ") + tools.RemovedEmphasis("3"),
		tools.Added("+     a := ") + tools.AddedEmphasis("10"),
		tools.Added("+     b := ") + tools.AddedEmphasis("20"),
		tools.Added("+     c := ") + tools.AddedEmphasis("30"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render = %q, want it to contain %q", got, want)
		}
	}
}

// old_str's first and last lines change and the lines between them do not.
// Close together, they are one block with the middle shown, since leaving out
// two lines would save nothing.
func TestMultilineFirstAndLastLineOfAShortOldStr(t *testing.T) {
	plain(t)
	got := patched(t, numbered(10),
		"line 3\nline 4\nline 5\nline 6\n",
		"LINE 3\nline 4\nline 5\nLINE 6\n")
	wantRows(t, got,
		"  1   line 1",
		"  2   line 2",
		"  3 - line 3",
		"  3 + LINE 3",
		"  4   line 4",
		"  5   line 5",
		"  6 - line 6",
		"  6 + LINE 6",
		"  7   line 7",
		"  8   line 8",
		"  9   line 9",
	)
}

// The same with a long old_str: the unchanged middle is left out under a ⋮,
// with context kept either side of it, which is what the user asked of a
// patch whose context runs to many lines.
func TestMultilineFirstAndLastLineOfALongOldStr(t *testing.T) {
	plain(t)
	file := numbered(20)
	start := strings.Index(file, "line 3\n")
	end := strings.Index(file, "line 15\n")
	old := file[start:end]
	new := strings.Replace(strings.Replace(old, "line 3\n", "changed 3\n", 1), "line 14\n", "changed 14\n", 1)
	got := patched(t, file, old, new)
	wantRows(t, got,
		"   1   line 1",
		"   2   line 2",
		"   3 - line 3",
		"   3 + changed 3",
		"   4   line 4",
		"   5   line 5",
		"   6   line 6",
		"   ⋮",
		"  11   line 11",
		"  12   line 12",
		"  13   line 13",
		"  14 - line 14",
		"  14 + changed 14",
		"  15   line 15",
		"  16   line 16",
		"  17   line 17",
	)
}

// One old_str removing a line, changing another and adding a third, with
// unchanged lines between. A removal shifts every new number after it, so
// the numbers on the two sides part company and each still names its line.
func TestMultilineRemoveChangeAndInsertInOnePatch(t *testing.T) {
	plain(t)
	file := numbered(12)
	start := strings.Index(file, "line 2\n")
	end := strings.Index(file, "line 11\n")
	old := file[start:end]
	new := strings.Replace(old, "line 3\n", "", 1)
	new = strings.Replace(new, "line 6\n", "line six\n", 1)
	new = strings.Replace(new, "line 9\n", "line 9\nnew line\n", 1)
	got := patched(t, file, old, new)
	wantRows(t, got,
		"   1   line 1",
		"   2   line 2",
		"   3 - line 3",
		"   3   line 4",
		"   4   line 5",
		"   6 - line 6",
		"   5 + line six",
		"   6   line 7",
		"   7   line 8",
		"   8   line 9",
		"   9 + new line",
		"  10   line 10",
		"  11   line 11",
		"  12   line 12",
	)
}

// In that patch only the changed line is marked; the line removed alone
// and the line added alone are drawn plain.
func TestMultilineOnlyPairedLinesAreMarked(t *testing.T) {
	emphasized(t)
	file := numbered(12)
	got := patched(t, file, "line 3\nline 4\nline 5\nline 6\n", "line 4\nline 5\nline six\nnew line\n")
	for _, want := range []string{
		tools.Removed("- line 3"),
		tools.Removed("- line ") + tools.RemovedEmphasis("6"),
		tools.Added("+ line ") + tools.AddedEmphasis("six"),
		tools.Added("+ new line"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render = %q, want it to contain %q", got, want)
		}
	}
}

// Two lines become four: the first two are marked where they grew, and
// the two lines put in whole are plain.
func TestMultilineMoreLinesInThanOut(t *testing.T) {
	plain(t)
	file := "a\nfoo(x)\nbar(y)\nz\n"
	old, new := "foo(x)\nbar(y)\n", "foo(x, 1)\nbar(y, 2)\nbaz()\nqux()\n"
	wantRows(t, patched(t, file, old, new),
		"  1   a",
		"  2 - foo(x)",
		"  3 - bar(y)",
		"  2 + foo(x, 1)",
		"  3 + bar(y, 2)",
		"  4 + baz()",
		"  5 + qux()",
		"  6   z",
	)

	emphasized(t)
	got := patched(t, file, old, new)
	for _, want := range []string{
		tools.Added("+ foo(x") + tools.AddedEmphasis(", 1") + tools.Added(")"),
		tools.Added("+ bar(y") + tools.AddedEmphasis(", 2") + tools.Added(")"),
		tools.Added("+ baz()"),
		tools.Added("+ qux()"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render = %q, want it to contain %q", got, want)
		}
	}
}

// Four lines become two, the other way round.
func TestMultilineMoreLinesOutThanIn(t *testing.T) {
	emphasized(t)
	got := patched(t, "a\nfoo(x, 1)\nbar(y, 2)\nbaz()\nqux()\nz\n",
		"foo(x, 1)\nbar(y, 2)\nbaz()\nqux()\n", "foo(x)\nbar(y)\n")
	for _, want := range []string{
		tools.Removed("- foo(x") + tools.RemovedEmphasis(", 1") + tools.Removed(")"),
		tools.Removed("- bar(y") + tools.RemovedEmphasis(", 2") + tools.Removed(")"),
		tools.Removed("- baz()"),
		tools.Removed("- qux()"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render = %q, want it to contain %q", got, want)
		}
	}
}

// Two lines swapped is one line moved: one goes out and comes back on the
// other side of its neighbour, and the neighbour is kept.
func TestMultilineSwappedLines(t *testing.T) {
	plain(t)
	rows := parse(t, patched(t, "a\nb\nc\nd\n", "b\nc\n", "c\nb\n"))
	var dels, ins, keeps int
	for _, r := range rows {
		switch r.kind {
		case '-':
			dels++
		case '+':
			ins++
		case ' ':
			keeps++
		}
	}
	if dels != 1 || ins != 1 || keeps != 3 {
		t.Errorf("swap drawn with %d -, %d + and %d unchanged rows, want 1, 1 and 3", dels, ins, keeps)
	}
}

// A whole block re-indented, as when code is wrapped in a new if: every line
// changes, each only in its indentation, and the lines added around it are
// plain.
func TestMultilineReindentedBlock(t *testing.T) {
	emphasized(t)
	file := "start\nfoo()\nbar()\nend\n"
	got := patched(t, file, "foo()\nbar()\n", "if ok {\n\tfoo()\n\tbar()\n}\n")
	for _, want := range []string{
		tools.Added("+ if ok {"),
		tools.Added("+ ") + tools.AddedEmphasis("    ") + tools.Added("foo()"),
		tools.Added("+ ") + tools.AddedEmphasis("    ") + tools.Added("bar()"),
		tools.Added("+ }"),
	} {
		if strings.Contains(got, want) {
			continue
		}
		t.Errorf("Render = %q, want it to contain %q", got, want)
	}
}
