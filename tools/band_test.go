package tools

import (
	"regexp"
	"strings"
	"testing"

	"github.com/fatih/color"
)

// atDepth draws diffs as a terminal of the given depth would, for one test.
func atDepth(t *testing.T, d colorDepth) {
	t.Helper()
	previous := diffDepth
	diffDepth = d
	t.Cleanup(func() { diffDepth = previous })
}

func TestDepthIsReadFromWhatTheTerminalSays(t *testing.T) {
	for _, c := range []struct {
		colorterm, term string
		want            colorDepth
	}{
		{"truecolor", "xterm-256color", depthTrue},
		{"24bit", "xterm", depthTrue},
		{"TrueColor", "", depthTrue},
		{"", "xterm-direct", depthTrue},
		{"", "tmux-256color", depth256},
		{"", "xterm-256color", depth256},
		{"", "xterm", depth16},
		{"", "linux", depth16},
		{"", "", depth16},
		{"yes", "screen", depth16},
	} {
		if got := depthFrom(c.colorterm, c.term); got != c.want {
			t.Errorf("depthFrom(%q, %q) = %d, want %d", c.colorterm, c.term, got, c.want)
		}
	}
}

// plainColor forces color off for one test.
func plainColor(t *testing.T) {
	t.Helper()
	previous := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = previous })
}

var sgr = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// bandRow is a changed row the way the diff draws one: a margin, then the
// line in pieces, one of them emphasized.
func bandRow() []string {
	return []string{
		RemovedMargin("  1 ") + Removed("- timeout: ") + RemovedEmphasis("30") + Removed(" seconds"),
		AddedMargin("  1 ") + Added("+ timeout: ") + AddedEmphasis("120") + Added(" seconds"),
	}
}

// The rule that lets the band use colors written as numbers: every piece of
// text on it sets its foreground and its background both, so no cell is left
// with the theme's color beside one of agi's. A sequence that resets is the
// only kind allowed to set neither.
func TestEveryPieceOfABandSetsBothColors(t *testing.T) {
	withColor(t)
	for _, d := range []colorDepth{depth256, depthTrue} {
		atDepth(t, d)
		for _, row := range bandRow() {
			for _, m := range sgr.FindAllStringSubmatch(row, -1) {
				params := m[1]
				if strings.HasPrefix(params, "0") {
					continue
				}
				fg := strings.HasPrefix(params, "38;") || strings.Contains(params, ";38;")
				bg := strings.Contains(params, "48;")
				// The sequence that opens the row and is erased in sets
				// only the background, and no text is written in it.
				if bg && !fg && strings.Contains(row, m[0]+"\x1b[K") {
					continue
				}
				if !fg || !bg {
					t.Errorf("depth %d: %q in row %q does not set both colors", d, m[0], row)
				}
			}
		}
	}
}

// A background left on at the end of a row is drawn into the next one when
// the screen scrolls, so every row ends switched off.
func TestABandRowEndsReset(t *testing.T) {
	withColor(t)
	for _, d := range []colorDepth{depth16, depth256, depthTrue} {
		atDepth(t, d)
		for _, row := range bandRow() {
			ms := sgr.FindAllStringSubmatchIndex(row, -1)
			last := ms[len(ms)-1]
			if last[1] != len(row) || !strings.HasPrefix(row[last[2]:last[3]], "0") {
				t.Errorf("depth %d: row %q does not end with a reset", d, row)
			}
		}
	}
}

// The row is erased in the band's color before anything is written on it,
// which is what carries the band to the right edge; a terminal of sixteen
// colors gets no band and nothing erased.
func TestTheMarginPaintsTheRow(t *testing.T) {
	withColor(t)
	atDepth(t, depthTrue)
	if got := AddedMargin("  1 "); !strings.HasPrefix(got, "\x1b[48;2;28;68;40m\x1b[K") {
		t.Errorf("AddedMargin = %q, want it to erase the row in the band's color first", got)
	}
	atDepth(t, depth256)
	if got := RemovedMargin("  1 "); !strings.HasPrefix(got, "\x1b[48;5;52m\x1b[K") {
		t.Errorf("RemovedMargin = %q, want it to erase the row in the band's color first", got)
	}
	atDepth(t, depth16)
	for _, got := range []string{AddedMargin("  1 "), RemovedMargin("  1 ")} {
		if strings.Contains(got, "\x1b[K") || strings.Contains(got, "48;") {
			t.Errorf("margin = %q, want no band on sixteen colors", got)
		}
	}
}

// The changed words are a different shade from the rest of the line, or they
// would not be marked at all.
func TestEmphasisIsAnotherShadeOfTheBand(t *testing.T) {
	withColor(t)
	for _, d := range []colorDepth{depth256, depthTrue} {
		atDepth(t, d)
		if Added("x") == AddedEmphasis("x") || Removed("x") == RemovedEmphasis("x") {
			t.Errorf("depth %d: the emphasis is drawn the same as the line", d)
		}
	}
}

// With color off a band is nothing at all, whatever the terminal could draw.
func TestABandIsPlainWithColorOff(t *testing.T) {
	plainColor(t)
	for _, d := range []colorDepth{depth16, depth256, depthTrue} {
		atDepth(t, d)
		want := []string{"  1 - timeout: 30 seconds", "  1 + timeout: 120 seconds"}
		for i, row := range bandRow() {
			if row != want[i] {
				t.Errorf("depth %d: row = %q, want %q", d, row, want[i])
			}
		}
	}
}
