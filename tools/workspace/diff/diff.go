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
	// maxWordEdits is the same bound for the word diff inside a line, and is
	// far smaller. A line that changed in more places than this is not worth
	// marking — the cutoff in emphasis would usually decline it anyway — and
	// giving up early is what keeps a long minified line cheap: the search is
	// the expensive part, and it runs for every pair of lines compared.
	maxWordEdits = 100
	// pairLookahead is how many lines put in a line taken out is compared
	// with, past the last pair, in looking for the line it became. A handful
	// covers the lines an edit adds between old ones — a block's opening
	// line, a comment, a new statement — and keeps the search a few word
	// diffs a line however large the run.
	pairLookahead = 8
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
// of line numbers, the lines taken out marked - in red, the lines put in
// marked + in green, and a few unchanged lines around them dimmed. Where two
// changes are far enough apart that the lines between them say nothing, those
// lines are left out and a ⋮ stands in their place.
//
// A line taken out carries its number in the old file, and every other line
// its number in the new one, so each number is where that line is or was.
//
// Everything past the cap is cut off before anything else is worked out, so
// a rewritten file costs what its first maxDiffRows lines cost: the width of
// the number column is the widest number shown, and only rows shown are
// paired, since the word diffs that pairing runs are the expensive part of
// drawing a diff. A line taken out whose partner the cap hides is left plain,
// rather than marked against a line the reader cannot see.
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

	marks := pairMarks(ops, a, b)

	var rows []string
	for i, o := range ops {
		if o == nil {
			rows = append(rows, tools.Detail(gutter[:len(gutter)-1]+"⋮"))
			continue
		}

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

// pairMarks works out, for every row of the diff, which parts of its line
// changed, and leaves a row it has nothing to say about with none.
func pairMarks(ops []*op, a, b []string) [][]span {
	marks := make([][]span, len(ops))
	for _, p := range pairLines(ops, a, b) {
		marks[p.old], marks[p.new] = p.oldSpans, p.newSpans
	}
	return marks
}

// pairing is a line taken out matched with the line put in that it became:
// the two rows' indexes into ops, and the marks on each. Either side's marks
// may be empty — a line that only grew lost nothing — and the two are still a
// pair.
type pairing struct {
	old, new           int
	oldSpans, newSpans []span
}

// pairLines matches the lines taken out with the lines put in.
//
// A run of changed lines is the lines taken out followed by the lines put in.
// Each line taken out, in order, is paired with the first line put in after
// the last pair that it is similar enough to — similar enough being whatever
// emphasis agrees to mark — and a line with no such partner is left plain.
// Order is kept, because an edit nearly always keeps its lines in the order
// they were in, but a line put in between them is skipped over: code wrapped
// in a new block gains a line above it, and pairing purely by position
// matched every line of the old code with the line before its own.
//
// The search looks no further than pairLookahead lines ahead, so a run the
// size of a rewritten file costs a few comparisons a line rather than one for
// every pair of lines in it, and each line is cut into words once however
// many lines it is compared with.
func pairLines(ops []*op, a, b []string) []pairing {
	var pairs []pairing
	words := make([]*wordLine, len(ops))
	wordsOf := func(i int) *wordLine {
		if words[i] == nil {
			if ops[i].kind == opDelete {
				words[i] = newWordLine(display(a[ops[i].ai]))
			} else {
				words[i] = newWordLine(display(b[ops[i].bi]))
			}
		}
		return words[i]
	}
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
		next := 0
		for _, d := range dels {
			for k := next; k < min(len(ins), next+pairLookahead); k++ {
				n := ins[k]
				if old, new, ok := compareWords(wordsOf(d), wordsOf(n)); ok {
					pairs = append(pairs, pairing{d, n, old, new})
					next = k + 1
					break
				}
			}
		}
	}
	return pairs
}

