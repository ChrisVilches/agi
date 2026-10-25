package diff

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/tools"
	"github.com/fatih/color"
)

// plain turns color off for the length of a test, so a rendered diff can be
// compared as text.
func plain(t *testing.T) {
	t.Helper()
	previous := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = previous })
}

// numbered returns a file of n lines reading "line 1" to "line n".
func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

// apply replays ops against a and b, rebuilding both sides, so a test can
// check that the ops really are a diff between them.
func apply(ops []op, a, b []string) (old, new []string) {
	for _, o := range ops {
		switch o.kind {
		case opKeep:
			if a[o.ai] != b[o.bi] {
				panic("kept lines differ")
			}
			old = append(old, a[o.ai])
			new = append(new, b[o.bi])
		case opDelete:
			old = append(old, a[o.ai])
		case opInsert:
			new = append(new, b[o.bi])
		}
	}
	return old, new
}

// Whatever two texts it is given, myers returns ops that take every line of
// the first in order and every line of the second in order. Random texts over
// a small alphabet exercise the ties and the long runs of equal lines.
func TestMyersRebuildsBothSides(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	words := []string{"a", "b", "c", "}", ""}
	gen := func() []string {
		lines := make([]string, r.Intn(12))
		for i := range lines {
			lines[i] = words[r.Intn(len(words))]
		}
		return lines
	}
	for range 2000 {
		a, b := gen(), gen()
		ops, ok := myers(a, b, maxEditDistance)
		if !ok {
			t.Fatalf("myers(%q, %q) gave up well inside its limit", a, b)
		}
		old, new := apply(ops, a, b)
		if strings.Join(old, "\n") != strings.Join(a, "\n") || strings.Join(new, "\n") != strings.Join(b, "\n") {
			t.Fatalf("myers(%q, %q) does not rebuild them", a, b)
		}
	}
}

// Past its limit the search gives up and says so, and one edit inside the
// limit is still found: the limit is a number of edits, not of lines.
func TestMyersGivesUpPastItsLimit(t *testing.T) {
	var a, b []string
	for i := range maxEditDistance {
		a = append(a, fmt.Sprint("a", i))
		b = append(b, fmt.Sprint("b", i))
	}
	if _, ok := myers(a, b, maxEditDistance); ok {
		t.Error("myers found a diff of 2000 edits within a limit of 1000")
	}
	changed := append(append([]string(nil), a[:500]...), "x")
	changed = append(changed, a[501:]...)
	if _, ok := myers(a, changed, maxEditDistance); !ok {
		t.Error("myers gave up on two edits")
	}
}

// When the search gives up, the line diff falls back to everything out and
// everything in, which is still a correct diff of the two.
func TestDiffLinesFallsBackCorrectly(t *testing.T) {
	var a, b []string
	for i := range maxEditDistance {
		a = append(a, fmt.Sprint("a", i))
		b = append(b, fmt.Sprint("b", i))
	}
	a = append([]string{"same"}, append(a, "end")...)
	b = append([]string{"same"}, append(b, "end")...)
	old, new := apply(diffLines(a, b), a, b)
	if strings.Join(old, "\n") != strings.Join(a, "\n") || strings.Join(new, "\n") != strings.Join(b, "\n") {
		t.Fatal("the fallback does not rebuild both sides")
	}
}

// The line that matters is the one that changed, whichever line of old_str
// it was, and it is shown with its number and three lines either side.
func TestRenderDiffShowsOnlyTheChangedLine(t *testing.T) {
	plain(t)
	before := numbered(20)
	after := strings.Replace(before, "line 10\n", "line ten\n", 1)
	want := strings.Join([]string{
		"   7   line 7",
		"   8   line 8",
		"   9   line 9",
		"  10 - line 10",
		"  10 + line ten",
		"  11   line 11",
		"  12   line 12",
		"  13   line 13",
	}, "\n")
	if got := Render(before, after); got != want {
		t.Errorf("Render =\n%s\nwant\n%s", got, want)
	}
}

