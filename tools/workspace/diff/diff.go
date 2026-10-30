// Package diff draws the change between two texts the way git draws one, for
// a person watching a run: numbered lines, a few lines of context, a ⋮ where
// unchanged lines are left out, and the words that changed inside a changed
// line marked. It is display only. Render is most of its surface — Added and
// Lines are the rest — and nothing it returns is meant to be read back by a
// program.
//
// It exists for patchfile, which announces a call with the diff the call is
// about to make. One call is one replacement, so the two texts differ in one
// stretch and nowhere else, and that is what keeps the work small: the lines
// the two share at either end are trimmed off first, and the line diff runs
// only over what is left — the size of the file does not enter into it.
// Texts that differ in several places are still drawn correctly, only with
// more of them searched.
//
// Added is for writefile, which creates a file and has nothing before it to
// diff against, so its announcement is the first lines of the new file drawn
// as the additions they are.
package diff

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ChrisVilches/agi/tools"
	"github.com/mattn/go-runewidth"
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
	// maxWordEdits is the same bound for the word diff of a run of changed
	// lines, counted per line of the run's longer side, and is far smaller. A
	// line that changed in more places than this is not worth marking — the
	// cutoff in runMarks would usually decline it anyway — and giving up early
	// is what keeps a long minified line cheap, since the search is the
	// expensive part.
	maxWordEdits = 100
	// maxRunEdits caps that bound for a run however many lines it has, so a
	// run the size of the screen is searched no further than the line diff
	// would search a file.
	maxRunEdits = maxEditDistance
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
	mid, ok := myers(a[pre:len(a)-suf], b[pre:len(b)-suf], maxEditDistance)
	if !ok {
		// Further apart than the search reaches: all of one side out and all
		// of the other in, which is still true and is what a change that
		// large reads as anyway.
		mid = nil
		for i := pre; i < len(a)-suf; i++ {
			mid = append(mid, op{opDelete, i - pre, 0})
		}
		for j := pre; j < len(b)-suf; j++ {
			mid = append(mid, op{opInsert, len(a) - suf - pre, j - pre})
		}
	}
	for _, o := range mid {
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
// algorithm, and reports false when the two are further apart than limit:
// what giving up should mean is the caller's to say, and building a list of
// everything out and everything in for a caller that only wanted to know
// whether the search succeeded cost a long line most of its time.
//
// Each step saves the part of the frontier the walk back will read, which at
// step d is the diagonals -d-1 to d+1 and nothing wider: saving the whole
// array every step made a search that ran to its limit cost the square of
// the limit in memory twice over.
func myers(a, b []string, limit int) ([]op, bool) {
	n, m := len(a), len(b)
	limit = min(n+m, limit)
	off := limit + 1
	v := make([]int, 2*off+1)
	var trace [][]int

	for d := 0; d <= limit; d++ {
		trace = append(trace, append([]int(nil), v[off-d-1:off+d+2]...))
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
				return backtrack(trace, n, m), true
			}
		}
	}
	return nil, false
}

