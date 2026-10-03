package terminal_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ChrisVilches/agi/terminal"
)

// seq prints 1 to n in the shell and waits for the prompt after it.
func seq(t *testing.T, term *terminal.Terminal, n int) {
	t.Helper()
	feed(t, term, fmt.Sprintf("seq 1 %d\n", n))
	mustWait(t, term, 10, terminal.WaitFor{Regex: re(fmt.Sprintf(`^%d\n\$`, n))})
}

// numbers returns from up to, but not including, to.
func numbers(from, to int) []string {
	var out []string
	for i := from; i < to; i++ {
		out = append(out, strconv.Itoa(i))
	}
	return out
}

func TestTheDefaultIsExactlyTheVisibleScreen(t *testing.T) {
	term := startShell(t, terminal.Options{})
	seq(t, term, 200)
	s := screen(term, 0, 0)
	if len(s.Rows) != 24 || s.FirstRow != 0 || !slices.Equal(s.Rows[22:], []string{"200", "$"}) {
		t.Fatalf("default = %+v, want the 24 visible rows ending in 200 and the prompt", s)
	}
	if !slices.Equal(screen(term, term.Status().Size.Rows, 0).Rows, s.Rows) {
		t.Fatal("asking for the screen's height differs from the default")
	}
}

func TestLinesCountUpFromTheBottomRow(t *testing.T) {
	term := startShell(t, terminal.Options{})
	seq(t, term, 200)
	s := screen(term, 4, 0)
	if !slices.Equal(s.Rows, []string{"198", "199", "200", "$"}) || s.FirstRow != 20 {
		t.Fatalf("bottom four = %+v", s)
	}
	if s.Rows[s.Cursor.Row-s.FirstRow] != "$" {
		t.Fatal("the cursor row does not map onto the rows returned")
	}
}

func TestLinesBeyondTheScreenComeFromScrollback(t *testing.T) {
	term := startShell(t, terminal.Options{})
	seq(t, term, 200)
	visible := screen(term, 0, 0).Rows
	firstVisible := atoi(t, visible[0])
	s := screen(term, 29, 0)
	if !slices.Equal(s.Rows[5:], visible) || !slices.Equal(s.Rows[:5], numbers(firstVisible-5, firstVisible)) {
		t.Fatalf("rows = %q, want the five lines above the screen and then the screen", s.Rows)
	}
	// What scrolled off is the command line and 1 up to the first visible
	// number, and the screen is 24 rows on top of that.
	if s.FirstRow != -5 || s.Total != firstVisible+24 || s.Truncated {
		t.Fatalf("first_row = %d, total = %d, truncated = %v", s.FirstRow, s.Total, s.Truncated)
	}
}

// Nearly all of a 24-row screen holding one command is blank, and a count of
// lines starts under the last of them rather than at the bottom of the grid.
// Counting rows instead answered a request for the last three lines of this
// screen with three blank ones, having left the command, its output and the
// prompt above the window.
func TestTrailingBlankRowsAreNotCounted(t *testing.T) {
	term := startShell(t, terminal.Options{})
	feed(t, term, "echo hi\n")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^hi\n\$`)})
	s := screen(term, 3, 0)
	if !slices.Equal(s.Rows, []string{"$ echo hi", "hi", "$"}) || s.FirstRow != 0 {
		t.Fatalf("bottom three = %+v, want the command, its output and the prompt", s)
	}
	if whole := screen(term, 0, 0); !slices.Equal(whole.Rows, s.Rows) {
		t.Fatalf("the screen = %q and its last three lines = %q, want the same three", whole.Rows, s.Rows)
	}
}

func TestMaxCharsKeepsTheNewestLines(t *testing.T) {
	term := startShell(t, terminal.Options{})
	seq(t, term, 200)
	s := screen(term, 50, 11)
	if !slices.Equal(s.Rows, []string{"199", "200", "$"}) || !s.Truncated || s.FirstRow != 21 {
		t.Fatalf("capped = %+v", s)
	}
}

// A line longer than the screen is wide wraps onto several rows, and a budget
// smaller than the bottom one of them still returns something, cut.
func TestMaxCharsCutsAnOverlongLine(t *testing.T) {
	term := startShell(t, terminal.Options{})
	feed(t, term, "clear; printf 'x%.0s' $(seq 1 300); echo; seq 1 30\n")
	mustWait(t, term, 10, terminal.WaitFor{Regex: re(`^30\n\$`)})
	rows := screen(term, 1000, 0).Rows
	longest := 0
	for i, r := range rows {
		if len(r) > len(rows[longest]) {
			longest = i
		}
	}
	if len(rows[longest]) < 80 || strings.Trim(rows[longest], "x") != "" {
		t.Fatalf("the longest row = %q, want a full row of x", rows[longest])
	}
	s := screen(term, len(rows)-longest, 10)
	if !s.Truncated || len([]rune(strings.Join(s.Rows, "\n"))) > 10 {
		t.Fatalf("capped = %+v, want at most 10 characters and truncated", s)
	}
}

// clear erases the scrollback as well as the screen, so nothing from before it
// can be read back.
func TestClearEmptiesScrollback(t *testing.T) {
	term := startShell(t, terminal.Options{})
	seq(t, term, 100)
	if screen(term, 0, 0).Total <= 24 {
		t.Fatal("there is no scrollback before clear, so the test proves nothing")
	}
	feed(t, term, "clear\n")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^\$\s*$`), Idle: duration(0.2)})
	if total := screen(term, 0, 0).Total; term.Text() != "$" || total != 1 {
		t.Fatalf("after clear the text is %q and the total %d, want the prompt alone and 1", term.Text(), total)
	}
}