// emphasis diffs two lines word by word and returns the parts of each that
// changed. It reports false when there is nothing worth marking: the lines
// differ in nothing that shows, or they have too little in common, which
// means either the pairing was wrong or the line was rewritten outright, and
// marking nearly every word of it says less than marking none.
//
// Too little is a similarity below one half, measured as the text the two
// share counted on both sides, over all the text of both — 2·shared/(old+new),
// the ratio Python's difflib uses. It is measured over both lines together
// rather than against the longer one, because an edit that mostly adds text
// keeps the whole of the line it started from and is still plainly the same
// line: against the longer line alone, f(a) growing into f(a, b, c, d) or 日本の
// 女性 into 日本を拠点とする女性 had too little in common to be marked.
//
// Words rather than characters, because characters that happen to match make
// a mess of the marks — count and amount share "ount" — and a person reads a
// change a word at a time anyway.
func emphasis(old, new string) ([]span, []span, bool) {
	return compareWords(newWordLine(old), newWordLine(new))
}

// wordLine is a line cut into the words it is diffed in, with where each word
// starts and how many visible characters the line has, worked out once so a
// line compared with several others is not cut up again for each.
type wordLine struct {
	text    string
	words   []string
	at      []int
	visible int
}

func newWordLine(text string) *wordLine {
	words := tokenize(text)
	return &wordLine{text: text, words: words, at: offsets(words), visible: visible(text)}
}

// compareWords is emphasis over two lines already cut into words.
//
// Before any search it asks whether the two could pass the cutoff at all:
// the text they share is at most the whole of the shorter, so a line less
// than a third the length of the other cannot reach one half, and is
// declined without being diffed. The words the two share at either end are
// then set aside the way diffLines sets aside lines, so two long lines that
// differ in one word cost a walk along them and a search over that word.
func compareWords(old, new *wordLine) ([]span, []span, bool) {
	if 4*min(old.visible, new.visible) < old.visible+new.visible {
		return nil, nil, false
	}
	a, b := old.words, new.words
	shared := 0
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		shared += visible(a[pre])
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		shared += visible(a[len(a)-1-suf])
		suf++
	}
	ops, ok := myers(a[pre:len(a)-suf], b[pre:len(b)-suf], maxWordEdits)
	if !ok {
		return nil, nil, false
	}

	var oldSpans, newSpans []span
	for _, o := range ops {
		i, j := o.ai+pre, o.bi+pre
		switch o.kind {
		case opKeep:
			shared += visible(a[i])
		case opDelete:
			oldSpans = appendSpan(oldSpans, old.text, old.at[i], old.at[i]+len(a[i]))
		case opInsert:
			newSpans = appendSpan(newSpans, new.text, new.at[j], new.at[j]+len(b[j]))
		}
	}
	if len(oldSpans) == 0 && len(newSpans) == 0 {
		return nil, nil, false
	}
	if 4*shared < old.visible+new.visible {
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
// out of a file of colored log output restyles everything after it. A tab is
// the exception, since it is ordinary content and the terminal draws it as
// space.
func display(line string) string {
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
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
// and in the color of an addition. Nothing is said about the lines past the
// limit, since the caller knows how it wants the rest accounted for; Lines
// counts them by the same rule.
func Added(text string, limit int) string {
	lines := splitLines(text)
	shown := lines[:min(limit, len(lines))]
	width := len(fmt.Sprint(len(shown)))
	gutter := "  " + strings.Repeat(" ", width)

	var rows []string
	for i, line := range shown {
		number := tools.Detail(fmt.Sprintf("  %*d", width, i+1))
		rows = append(rows, number+" "+tools.Added("+ "+display(line)))
		if !strings.HasSuffix(line, "\n") {
			rows = append(rows, tools.Detail(gutter+"   (no newline at end of file)"))
		}
	}
	return strings.Join(rows, "\n")
}

// Lines counts the lines of text the way Added draws them: a last line without
// a newline is still a line, and an empty text has none.
func Lines(text string) int { return len(splitLines(text)) }