// Two changes far apart are two hunks, and the unchanged lines between them
// are left out under a ⋮ rather than shown.
func TestRenderDiffLeavesOutTheMiddle(t *testing.T) {
	plain(t)
	before := numbered(30)
	after := strings.Replace(strings.Replace(before, "line 5\n", "five\n", 1), "line 25\n", "", 1)
	want := strings.Join([]string{
		"   2   line 2",
		"   3   line 3",
		"   4   line 4",
		"   5 - line 5",
		"   5 + five",
		"   6   line 6",
		"   7   line 7",
		"   8   line 8",
		"   ⋮",
		"  22   line 22",
		"  23   line 23",
		"  24   line 24",
		"  25 - line 25",
		"  25   line 26",
		"  26   line 27",
		"  27   line 28",
	}, "\n")
	if got := Render(before, after); got != want {
		t.Errorf("Render =\n%s\nwant\n%s", got, want)
	}
}

// Changes close enough that their context would overlap are one hunk, with
// every line between them shown.
func TestRenderDiffJoinsChangesThatAreClose(t *testing.T) {
	plain(t)
	before := numbered(20)
	after := strings.Replace(strings.Replace(before, "line 5\n", "five\n", 1), "line 11\n", "eleven\n", 1)
	if got := Render(before, after); strings.Contains(got, "⋮") {
		t.Errorf("Render =\n%s\nwant one hunk", got)
	}
}

// At the edges of the file there is less context to show, and nothing is
// invented to make up for it.
func TestRenderDiffAtTheEdgesOfTheFile(t *testing.T) {
	plain(t)
	before := numbered(5)
	after := strings.Replace(strings.Replace(before, "line 1\n", "first\n", 1), "line 5\n", "last\n", 1)
	want := strings.Join([]string{
		"  1 - line 1",
		"  1 + first",
		"  2   line 2",
		"  3   line 3",
		"  4   line 4",
		"  5 - line 5",
		"  5 + last",
	}, "\n")
	if got := Render(before, after); got != want {
		t.Errorf("Render =\n%s\nwant\n%s", got, want)
	}
}

// An insertion is + lines alone and a deletion - lines alone, each numbered
// in the file that has them.
func TestRenderDiffInsertionAndDeletion(t *testing.T) {
	plain(t)
	if got, want := Render("a\nc\n", "a\nb\nc\n"), "  1   a\n  2 + b\n  3   c"; got != want {
		t.Errorf("insertion =\n%s\nwant\n%s", got, want)
	}
	if got, want := Render("a\nb\nc\n", "a\nc\n"), "  1   a\n  2 - b\n  2   c"; got != want {
		t.Errorf("deletion =\n%s\nwant\n%s", got, want)
	}
}

// Adding the newline a file ended without changes no visible text, so the
// side without one says so, or the diff would show a line replaced by itself.
func TestRenderDiffShowsAMissingFinalNewline(t *testing.T) {
	plain(t)
	want := strings.Join([]string{
		"  1   a",
		"  2 - b",
		"      (no newline at end of file)",
		"  2 + b",
	}, "\n")
	if got := Render("a\nb", "a\nb\n"); got != want {
		t.Errorf("Render =\n%s\nwant\n%s", got, want)
	}
}

// A file with Windows line endings is shown without the carriage returns,
// which would otherwise send the cursor back to the start of the row.
func TestRenderDiffDropsCarriageReturns(t *testing.T) {
	plain(t)
	if got := Render("a\r\nb\r\n", "a\r\nc\r\n"); strings.Contains(got, "\r") {
		t.Errorf("Render = %q, want no carriage return", got)
	}
}

// The numbers are right-aligned to the widest one shown.
func TestRenderDiffAlignsTheNumbers(t *testing.T) {
	plain(t)
	before := numbered(12)
	after := strings.Replace(before, "line 9\n", "nine\n", 1)
	got := Render(before, after)
	if !strings.Contains(got, "\n   9 - line 9\n") || !strings.Contains(got, "\n  10   line 10") {
		t.Errorf("Render =\n%s\nwant the numbers right-aligned to two digits", got)
	}
}

// A change bigger than the screen is cut short and says how much was left
// out, rather than pushing everything before it out of sight.
func TestRenderDiffIsCapped(t *testing.T) {
	plain(t)
	before := numbered(100)
	after := strings.ReplaceAll(before, "line", "LINE")
	rows := strings.Split(Render(before, after), "\n")
	if len(rows) != maxDiffRows+1 {
		t.Fatalf("got %d rows, want %d and a line saying how many more", len(rows), maxDiffRows)
	}
	if want := fmt.Sprintf("… %d more lines", 200-maxDiffRows); !strings.HasSuffix(rows[maxDiffRows], want) {
		t.Errorf("last row = %q, want it to end %q", rows[maxDiffRows], want)
	}
}