func TestTheScrollbackOptionBoundsHistory(t *testing.T) {
	term := startShell(t, terminal.Options{Scrollback: 50})
	seq(t, term, 300)
	s := screen(term, 1000, 0)
	if s.Total != 50+24 || len(s.Rows) != 74 || s.FirstRow != -50 {
		t.Fatalf("total = %d, rows = %d, first_row = %d, want 74, 74 and -50", s.Total, len(s.Rows), s.FirstRow)
	}
	h := history(s)
	if h[len(h)-1] != strconv.Itoa(atoi(t, s.Rows[50])-1) {
		t.Fatalf("history ends with %q and the screen starts with %q, want them adjacent", h[len(h)-1], s.Rows[50])
	}
}

// Nothing vim draws becomes history, however far through a file it scrolls,
// and the history there was stays readable while vim is open.
func TestTheAlternateScreenDoesNotFeedScrollback(t *testing.T) {
	need(t, "vim")
	term := startShell(t, terminal.Options{})
	seq(t, term, 50)
	before := history(screen(term, 1000, 0))
	path := filepath.Join(t.TempDir(), "long.txt")
	var content strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&content, "line%d\n", i)
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	feed(t, term, fmt.Sprintf("vim -u NONE -N -i NONE %s\n", path))
	mustWait(t, term, 10, terminal.WaitFor{Regex: re(`^line0$`), Idle: duration(0.3)})
	feed(t, term, "G")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^line499$`), Idle: duration(0.3)})
	during := screen(term, 1000, 0)
	h := history(during)
	// Typing the long command line may have scrolled a line or two more into
	// history before vim started, so what was there has to be a prefix.
	if len(h) < len(before) || !slices.Equal(h[:len(before)], before) {
		t.Fatal("history changed while vim was open")
	}
	for _, l := range h {
		if strings.HasPrefix(l, "line") {
			t.Fatalf("what vim drew reached history: %q", l)
		}
	}
	if !slices.Equal(during.Rows[len(during.Rows)-24:], screen(term, 0, 0).Rows) {
		t.Fatal("the bottom rows are not vim's screen")
	}
	feed(t, term, ":q!\r")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^\$\s*$`), Idle: duration(0.3)})
	for _, l := range history(screen(term, 1000, 0)) {
		if strings.HasPrefix(l, "line") {
			t.Fatalf("what vim drew is in history after it exited: %q", l)
		}
	}
}

func TestNegativeScreenOptionsMeanTheDefaults(t *testing.T) {
	term := startShell(t, terminal.Options{})
	seq(t, term, 200)
	s := screen(term, -3, -1)
	if !slices.Equal(s.Rows, screen(term, 0, 0).Rows) || len(s.Rows) != 24 || s.Truncated {
		t.Fatalf("negative options = %+v, want the visible screen uncut", s)
	}
}
