// Package diff draws the change between two texts the way git draws one, for
// a person watching a run: numbered lines, a few lines of context, a ⋮ where
// unchanged lines are left out, and the words that changed inside a changed
// line marked. It is display only. Render is the whole of its surface, and
// nothing it returns is meant to be read back by a program.
//
// It exists for patchfile, which announces a call with the diff the call is
// about to make. One call is one replacement, so the two texts differ in one
// stretch and nowhere else, and that is what keeps the work small: the lines
// the two share at either end are trimmed off first, and the line diff runs
// only over what is left — the size of the file does not enter into it.
// Texts that differ in several places are still drawn correctly, only with
// more of them searched.
package diff

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ChrisVilches/agi/tools"
)

const (
	// diffContext is how many unchanged lines are shown either side of a
	// change, which is git's default and what a reader of a diff is used to.
	diffContext = 3
	// maxDiffRows caps the lines a diff is shown in. The announcement is read
	// as a run goes past, and a call that rewrites a whole file would
	// otherwise push everything before it off the screen.
	maxDiffRows = 40
	// maxEditDistance bounds the search for the smallest diff. Past it the two
	// sides are shown as all of one taken out and all of the other put in,
	// which is still true and is what a change that large reads as anyway.
	maxEditDistance = 1000
)

type opKind int

const (
	opKeep opKind = iota
	opDelete
	opInsert
)

// op is one line of a diff: a line both sides share, one only the old file
// has, or one only the new file has. The indexes are into the old and new
// lines, and only the one that side has means anything for a delete or an
// insert.
type op struct {
	kind   opKind
	ai, bi int
}