// Each kind of line is drawn in the style a diff is drawn in: red out, green
// in, and the context and numbers dimmed.
func TestRenderDiffColors(t *testing.T) {
	previous := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = previous })

	got := strings.Split(Render("a\nb\n", "a\nc\n"), "\n")
	want := []string{
		tools.Detail("  1") + tools.Detail("   a"),
		tools.Detail("  2") + " " + tools.Removed("- b"),
		tools.Detail("  2") + " " + tools.Added("+ c"),
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Render = %q, want %q", got, want)
	}
}

// the pieces put back together are the line.
func TestTokenize(t *testing.T) {
	cases := map[string][]string{
		"timeout: 30":  {"timeout", ":", " ", "30"},
		"  f(a_b, 2)":  {"  ", "f", "(", "a_b", ",", " ", "2", ")"},
		"größe = año":  {"größe", " ", "=", " ", "año"},
		"":             nil,
		"\t\tx  ->  y": {"\t\t", "x", "  ", "-", ">", "  ", "y"},
	}
	for line, want := range cases {
		got := tokenize(line)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("tokenize(%q) = %q, want %q", line, got, want)
		}
		if strings.Join(got, "") != line {
			t.Errorf("tokenize(%q) does not join back into the line", line)
		}
	}
}

// marked returns the text of each span, so a test can say what was marked.
func marked(line string, spans []span) []string {
	var out []string
	for _, s := range spans {
		out = append(out, line[s.from:s.to])
	}
	return out
}

func TestEmphasis(t *testing.T) {
	cases := []struct {
		name, old, new string
		oldMarks       []string
		newMarks       []string
	}{
		{"one value", "timeout: 30", "timeout: 120", []string{"30"}, []string{"120"}},
		{"two separate edits", "result := f(a, b)", "result := g(a, c)", []string{"f", "b"}, []string{"g", "c"}},
		{"words side by side join", "say foo bar now", "say baz qux now", []string{"foo bar"}, []string{"baz qux"}},
		{"an insertion marks one side", "f(a)", "f(a, b)", nil, []string{", b"}},
		{"indentation", "  return x", "\treturn x", []string{"  "}, []string{"\t"}},
	}
	for _, c := range cases {
		old, new, ok := emphasis(c.old, c.new)
		if !ok {
			t.Errorf("%s: emphasis declined, want marks", c.name)
			continue
		}
		if got := marked(c.old, old); strings.Join(got, "|") != strings.Join(c.oldMarks, "|") {
			t.Errorf("%s: old marks = %q, want %q", c.name, got, c.oldMarks)
		}
		if got := marked(c.new, new); strings.Join(got, "|") != strings.Join(c.newMarks, "|") {
			t.Errorf("%s: new marks = %q, want %q", c.name, got, c.newMarks)
		}
	}
}

// Two lines with little in common get no marks: either they were paired
// wrongly or the line was rewritten, and marking all of it says nothing.
func TestEmphasisDeclinesLinesThatAreTooDifferent(t *testing.T) {
	if _, _, ok := emphasis("return nil, err", "log.Printf(\"done\")"); ok {
		t.Error("emphasis marked two unrelated lines")
	}
	if _, _, ok := emphasis("same", "same"); ok {
		t.Error("emphasis marked two identical lines")
	}
}

// emphasized turns color on for the length of a test.
func emphasized(t *testing.T) {
	t.Helper()
	previous := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = previous })
}

// A changed line paired with its replacement is drawn in its own color with
// the part that changed in reverse video.
func TestRenderDiffMarksTheChangedWords(t *testing.T) {
	emphasized(t)
	got := strings.Split(Render("timeout: 30\n", "timeout: 120\n"), "\n")
	want := []string{
		tools.Detail("  1") + " " + tools.Removed("- timeout: ") + tools.RemovedEmphasis("30"),
		tools.Detail("  1") + " " + tools.Added("+ timeout: ") + tools.AddedEmphasis("120"),
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Render = %q, want %q", got, want)
	}
}

