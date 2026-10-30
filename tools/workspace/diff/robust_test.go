package diff

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// The tests in this file hold the package to rules that must be true of every
// diff, whatever the two texts are, and walk the edges of each limit it has.
// diff_test.go has the examples; these are the guarantees.

// row is one line of a rendered diff, read back out of it with color off.
type row struct {
	kind   byte // '-', '+', ' ', or 0 for a row that is not a line
	number int
	text   string
}

var rowPattern = regexp.MustCompile(`^ +(\d+) ([-+ ]) (.*)$`)

// parse reads a rendered diff back into rows. Rows that are not a line of
// either text — a gap, a note about a missing newline, the count of rows left
// out — come back with a kind of 0 and their text whole.
func parse(t *testing.T, rendered string) []row {
	t.Helper()
	if rendered == "" {
		return nil
	}
	var rows []row
	for _, line := range strings.Split(rendered, "\n") {
		m := rowPattern.FindStringSubmatch(line)
		if m == nil {
			rows = append(rows, row{text: strings.TrimSpace(line)})
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("row %q has a number that does not parse", line)
		}
		rows = append(rows, row{kind: m[2][0], number: n, text: m[3]})
	}
	return rows
}

// lcs is the length of a longest common subsequence of a and b, by the
// textbook table. It is the slow, obviously correct answer myers is checked
// against: a shortest diff keeps exactly that many lines.
func lcs(a, b []string) int {
	table := make([][]int, len(a)+1)
	for i := range table {
		table[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else {
				table[i][j] = max(table[i+1][j], table[i][j+1])
			}
		}
	}
	return table[0][0]
}

// checkMyers holds one result of myers to everything it promises: the ops
// rebuild both sides, the indexes count up through each side without a gap,
// and the number of lines kept is the most any diff could keep.
func checkMyers(t *testing.T, a, b []string) {
	t.Helper()
	ops, ok := myers(a, b, maxEditDistance)
	if !ok {
		// No diff between them can need more edits than they have lines, so
		// giving up is only allowed when that is past the limit.
		if len(a)+len(b) <= maxEditDistance {
			t.Fatalf("myers(%q, %q) gave up within its limit", a, b)
		}
		return
	}
	old, new := apply(ops, a, b)
	if strings.Join(old, "\x00") != strings.Join(a, "\x00") || strings.Join(new, "\x00") != strings.Join(b, "\x00") {
		t.Fatalf("myers(%q, %q) does not rebuild them", a, b)
	}
	ai, bi, kept := 0, 0, 0
	for _, o := range ops {
		switch o.kind {
		case opKeep:
			if o.ai != ai || o.bi != bi {
				t.Fatalf("myers(%q, %q): keep at (%d, %d), want (%d, %d)", a, b, o.ai, o.bi, ai, bi)
			}
			ai, bi, kept = ai+1, bi+1, kept+1
		case opDelete:
			if o.ai != ai {
				t.Fatalf("myers(%q, %q): delete at %d, want %d", a, b, o.ai, ai)
			}
			ai++
		case opInsert:
			if o.bi != bi {
				t.Fatalf("myers(%q, %q): insert at %d, want %d", a, b, o.bi, bi)
			}
			bi++
		}
	}
	if len(a)+len(b) <= maxEditDistance {
		if want := lcs(a, b); kept != want {
			t.Fatalf("myers(%q, %q) keeps %d lines, want %d: it is not a shortest diff", a, b, kept, want)
		}
	}
}

// checkRender holds one rendering to everything a reader relies on. Every row
// that is a line says what that line of its text really is, under the right
// number; the numbers on each side only go up; nothing a terminal would act
// on is printed; and when nothing was cut off, every changed line is there.
func checkRender(t *testing.T, before, after string) {
	t.Helper()
	got := Render(before, after)
	if before == after {
		if got != "" {
			t.Fatalf("Render of two equal texts = %q, want nothing", got)
		}
		return
	}
	if strings.Contains(got, "\r") {
		t.Fatalf("Render(%q, %q) prints a carriage return: %q", before, after, got)
	}

	a, b := splitLines(before), splitLines(after)
	rows := parse(t, got)
	lastOld, lastNew := 0, 0
	dels, ins, cut := 0, 0, false
	for _, r := range rows {
		switch r.kind {
		case '-':
			if r.number < 1 || r.number > len(a) || r.text != display(a[r.number-1]) {
				t.Fatalf("Render(%q, %q): row - %d %q is not line %d of before", before, after, r.number, r.text, r.number)
			}
			if r.number <= lastOld {
				t.Fatalf("Render(%q, %q): old line %d after old line %d", before, after, r.number, lastOld)
			}
			lastOld = r.number
			dels++
		case '+', ' ':
			if r.number < 1 || r.number > len(b) || r.text != display(b[r.number-1]) {
				t.Fatalf("Render(%q, %q): row %c %d %q is not line %d of after", before, after, r.kind, r.number, r.text, r.number)
			}
			if r.number <= lastNew {
				t.Fatalf("Render(%q, %q): new line %d after new line %d", before, after, r.number, lastNew)
			}
			lastNew = r.number
			if r.kind == '+' {
				ins++
			}
		default:
			if strings.HasPrefix(r.text, "…") {
				cut = true
			}
		}
	}
	if cut {
		return
	}
	var wantDels, wantIns int
	for _, o := range diffLines(a, b) {
		switch o.kind {
		case opDelete:
			wantDels++
		case opInsert:
			wantIns++
		}
	}
	if dels != wantDels || ins != wantIns {
		t.Fatalf("Render(%q, %q) shows %d - and %d + rows, want %d and %d:\n%s",
			before, after, dels, ins, wantDels, wantIns, got)
	}
	if dels+ins == 0 {
		t.Fatalf("Render(%q, %q) shows no change between two different texts:\n%s", before, after, got)
	}
}