// splitLines cuts text into lines that keep their newline, so that a last line
// with one and a last line without one are different lines. That is what lets
// a patch that only adds or removes the newline at the end of a file show as a
// change rather than as nothing.
func splitLines(s string) []string {
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// diffLines returns the ops that turn a into b, with the lines the two share
// at either end trimmed to diffContext of each rather than listed in full.
func diffLines(a, b []string) []op {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	// Two equal texts have no change to put context around, and the context
	// the loops below would take from the end of them is not a diff of
	// anything.
	if pre == len(a) && pre == len(b) {
		return nil
	}

	var ops []op
	for i := max(0, pre-diffContext); i < pre; i++ {
		ops = append(ops, op{opKeep, i, i})
	}
	for _, o := range myers(a[pre:len(a)-suf], b[pre:len(b)-suf]) {
		o.ai += pre
		o.bi += pre
		ops = append(ops, o)
	}
	for i := 0; i < min(suf, diffContext); i++ {
		ops = append(ops, op{opKeep, len(a) - suf + i, len(b) - suf + i})
	}
	return groupChanges(ops)
}

// myers finds a shortest list of ops turning a into b, by Myers' O(ND)
// algorithm. When the two are further apart than maxEditDistance it stops
// looking and takes all of a out and puts all of b in.
func myers(a, b []string) []op {
	n, m := len(a), len(b)
	limit := min(n+m, maxEditDistance)
	off := limit + 1
	v := make([]int, 2*off+1)
	var trace [][]int

	for d := 0; d <= limit; d++ {
		trace = append(trace, append([]int(nil), v...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				return backtrack(trace, off, n, m)
			}
		}
	}

	ops := make([]op, 0, n+m)
	for i := range n {
		ops = append(ops, op{opDelete, i, 0})
	}
	for j := range m {
		ops = append(ops, op{opInsert, n, j})
	}
	return ops
}

// backtrack walks the saved frontiers back from the end to recover the path
// myers found, and returns it in order.
func backtrack(trace [][]int, off, x, y int) []op {
	var ops []op
	for d := len(trace) - 1; d >= 0; d-- {
		v := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := v[off+prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			ops = append(ops, op{opKeep, x, y})
		}
		if d > 0 {
			if x == prevX {
				ops = append(ops, op{opInsert, x, y - 1})
			} else {
				ops = append(ops, op{opDelete, x - 1, y})
			}
		}
		x, y = prevX, prevY
	}
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops
}

// groupChanges puts every run of changed lines in the order a diff is read in:
// what goes out, then what comes in. A shortest diff is free to interleave the
// two, and a line shown taken out, another put in, and a third taken out reads
// as three changes where there was one.
func groupChanges(ops []op) []op {
	out := make([]op, 0, len(ops))
	for i := 0; i < len(ops); {
		if ops[i].kind == opKeep {
			out = append(out, ops[i])
			i++
			continue
		}
		j := i
		for j < len(ops) && ops[j].kind != opKeep {
			j++
		}
		for _, o := range ops[i:j] {
			if o.kind == opDelete {
				out = append(out, o)
			}
		}
		for _, o := range ops[i:j] {
			if o.kind == opInsert {
				out = append(out, o)
			}
		}
		i = j
	}
	return out
}

// Render draws the change from before to after as git draws one: a column
// of line numbers, the lines taken out marked - in red, the lines put in
// marked + in green, and a few unchanged lines around them dimmed. Where two
// changes are far enough apart that the lines between them say nothing, those
// lines are left out and a ⋮ stands in their place.
//
// A line taken out carries its number in the old file, and every other line
// its number in the new one, so each number is where that line is or was.
func Render(before, after string) string {
	a, b := splitLines(before), splitLines(after)
	ops := collapse(diffLines(a, b))

	width := 1
	for _, o := range ops {
		if o != nil {
			width = max(width, len(fmt.Sprint(lineNumber(*o))))
		}
	}
	gutter := "  " + strings.Repeat(" ", width)

	marks := pairMarks(ops, a, b)

	var rows []string
	shown := 0
	for i, o := range ops {
		if shown == maxDiffRows {
			if rest := countLines(ops[i:]); rest > 0 {
				rows = append(rows, tools.Detail(fmt.Sprintf("%s … %d more lines", gutter, rest)))
			}
			break
		}
		if o == nil {
			rows = append(rows, tools.Detail(gutter[:len(gutter)-1]+"⋮"))
			continue
		}
		shown++

		number := tools.Detail(fmt.Sprintf("  %*d", width, lineNumber(*o)))
		var line string
		switch o.kind {
		case opKeep:
			rows = append(rows, number+tools.Detail("   "+display(b[o.bi])))
			continue
		case opDelete:
			line = a[o.ai]
			rows = append(rows, number+" "+paint(tools.Removed, tools.RemovedEmphasis, "- ", display(line), marks[i]))
		case opInsert:
			line = b[o.bi]
			rows = append(rows, number+" "+paint(tools.Added, tools.AddedEmphasis, "+ ", display(line), marks[i]))
		}
		if !strings.HasSuffix(line, "\n") {
			rows = append(rows, tools.Detail(gutter+"   (no newline at end of file)"))
		}
	}
	return strings.Join(rows, "\n")
}

// span is a stretch of a line, as byte offsets, that changed.
type span struct{ from, to int }

// pairMarks works out, for every row of the diff, which parts of its line
// changed, and leaves a row it has nothing to say about with none.
//
// A run of changed lines is the lines taken out followed by the lines put in,
// and the first taken out is paired with the first put in, the second with
// the second, and so on. That is a guess, and a good one: an edit nearly
// always keeps its lines in the order they were in. A guess that pairs two
// lines with little in common is caught by emphasis, which declines to mark
// them. It is worked out over the whole run before anything is drawn, since
// the cap on rows can cut a run in half.
func pairMarks(ops []*op, a, b []string) [][]span {
	marks := make([][]span, len(ops))
	for i := 0; i < len(ops); {
		if ops[i] == nil || ops[i].kind == opKeep {
			i++
			continue
		}
		var dels, ins []int
		for ; i < len(ops) && ops[i] != nil && ops[i].kind != opKeep; i++ {
			if ops[i].kind == opDelete {
				dels = append(dels, i)
			} else {
				ins = append(ins, i)
			}
		}
		for k := range min(len(dels), len(ins)) {
			d, n := dels[k], ins[k]
			if old, new, ok := emphasis(display(a[ops[d].ai]), display(b[ops[n].bi])); ok {
				marks[d], marks[n] = old, new
			}
		}
	}
	return marks
}

// emphasis diffs two lines word by word and returns the parts of each that
// changed. It reports false when there is nothing worth marking: the lines
// differ in nothing that shows, or they have less than half their text in
// common, which means either the pairing was wrong or the line was rewritten
// outright, and marking nearly every word of it says less than marking none.
//
// Words rather than characters, because characters that happen to match make
// a mess of the marks — count and amount share "ount" — and a person reads a
// change a word at a time anyway.
func emphasis(old, new string) ([]span, []span, bool) {
	at, bt := tokenize(old), tokenize(new)
	aoff, boff := offsets(at), offsets(bt)

	var oldSpans, newSpans []span
	shared := 0
	for _, o := range myers(at, bt) {
		switch o.kind {
		case opKeep:
			shared += visible(at[o.ai])
		case opDelete:
			oldSpans = appendSpan(oldSpans, old, aoff[o.ai], aoff[o.ai]+len(at[o.ai]))
		case opInsert:
			newSpans = appendSpan(newSpans, new, boff[o.bi], boff[o.bi]+len(bt[o.bi]))
		}
	}
	if len(oldSpans) == 0 && len(newSpans) == 0 {
		return nil, nil, false
	}
	if 2*shared < max(visible(old), visible(new)) {
		return nil, nil, false
	}
	return oldSpans, newSpans, true
}

// appendSpan adds a changed stretch of line, joining it to the one before when
// nothing but space lies between them, so that two words replaced side by side
// are marked as one change and not two with a gap in it.
func appendSpan(spans []span, line string, from, to int) []span {
	if n := len(spans); n > 0 && strings.TrimSpace(line[spans[n-1].to:from]) == "" {
		spans[n-1].to = to
		return spans
	}
	return append(spans, span{from, to})
}

// tokenize cuts a line into the units it is diffed in: a run of letters,
// digits and underscores is a word, a run of space is one token, and every
// other character is a token of its own, so that punctuation next to a word
// does not make the word look changed. The tokens joined are the line.
func tokenize(s string) []string {
	var tokens []string
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		end := size
		switch {
		case isWord(r):
			for end < len(s) {
				r, n := utf8.DecodeRuneInString(s[end:])
				if !isWord(r) {
					break
				}
				end += n
			}
		case unicode.IsSpace(r):
			for end < len(s) {
				r, n := utf8.DecodeRuneInString(s[end:])
				if !unicode.IsSpace(r) {
					break
				}
				end += n
			}
		}
		tokens = append(tokens, s[:end])
		s = s[end:]
	}
	return tokens
}

