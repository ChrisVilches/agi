package workspace

import (
	"fmt"
	"strings"

	"github.com/ChrisVilches/agi/tools"
)

// The diff patchfile announces a call with. It is display only: the model is
// never shown it, and nothing about what the call does depends on it.
//
// One call is one replacement, so the file before and the file after differ
// in one stretch and nowhere else. That is what keeps this small. The lines
// the two files share at either end are trimmed off first, and the line diff
// runs over what is left, which is never more than old_str and new_str — the
// size of a file does not enter into it.

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

// renderDiff draws the change from before to after as git draws one: a column
// of line numbers, the lines taken out marked - in red, the lines put in
// marked + in green, and a few unchanged lines around them dimmed. Where two
// changes are far enough apart that the lines between them say nothing, those
// lines are left out and a ⋮ stands in their place.
//
// A line taken out carries its number in the old file, and every other line
// its number in the new one, so each number is where that line is or was.
func renderDiff(before, after string) string {
	a, b := splitLines(before), splitLines(after)
	ops := collapse(diffLines(a, b))

	width := 1
	for _, o := range ops {
		if o != nil {
			width = max(width, len(fmt.Sprint(lineNumber(*o))))
		}
	}
	gutter := "  " + strings.Repeat(" ", width)

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
			rows = append(rows, number+" "+tools.Removed("- "+display(line)))
		case opInsert:
			line = b[o.bi]
			rows = append(rows, number+" "+tools.Added("+ "+display(line)))
		}
		if !strings.HasSuffix(line, "\n") {
			rows = append(rows, tools.Detail(gutter+"   (no newline at end of file)"))
		}
	}
	return strings.Join(rows, "\n")
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