// checkEmphasis holds the marks on one pair of lines to what paint needs of
// them: inside the line, in order, not touching, and never cutting a
// character of valid text in half.
func checkEmphasis(t *testing.T, old, new string) {
	t.Helper()
	oldSpans, newSpans, ok := emphasis(old, new)
	if !ok {
		if oldSpans != nil || newSpans != nil {
			t.Fatalf("emphasis(%q, %q) declined but returned marks", old, new)
		}
		return
	}
	for _, side := range []struct {
		line  string
		spans []span
	}{{old, oldSpans}, {new, newSpans}} {
		end := -1
		for _, s := range side.spans {
			if s.from >= s.to || s.from < 0 || s.to > len(side.line) {
				t.Fatalf("emphasis(%q, %q): mark %v is not inside %q", old, new, s, side.line)
			}
			if s.from <= end {
				t.Fatalf("emphasis(%q, %q): mark %v overlaps or touches the one before", old, new, s)
			}
			end = s.to
			if utf8.ValidString(side.line) && !(utf8.ValidString(side.line[:s.from]) && utf8.ValidString(side.line[s.from:s.to])) {
				t.Fatalf("emphasis(%q, %q): mark %v cuts a character in half", old, new, s)
			}
		}
	}
	plain(t)
	if got := paint(identity, identity, "- ", old, oldSpans); got != "- "+old {
		t.Fatalf("paint with color off = %q, want the line unchanged", got)
	}
	// What is left unmarked is the words the two lines share, so with the
	// marks cut out and space ignored the two sides read the same. Space is
	// ignored because a mark may take in the space between two changed words.
	if o, n := unmarked(old, oldSpans), unmarked(new, newSpans); o != n {
		t.Fatalf("emphasis(%q, %q) leaves %q unmarked on one side and %q on the other", old, new, o, n)
	}
}

// unmarked is a line with its marks cut out and its space removed.
func unmarked(line string, spans []span) string {
	var b strings.Builder
	at := 0
	for _, s := range spans {
		b.WriteString(line[at:s.from])
		at = s.to
	}
	b.WriteString(line[at:])
	return strings.Join(strings.Fields(b.String()), "")
}

func identity(s string) string { return s }

func FuzzMyers(f *testing.F) {
	for _, seed := range [][2]string{
		{"", ""}, {"a", ""}, {"", "a"}, {"a\nb\nc", "a\nc"}, {"x\ny\nx\ny", "y\nx\ny\nx"},
		{"}\n}\n}", "}\n\n}"}, {"a\nb\na\nb\na", "b\na\nb"},
	} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		checkMyers(t, strings.Split(a, "\n"), strings.Split(b, "\n"))
	})
}

func FuzzRender(f *testing.F) {
	for _, seed := range [][2]string{
		{"", "x"}, {"x", ""}, {"a\nb", "a\nb\n"}, {"a\r\nb\r\n", "a\r\nc\r\n"},
		{numbered(30), strings.Replace(numbered(30), "line 15\n", "", 1)},
		{"\xff\xfe\n", "\xff\n"}, {"tab\there\n", "tab\tthere\n"},
		{"モーニング娘。\n", "モーニング娘。'26\n"}, {"a\rb\n", "a\rc\n"},
	} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, before, after string) {
		plain(t)
		checkRender(t, before, after)
	})
}