func isWord(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// offsets returns where each token starts in the line they were cut from.
func offsets(tokens []string) []int {
	at := make([]int, len(tokens))
	n := 0
	for i, t := range tokens {
		at[i] = n
		n += len(t)
	}
	return at
}

// visible counts the characters of s that are not space, which is what "how
// much two lines have in common" is measured in: indentation is not content.
func visible(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}

// paint draws one changed line: the marker and the parts that did not change
// in the line's own style, and the parts that did in its emphasis. A line
// with no marks is drawn all in the one style, as it was before marks
// existed, and with color off the pieces join into exactly that text.
func paint(plain, emph func(string) string, marker, text string, marks []span) string {
	if len(marks) == 0 {
		return plain(marker + text)
	}
	out := plain(marker + text[:marks[0].from])
	for i, m := range marks {
		out += emph(text[m.from:m.to])
		end := len(text)
		if i+1 < len(marks) {
			end = marks[i+1].from
		}
		if m.to < end {
			out += plain(text[m.to:end])
		}
	}
	return out
}

// collapse leaves out the unchanged lines between two changes that are more
// than twice diffContext apart, keeping diffContext either side and putting a
// nil where the rest were. The runs at either end were already cut to
// diffContext by diffLines.
func collapse(ops []op) []*op {
	var out []*op
	for i := 0; i < len(ops); {
		if ops[i].kind != opKeep {
			out = append(out, &ops[i])
			i++
			continue
		}
		j := i
		for j < len(ops) && ops[j].kind == opKeep {
			j++
		}
		inner := i > 0 && j < len(ops)
		if inner && j-i > 2*diffContext {
			for k := i; k < i+diffContext; k++ {
				out = append(out, &ops[k])
			}
			out = append(out, nil)
			for k := j - diffContext; k < j; k++ {
				out = append(out, &ops[k])
			}
		} else {
			for k := i; k < j; k++ {
				out = append(out, &ops[k])
			}
		}
		i = j
	}
	return out
}

// lineNumber is the number a row of the diff is shown with: the old file's
// for a line taken out, and the new file's for everything else.
func lineNumber(o op) int {
	if o.kind == opDelete {
		return o.ai + 1
	}
	return o.bi + 1
}

// countLines counts the rows among ops that are lines, leaving out the gaps.
func countLines(ops []*op) int {
	n := 0
	for _, o := range ops {
		if o != nil {
			n++
		}
	}
	return n
}

// display is a line as it is printed: without its newline, and without the
// carriage return a file with Windows line endings puts before it.
func display(line string) string {
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
}