// Lines are paired in order, and a line left over with nothing to pair with
// is drawn plain.
func TestRenderDiffPairsLinesInOrder(t *testing.T) {
	emphasized(t)
	got := strings.Split(Render("a = 1\nb = 2\n", "a = 10\n"), "\n")
	want := []string{
		tools.Detail("  1") + " " + tools.Removed("- a = ") + tools.RemovedEmphasis("1"),
		tools.Detail("  2") + " " + tools.Removed("- b = 2"),
		tools.Detail("  1") + " " + tools.Added("+ a = ") + tools.AddedEmphasis("10"),
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Render = %q, want %q", got, want)
	}
}

// Chinese and Japanese are written without spaces, so a run of their letters
// is not a word, and treating it as one made a whole clause one token: a line
// that changed two words in the middle of it came out as one long piece
// replaced, too little in common to be marked at all. Each character is a
// token of its own instead.
func TestEmphasisMarksWordsInJapanese(t *testing.T) {
	old := "（モーニングむすめ）は、ハロー!プロジェクトに所属する日本の女性アイドルグループ[2][3]。"
	new := "（モーニングむすめ）は、ハロー!プロジェクトに在籍する日本を拠点とする女性アイドルグループ[2][3]。"
	oldSpans, newSpans, ok := emphasis(old, new)
	if !ok {
		t.Fatal("emphasis declined a line that changed two words")
	}
	if got := marked(old, oldSpans); strings.Join(got, "|") != "所属|の" {
		t.Errorf("old marks = %q, want 所属 and の", got)
	}
	if got := marked(new, newSpans); strings.Join(got, "|") != "在籍|を拠点とする" {
		t.Errorf("new marks = %q, want 在籍 and を拠点とする", got)
	}
}

// Each CJK character stands alone, the marks that only lengthen a sound or
// repeat a character included, while a word in a script with spaces stays
// whole beside them.
func TestTokenizeSplitsCJKIntoCharacters(t *testing.T) {
	cases := map[string][]string{
		"日本語":         {"日", "本", "語"},
		"グループ":        {"グ", "ル", "ー", "プ"},
		"人々":          {"人", "々"},
		"Go言語で書くcode": {"Go", "言", "語", "で", "書", "く", "code"},
		"안녕 하세요":      {"안녕", " ", "하세요"},
	}
	for line, want := range cases {
		if got := tokenize(line); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("tokenize(%q) = %q, want %q", line, got, want)
		}
	}
}

// The marks reach the screen: the drawn row carries the changed words in
// reverse video and the rest of the line in its plain color.
func TestRenderMarksJapanese(t *testing.T) {
	emphasized(t)
	got := Render("日本の女性\n", "日本を拠点とする女性\n")
	for _, want := range []string{
		tools.Removed("- 日本") + tools.RemovedEmphasis("の") + tools.Removed("女性"),
		tools.Added("+ 日本") + tools.AddedEmphasis("を拠点とする") + tools.Added("女性"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render = %q, want it to contain %q", got, want)
		}
	}
}

// Added numbers a new file's lines from 1, stops at the limit, and widens the
// number column to the widest number it shows.
func TestAddedDrawsTheFirstLinesOfANewFile(t *testing.T) {
	plain(t)
	if got, want := Added("a\nb\nc\n", 2), "  1 + a\n  2 + b"; got != want {
		t.Errorf("Added() =\n%s\nwant\n%s", got, want)
	}
	got := strings.Split(Added(numbered(12), 10), "\n")
	if len(got) != 10 || got[0] != "   1 + line 1" || got[9] != "  10 + line 10" {
		t.Errorf("Added() = %q, want ten rows with a two-digit column", got)
	}
	// Lines are read the way cat and wc read them: a final newline ends the
	// last line rather than starting an empty one, so a text with one and a
	// text without one are drawn alike.
	for _, text := range []string{"a\nb\n", "a\nb"} {
		if got, want := Added(text, 5), "  1 + a\n  2 + b"; got != want {
			t.Errorf("Added(%q) =\n%s\nwant\n%s", text, got, want)
		}
	}
}

// Lines counts the rows Added draws: what wc -l counts, plus a last line that
// has no newline.
func TestLinesCountsWhatAddedDraws(t *testing.T) {
	for text, want := range map[string]int{"": 0, "a": 1, "a\n": 1, "a\nb": 2, "\n\n": 2} {
		if got := Lines(text); got != want {
			t.Errorf("Lines(%q) = %d, want %d", text, got, want)
		}
	}
}