func FuzzEmphasis(f *testing.F) {
	for _, seed := range [][2]string{
		{"timeout: 30", "timeout: 120"}, {"f(a, b)", "g(a, c)"}, {"", "x"}, {"größe", "grösse"},
		{"\xff a", "\xfe a"}, {"所属する日本の", "在籍する日本を"}, {" 0", "!0!"},
	} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, old, new string) {
		checkEmphasis(t, old, new)
		if strings.Join(tokenize(old), "") != old {
			t.Fatalf("tokenize(%q) does not join back into the line", old)
		}
	})
}

// Equal texts are no change, and draw as nothing at all.
func TestRenderOfEqualTextsIsEmpty(t *testing.T) {
	for _, s := range []string{"", "a", "a\n", numbered(50)} {
		if got := Render(s, s); got != "" {
			t.Errorf("Render(%q, %q) = %q, want nothing", s, s, got)
		}
	}
}

// Everything added and everything taken away are the two extremes, and the
// numbers count from 1 on the side that has lines.
func TestRenderFromAndToNothing(t *testing.T) {
	plain(t)
	if got, want := Render("", "a\nb\n"), "  1 + a\n  2 + b"; got != want {
		t.Errorf("from nothing =\n%s\nwant\n%s", got, want)
	}
	if got, want := Render("a\nb\n", ""), "  1 - a\n  2 - b"; got != want {
		t.Errorf("to nothing =\n%s\nwant\n%s", got, want)
	}
	if got, want := Render("", "x"), "  1 + x\n      (no newline at end of file)"; got != want {
		t.Errorf("one line with no newline =\n%s\nwant\n%s", got, want)
	}
}

// The newline at the end of a text can go as well as come, and a text made
// of one line is the shortest one with an end to change.
func TestRenderRemovingTheFinalNewline(t *testing.T) {
	plain(t)
	want := "  1 - a\n  1 + a\n      (no newline at end of file)"
	if got := Render("a\n", "a"); got != want {
		t.Errorf("Render =\n%s\nwant\n%s", got, want)
	}
}

// Two changes are drawn as one block while the lines between them are no
// more than both blocks' context put together, and split with a ⋮ from the
// first line more.
func TestRenderJoinsAndSplitsAtExactlyTwiceTheContext(t *testing.T) {
	plain(t)
	change := func(between int) string {
		before := numbered(between + 2)
		after := strings.Replace(before, "line 1\n", "first\n", 1)
		last := fmt.Sprintf("line %d\n", between+2)
		return Render(before, strings.Replace(after, last, "last\n", 1))
	}
	if got := change(2 * diffContext); strings.Contains(got, "⋮") {
		t.Errorf("%d lines apart =\n%s\nwant one block", 2*diffContext, got)
	}
	got := change(2*diffContext + 1)
	if !strings.Contains(got, "⋮") {
		t.Errorf("%d lines apart =\n%s\nwant two blocks", 2*diffContext+1, got)
	}
	if rows := parse(t, got); len(rows) != 2+2*diffContext+1+2 {
		t.Errorf("%d lines apart has %d rows, want the two changes, %d of context and a gap:\n%s",
			2*diffContext+1, len(rows), 2*diffContext, got)
	}
}

// The cap is a number of lines: exactly that many is drawn whole, and one
// more draws the cap and says one line was left out, in the singular.
func TestRenderCapIsExact(t *testing.T) {
	plain(t)
	whole := Render("", numbered(maxDiffRows))
	if rows := parse(t, whole); len(rows) != maxDiffRows || strings.Contains(whole, "more") {
		t.Errorf("exactly %d lines = %d rows, want all of them and no note", maxDiffRows, len(rows))
	}
	over := strings.Split(Render("", numbered(maxDiffRows+1)), "\n")
	if len(over) != maxDiffRows+1 || !strings.HasSuffix(over[maxDiffRows], "… 1 more line") {
		t.Errorf("%d lines ends %q, want the cap and a note of 1 more line", maxDiffRows+1, over[len(over)-1])
	}
}

// Numbers are padded to the widest one shown, including when that is in a
// later block than the first.
func TestRenderNumbersGrowAWidthAcrossBlocks(t *testing.T) {
	plain(t)
	before := numbered(120)
	after := strings.Replace(strings.Replace(before, "line 2\n", "two\n", 1), "line 110\n", "hundred ten\n", 1)
	for _, r := range strings.Split(Render(before, after), "\n") {
		if m := rowPattern.FindStringSubmatch(r); m != nil && !strings.HasPrefix(r, fmt.Sprintf("  %3s", m[1])) {
			t.Errorf("row %q is not padded to three digits", r)
		}
	}
}