// backtrack walks the saved frontiers back from the end to recover the path
// myers found, and returns it in order. The frontier saved at step d starts
// at diagonal -d-1, so diagonal k of it is at k+d+1.
func backtrack(trace [][]int, x, y int) []op {
	var ops []op
	for d := len(trace) - 1; d >= 0; d-- {
		v, at := trace[d], d+1
		k := x - y
		var prevK int
		if k == -d || (k != d && v[at+k-1] < v[at+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := v[at+prevK]
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
// of line numbers, the lines taken out marked - on a red band, the lines put
// in marked + on a green one, and a few unchanged lines around them dimmed.
// How a band is painted, and what is drawn instead on a terminal that has not
// said it draws more than sixteen colors, is the tools package's. Where two
// changes are far enough apart that the lines between them say nothing, those
// lines are left out and a ⋮ stands in their place.
//
// A line taken out carries its number in the old file, and every other line
// its number in the new one, so each number is where that line is or was.
//
// Everything past the cap is cut off before anything else is worked out, so
// a rewritten file costs what its first maxDiffRows lines cost: the width of
// the number column is the widest number shown, and only rows shown are
// marked, since the word diffs that marking runs are the expensive part of
// drawing a diff. Words are compared only with the rows shown, so a word that
// moved into a line the cap hides is marked as taken out rather than matched
// with a line the reader cannot see.
func Render(before, after string) string {
	a, b := splitLines(before), splitLines(after)
	ops, rest := cut(collapse(diffLines(a, b)))

	width := 1
	for _, o := range ops {
		if o != nil {
			width = max(width, len(fmt.Sprint(lineNumber(*o))))
		}
	}
	gutter := "  " + strings.Repeat(" ", width)

	marks := wordMarks(ops, a, b)

	var rows []string
	for i, o := range ops {
		if o == nil {
			rows = append(rows, tools.Detail(gutter[:len(gutter)-1]+"⋮"))
			continue
		}

		number := fmt.Sprintf("  %*d", width, lineNumber(*o))
		var line string
		switch o.kind {
		case opKeep:
			rows = append(rows, tools.Detail(number)+tools.Detail("   "+display(b[o.bi])))
			continue
		case opDelete:
			line = a[o.ai]
			rows = append(rows, tools.RemovedMargin(number+" ")+paint(tools.Removed, tools.RemovedEmphasis, "- ", display(line), marks[i]))
		case opInsert:
			line = b[o.bi]
			rows = append(rows, tools.AddedMargin(number+" ")+paint(tools.Added, tools.AddedEmphasis, "+ ", display(line), marks[i]))
		}
		if !strings.HasSuffix(line, "\n") {
			rows = append(rows, tools.Detail(gutter+"   (no newline at end of file)"))
		}
	}
	switch {
	case rest == 1:
		rows = append(rows, tools.Detail(gutter+" … 1 more line"))
	case rest > 1:
		rows = append(rows, tools.Detail(fmt.Sprintf("%s … %d more lines", gutter, rest)))
	}
	return strings.Join(rows, "\n")
}

// cut keeps the rows of a diff up to its maxDiffRows-th line and reports how
// many lines it left out after them. A gap that would come straight after
// the last line kept goes with what was left out, since there is nothing
// after it to separate.
func cut(ops []*op) ([]*op, int) {
	lines := 0
	for i, o := range ops {
		if lines == maxDiffRows {
			return ops[:i], countLines(ops[i:])
		}
		if o != nil {
			lines++
		}
	}
	return ops, 0
}

// span is a stretch of a line, as byte offsets, that changed.
type span struct{ from, to int }

// wordMarks works out, for every row of the diff, which parts of its line
// changed, and leaves a row it has nothing to say about with none.
//
// A run of changed lines is the lines taken out followed by the lines put in,
// and the run is diffed as a whole: the words of every line taken out against
// the words of every line put in, one stream each, with a line break counting
// as a single space. Diffing line against line was tried first and is wrong
// for prose, where a reworded paragraph is wrapped again and words move from
// one line to the next: a word that only moved was marked as taken out of one
// line and put into its neighbour, and a line whose words were spread over
// two others matched neither and was left with no marks at all. Diffing the
// stream is what git diff --word-diff does, and it costs code nothing — code
// wrapped in a new block still finds every old line inside the new one,
// because the words of each are still in the same order.
func wordMarks(ops []*op, a, b []string) [][]span {
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
		old, new := make([]string, len(dels)), make([]string, len(ins))
		for k, d := range dels {
			old[k] = display(a[ops[d].ai])
		}
		for k, n := range ins {
			new[k] = display(b[ops[n].bi])
		}
		oldSpans, newSpans := runMarks(old, new)
		for k, d := range dels {
			marks[d] = oldSpans[k]
		}
		for k, n := range ins {
			marks[n] = newSpans[k]
		}
	}
	return marks
}

// words is a run of lines cut into the stream of words it is diffed in.
// keys is what each word is compared as, line which line it is on — -1 for
// the break between two lines — and at where in that line it starts.
type words struct {
	lines   []string
	tokens  []string
	keys    []string
	line    []int
	at      []int
	visible []int
}

// lineBreak is what the break between two lines is compared as: the single
// space it would be if the paragraph were wrapped somewhere else.
const lineBreak = " "

func newWords(lines []string) *words {
	w := &words{lines: lines, visible: make([]int, len(lines))}
	for l, text := range lines {
		if l > 0 {
			w.add("", lineBreak, -1, 0)
		}
		at := 0
		for k, token := range tokenize(text) {
			key := token
			if k == 0 && strings.TrimSpace(token) == "" {
				// Indentation is compared as itself and never as the space
				// between two words, so a line that only moved into a block
				// is still marked where it gained its indentation.
				key = "\n" + token
			}
			w.add(token, key, l, at)
			at += len(token)
		}
		w.visible[l] = visible(text)
	}
	return w
}

func (w *words) add(token, key string, line, at int) {
	w.tokens = append(w.tokens, token)
	w.keys = append(w.keys, key)
	w.line = append(w.line, line)
	w.at = append(w.at, at)
}

// runMarks diffs a run's lines taken out against its lines put in as one
// stream of words, and returns the marks on each line of either side. It
// marks nothing when there is nothing worth marking.
//
// A line keeps its marks only when at least a third of it was kept. A line
// put in or taken out whole has nothing kept and is drawn plain, as is one
// that shares no more than a stray bracket with the rest of the run, which is
// a word matched by chance rather than a line that changed.
//
// The run is not marked at all when its lines have too little in common,
// which means it was rewritten outright, and marking nearly every word of it
// says less than marking none. Too little is a similarity below one half,
// measured as the text kept, counted on both sides, over all the text of
// both — 2·shared/(old+new), the ratio Python's difflib uses. It is
// measured over both sides together rather than against the longer one,
// because an edit that mostly adds text keeps the whole of what it started
// from and is still plainly the same text: against the longer side alone,
// f(a) growing into f(a, b, c, d) or 日本の女性 into 日本を拠点とする女性 had too
// little in common to be marked. A third is where that ratio puts a single
// line that only grew, so the two rules agree on a run of one line taken out
// and one put in, which is marked exactly as the ratio says. The lines put in
// or taken out whole are left out of the ratio, since they have no partner to
// be similar to, and counting them declined every edit with a new line beside
// it.
//
// A shortest diff is seldom the only one, and across lines the choice
// between them matters: a short word such as a, =, or ) is found on many
// lines, and which of them it is matched with decides which line keeps its
// marks. Myers follows a match as soon as it finds one and so keeps the
// earliest; run backwards it keeps the latest. Neither is right every time —
// the earliest matched a with the a of a comment put in above the line that
// changed, and the latest matched the ) of a line taken out with the one of a
// line put in whole after its partner — so the run is diffed both ways and
// the marks kept are the ones that show more of it as unchanged, counting
// only the lines that keep their marks. A tie keeps the earliest.
func runMarks(old, new []string) ([][]span, [][]span) {
	a, b := newWords(old), newWords(new)
	limit := min(maxWordEdits*max(len(old), len(new)), maxRunEdits)
	oldSpans, newSpans := make([][]span, len(old)), make([][]span, len(new))
	best := 0
	for _, latest := range []bool{false, true} {
		ops, ok := myersFrom(a.keys, b.keys, limit, latest)
		if !ok {
			// Both ways are equally long, so if one gives up so does the
			// other.
			break
		}
		if o, n, kept := spansOf(ops, a, b); kept > best {
			oldSpans, newSpans, best = o, n, kept
		}
	}
	trimSpans(oldSpans, old)
	trimSpans(newSpans, new)
	return oldSpans, newSpans
}

// myersFrom is myers keeping the earliest of equally short diffs, or, when
// latest is set, the latest — the same search over both sides backwards, its
// answer turned back around.
func myersFrom(a, b []string, limit int, latest bool) ([]op, bool) {
	if !latest {
		return myers(a, b, limit)
	}
	ops, ok := myers(reversed(a), reversed(b), limit)
	for i, j := 0, len(ops)-1; i <= j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
		ops[i].ai, ops[i].bi = len(a)-1-ops[i].ai, len(b)-1-ops[i].bi
		if i != j {
			ops[j].ai, ops[j].bi = len(a)-1-ops[j].ai, len(b)-1-ops[j].bi
		}
	}
	return ops, ok
}

func reversed(s []string) []string {
	out := make([]string, len(s))
	for i, x := range s {
		out[len(s)-1-i] = x
	}
	return out
}

// spansOf turns a diff of a run's words into the marks on each line under the
// rules runMarks gives, and reports how much text the lines that keep their
// marks kept, which is zero when nothing is marked.
func spansOf(ops []op, a, b *words) ([][]span, [][]span, int) {
	old, new := a.lines, b.lines
	oldSpans, newSpans := make([][]span, len(old)), make([][]span, len(new))
	oldKept, newKept := make([]int, len(old)), make([]int, len(new))
	for _, o := range ops {
		i, j := o.ai, o.bi
		switch o.kind {
		case opKeep:
			v := visible(a.tokens[i])
			if l := a.line[i]; l >= 0 {
				oldKept[l] += v
			}
			if l := b.line[j]; l >= 0 {
				newKept[l] += v
			}
		case opDelete:
			if l := a.line[i]; l >= 0 {
				oldSpans[l] = appendSpan(oldSpans[l], old[l], a.at[i], a.at[i]+len(a.tokens[i]))
			}
		case opInsert:
			if l := b.line[j]; l >= 0 {
				newSpans[l] = appendSpan(newSpans[l], new[l], b.at[j], b.at[j]+len(b.tokens[j]))
			}
		}
	}

	shared, total, kept := 0, 0, 0
	for _, side := range []struct {
		spans         [][]span
		kept, visible []int
	}{{oldSpans, oldKept, a.visible}, {newSpans, newKept, b.visible}} {
		for l := range side.spans {
			if side.kept[l] > 0 {
				shared += side.kept[l]
				total += side.visible[l]
			}
			if 3*side.kept[l] < side.visible[l] {
				side.spans[l] = nil
				continue
			}
			kept += side.kept[l]
		}
	}
	if 2*shared < total {
		return nil, nil, 0
	}
	return oldSpans, newSpans, kept
}

// trimSpans takes the space off either end of every mark that is not space
// alone: the space beside a changed word is only where a line break was or
// will be, and a mark reaching into it draws a change nobody made.
func trimSpans(spans [][]span, lines []string) {
	for l := range spans {
		for k, s := range spans[l] {
			text := lines[l][s.from:s.to]
			if trimmed := strings.TrimLeftFunc(text, unicode.IsSpace); trimmed != "" {
				s.from += len(text) - len(trimmed)
				s.to -= len(trimmed) - len(strings.TrimRightFunc(trimmed, unicode.IsSpace))
			}
			spans[l][k] = s
		}
	}
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
//
// A Chinese or Japanese character is a token of its own as well, because
// those scripts are written without spaces and a run of their letters is not
// a word but a clause: taken whole, a sentence that changed two words came
// out as one long piece replaced. A single character is close to the right
// unit there, since a kanji carries its meaning alone, and the trouble
// characters cause in a script with spaces — count and amount sharing "ount"
// — is caught by the cutoff in emphasis when it happens.
func tokenize(s string) []string {
	var tokens []string
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		end := size
		switch {
		case standsAlone(r):
			// One character, already measured.
		case isWord(r):
			for end < len(s) {
				r, n := utf8.DecodeRuneInString(s[end:])
				if !isWord(r) || standsAlone(r) {
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

// standsAlone reports whether r is a character of a script written without
// spaces, which tokenize makes a token of its own. Korean is not among them,
// since it puts spaces between its words. The long-vowel mark ー and the
// closing mark 〆 are listed by hand: Unicode files them under no script,
// though they appear only inside Japanese words.
func standsAlone(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) || r == 'ー' || r == '〆'
}

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
//
// Every other control character is drawn in caret notation, the way cat -v
// and vim draw one — ^M for a carriage return, ^[ for an escape, ^? for a
// delete — because printed as it is, it is an instruction to the terminal and
// not a character: a carriage return writes the rest of the line over its own
// number, a backspace eats the character before it, and an escape sequence
// out of a file of colored log output restyles everything after it.
//
// A tab is ordinary content and is drawn as the spaces it stands for, by
// expandTabs, because a changed line is painted as a band and a tab only
// moves the cursor: the cells it passes over are never written, so they keep
// the terminal's background and a line indented with tabs came out with a
// hole in its band where the indentation was.
func display(line string) string {
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	return expandTabs(caret(line))
}

// tabWidth is the column a tab stops at a multiple of, counted from the start
// of the line's text rather than of the screen.
//
// Four, not the terminal's eight. Indentation is what tabs are nearly always
// for, and eight columns a level pushes a nested line toward the edge, where
// it wraps and the rows it wraps onto are where a band stops short. Counting
// from the start of the text rather than the screen keeps a line's own
// alignment whatever width the number column is.
//
// Known limitation: a file that aligns text in the middle of a line with tabs
// on the assumption that they stop every eight columns, as a Makefile or a
// table in a comment may, is drawn with that alignment off. Go's own
// formatting aligns with spaces and indents with tabs, so a Go file reads
// correctly at any width.
const tabWidth = 4

// expandTabs replaces every tab with the spaces up to the next tab stop.
//
// Columns are counted by how wide each character is drawn, so a Chinese or
// Japanese character before a tab counts two, as a terminal draws it. A byte
// that is not valid UTF-8 counts one and is copied as it was. Known
// limitation: a character whose width terminals disagree about — some emoji,
// some symbols of ambiguous width — can put the stops after it a column off,
// which is the same disagreement a terminal's own tab stops are subject to.
func expandTabs(line string) string {
	if !strings.Contains(line, "\t") {
		return line
	}
	var b strings.Builder
	column := 0
	for i := 0; i < len(line); {
		r, size := utf8.DecodeRuneInString(line[i:])
		switch {
		case r == '\t':
			spaces := tabWidth - column%tabWidth
			b.WriteString(strings.Repeat(" ", spaces))
			column += spaces
		case r == utf8.RuneError && size == 1:
			b.WriteByte(line[i])
			column++
		default:
			b.WriteString(line[i : i+size])
			column += runewidth.RuneWidth(r)
		}
		i += size
	}
	return b.String()
}

// caret draws every control character but a tab in caret notation, for the
// reason display gives.
func caret(line string) string {
	if !strings.ContainsFunc(line, isControl) {
		return line
	}
	// Byte by byte, since every control character is a single byte below
	// 0x80 and no byte of a longer character is: the rest of the line is
	// copied as it was, invalid UTF-8 included, where decoding it would have
	// turned a stray byte into a replacement character.
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case !isControl(rune(c)):
			b.WriteByte(c)
		case c == 0x7f:
			b.WriteString("^?")
		default:
			b.WriteByte('^')
			b.WriteByte(c + '@')
		}
	}
	return b.String()
}

// isControl reports whether r is one of the control characters display draws
// in caret notation: the C0 set and delete, except tab.
func isControl(r rune) bool { return (r < 0x20 && r != '\t') || r == 0x7f }

// Added draws the first limit lines of text as lines put into a file that held
// nothing, the way Render draws an inserted line: numbered from 1, marked +,
// and in the color of an addition. Lines are read the way cat, vim and wc
// read them: a final newline ends the last line rather than starting an empty
// one, so what is drawn is what the user sees on opening the file. Nothing is
// said about the lines past the limit, since the caller knows how it wants
// the rest accounted for; Lines counts them by the same rule.
//
// Known limitation: a text that ends with a newline and one that does not are
// drawn exactly alike, so this does not show whether the file will end with
// one. Render's "(no newline at end of file)" is not used here on purpose. It
// belongs to the last line, which a preview cut at limit draws only for short
// files, so it would appear for those and never for long ones; and drawing the
// end of the file whatever its length would spend a row of every preview on
// one byte.
func Added(text string, limit int) string {
	lines := splitLines(text)
	shown := lines[:min(limit, len(lines))]
	width := len(fmt.Sprint(len(shown)))

	var rows []string
	for i, line := range shown {
		number := fmt.Sprintf("  %*d", width, i+1)
		rows = append(rows, tools.AddedMargin(number+" ")+tools.Added("+ "+display(line)))
	}
	return strings.Join(rows, "\n")
}

// Lines counts the rows Added draws for text: what wc -l counts, plus a last
// line that has no newline.
func Lines(text string) int { return len(splitLines(text)) }