// A line's own content is printed as it is — wide characters and emoji are
// not changed — save that carriage returns are taken away and a tab becomes
// the spaces up to its stop.
func TestRenderKeepsTheTextOfALine(t *testing.T) {
	plain(t)
	cases := map[string]string{
		"\tindented\twith tabs": "    indented    with tabs",
		"日本語の行 🎌":               "日本語の行 🎌",
		"windows\r":             "windows",
	}
	for line, want := range cases {
		rows := parse(t, Render(line+"\n", "changed\n"))
		if len(rows) == 0 || rows[0].text != want {
			t.Errorf("line %q is drawn as %q, want %q", line, rows[0].text, want)
		}
	}
}

// A carriage return in the middle of a line would send the cursor back over
// the line number, so it is drawn where a reader can see it rather than
// acted on.
func TestRenderShowsACarriageReturnInsideALine(t *testing.T) {
	plain(t)
	got := Render("a\rb\n", "c\n")
	if strings.Contains(got, "\r") || !strings.Contains(got, "a^Mb") {
		t.Errorf("Render = %q, want the return shown as ^M", got)
	}
}

// A line changed beside lines put in or taken out whole is marked, and the
// whole lines stay plain, whichever side has more.
func TestMarksWithUnevenSides(t *testing.T) {
	ops := collapse(diffLines(
		[]string{"a = 1\n", "b = 2\n", "c = 3\n"},
		[]string{"a = 10\n"}))
	marks := wordMarks(ops, []string{"a = 1\n", "b = 2\n", "c = 3\n"}, []string{"a = 10\n"})
	var marked int
	for _, m := range marks {
		if m != nil {
			marked++
		}
	}
	if marked != 2 {
		t.Errorf("three out and one in marked %d rows, want the one pair", marked)
	}

	ops = collapse(diffLines([]string{"a = 1\n"}, []string{"a = 10\n", "b = 2\n", "c = 3\n"}))
	marks = wordMarks(ops, []string{"a = 1\n"}, []string{"a = 10\n", "b = 2\n", "c = 3\n"})
	marked = 0
	for _, m := range marks {
		if m != nil {
			marked++
		}
	}
	if marked != 2 {
		t.Errorf("one out and three in marked %d rows, want the one pair", marked)
	}
}

// A similarity of exactly one half is enough to be marked, and less is not.
func TestEmphasisCutoffIsAtExactlyHalf(t *testing.T) {
	if _, _, ok := emphasis("ab cd", "ab ef"); !ok {
		t.Error("two lines sharing exactly half their text were not marked")
	}
	if _, _, ok := emphasis("ab cde", "ab fgh"); ok {
		t.Error("two lines sharing less than half their text were marked")
	}
}

// A change to nothing but punctuation is still a change, and marked as one.
func TestEmphasisMarksPunctuation(t *testing.T) {
	old, new, ok := emphasis("call(a, b)", "call[a, b]")
	if !ok {
		t.Fatal("a change of brackets was not marked")
	}
	if got := marked("call(a, b)", old); strings.Join(got, "|") != "(|)" {
		t.Errorf("old marks = %q, want the two brackets", got)
	}
	if got := marked("call[a, b]", new); strings.Join(got, "|") != "[|]" {
		t.Errorf("new marks = %q, want the two brackets", got)
	}
}

// The guarantees above, over a spread of hand-made pairs as well as whatever
// the fuzzers find, so that `go test` checks them without -fuzz.
func TestRenderGuarantees(t *testing.T) {
	plain(t)
	base := numbered(60)
	cases := [][2]string{
		{base, strings.Replace(base, "line 1\n", "", 1)},
		{base, strings.Replace(base, "line 60\n", "line sixty", 1)},
		{base, strings.ReplaceAll(base, "line 3", "LINE 3")},
		{base, "x\n" + base + "y\n"},
		{"a\nb\na\nb\n", "b\na\nb\na\n"},
		{"}\n}\n}\n", "}\n\n}\n"},
		{"", ""},
		{"\n\n\n", "\n\n"},
	}
	for _, c := range cases {
		checkRender(t, c[0], c[1])
	}
}

// An edit that mostly adds to a line keeps all of the line it started from,
// and is marked like any other: the cutoff weighs both lines together, not
// the shared text against the longer one alone.
func TestEmphasisMarksALineThatMostlyGrew(t *testing.T) {
	_, new, ok := emphasis("f(a)", "f(a, b, c, d)")
	if !ok {
		t.Fatal("a line that only grew was not marked")
	}
	if got := marked("f(a, b, c, d)", new); strings.Join(got, "|") != ", b, c, d" {
		t.Errorf("new marks = %q, want the arguments added", got)
	}
	if _, _, ok := emphasis("}", "return x }"); ok {
		t.Error("a closing brace was marked against an unrelated line ending in one")
	}
}
