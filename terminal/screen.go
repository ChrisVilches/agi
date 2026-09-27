package terminal

import (
	"context"
	"fmt"
	"image/color"
	"io"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// waitTick is the longest a waiter goes without checking its conditions
// again. Output wakes it sooner; this is for a condition that becomes true by
// time passing, which Idle does and nothing written to the screen announces.
const waitTick = 50 * time.Millisecond

// TruncationMarker ends a line that was cut to fit a MaxChars budget, so that
// a cut line cannot be read as the whole of one.
const TruncationMarker = "…"

// Cursor is where the cursor is, in screen coordinates counted from zero.
type Cursor struct {
	Row     int  `json:"row"`
	Col     int  `json:"col"`
	Visible bool `json:"visible"`
}

// Size is the size of a screen in cells.
type Size struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// Snapshot is the bottom lines of scrollback and screen, oldest first, and
// what the screen around them looked like.
//
// Its fields carry JSON tags because the likeliest thing to be done with one
// is to hand it to a model, which reads JSON.
type Snapshot struct {
	// Rows are the lines, oldest first, with trailing spaces trimmed. A
	// snapshot at least as tall as the screen ends with the visible screen,
	// blank rows included.
	Rows []string `json:"rows"`

	// FirstRow is the screen row Rows[0] is on. A negative one is a line of
	// scrollback, so the cursor is at Rows[Cursor.Row-FirstRow] whatever was
	// asked for.
	FirstRow int `json:"first_row"`

	// Total is how many lines there are to ask for: scrollback and the height
	// of the screen together.
	Total int `json:"total"`

	// Truncated says whether MaxChars cut anything. FirstRow describes what
	// was returned and not what was asked for, so it accounts for the cut.
	Truncated bool   `json:"truncated"`
	Cursor    Cursor `json:"cursor"`
	Size      Size   `json:"size"`
	AltScreen bool   `json:"alt_screen"`
	Title     string `json:"title"`
}

// Style is everything about a cell that is not the default. A color is a
// basic name — "red", "brightblue", and "brown" for color 3, which is what the
// emulator calls it — or six hex digits, and an empty one is the terminal's
// default.
type Style struct {
	Fg            string `json:"fg,omitempty"`
	Bg            string `json:"bg,omitempty"`
	Bold          bool   `json:"bold,omitempty"`
	Italics       bool   `json:"italics,omitempty"`
	Underscore    bool   `json:"underscore,omitempty"`
	Strikethrough bool   `json:"strikethrough,omitempty"`
	Reverse       bool   `json:"reverse,omitempty"`
	Blink         bool   `json:"blink,omitempty"`
}

// Span is a run of cells that share one style. Style is embedded so that it
// flattens into the span's JSON rather than nesting under a key of its own.
type Span struct {
	Text string `json:"text"`
	Style
}

// screen is what the program has drawn, fed from the PTY.
//
// It is the only thing in the package that knows about the emulator, which is
// what lets it be tested by feeding it bytes with no program and no PTY
// anywhere. Every method is safe for concurrent use: the reader feeds it while
// callers read it.
//
// mu guards the emulator and the fields its callbacks set. The callbacks run
// inside emu.Write, which feed calls with mu already held, so a callback must
// never take mu itself — it would wait for a lock its own caller holds.
type screen struct {
	mu  sync.Mutex
	emu *vt.Emulator

	// changed is closed and replaced on every change, which wakes every
	// waiter at once without anybody having to keep a list of them.
	changed chan struct{}

	// These are set by the emulator's callbacks, from inside emu.Write.
	cursorVisible bool
	appCursorKeys bool
	title         string

	lastOutput time.Time
}

// newScreen makes a screen that keeps scrollback lines of history. reply is
// handed the emulator's answers to the program's questions — where the cursor
// is, which attributes the terminal has — which have to go back to the program
// through the PTY.
//
// Those answers are why a goroutine is started here. The emulator writes them
// into a synchronous pipe, so the first time a program asks a question the
// write blocks until something reads it — and the write is happening inside
// feed, with the lock held, so a pipe nobody drained would stop the screen
// for good the first time a program asked where the cursor was. close ends
// the goroutine.
func newScreen(cols, rows, scrollback int, reply func([]byte)) *screen {
	s := &screen{
		emu:           vt.NewEmulator(cols, rows),
		changed:       make(chan struct{}),
		cursorVisible: true,
		lastOutput:    time.Now(),
	}
	// The emulator decides what scrollback holds, and it holds only what the
	// normal screen sends it: a line that leaves the top of the scroll region
	// — a line feed at the bottom, a deleted line at the top margin, a scroll
	// up — and the lines that were not blank when the screen was erased.
	// Erasing the scrollback empties it.
	s.emu.SetScrollbackSize(scrollback)
	s.emu.SetCallbacks(vt.Callbacks{
		Title:            func(title string) { s.title = title },
		CursorVisibility: func(visible bool) { s.cursorVisible = visible },
		EnableMode: func(m ansi.Mode) {
			if m == ansi.ModeCursorKeys {
				s.appCursorKeys = true
			}
		},
		DisableMode: func(m ansi.Mode) {
			if m == ansi.ModeCursorKeys {
				s.appCursorKeys = false
			}
		},
	})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := s.emu.Read(buf)
			if n > 0 && reply != nil {
				reply(append([]byte(nil), buf[:n]...))
			}
			if err != nil {
				return
			}
		}
	}()
	return s
}

func (s *screen) broadcastLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// feed hands output from the program to the emulator.
func (s *screen) feed(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.emu.Write(data)
	s.lastOutput = time.Now()
	s.broadcastLocked()
}

// notify wakes every waiter without anything having changed on the screen,
// for a change that happened somewhere else: the program exiting.
func (s *screen) notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broadcastLocked()
}

// close ends the goroutine that relays the emulator's answers.
func (s *screen) close() {
	if pw, ok := s.emu.InputPipe().(*io.PipeWriter); ok {
		_ = pw.Close()
	}
}

func (s *screen) size() Size {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Size{Cols: s.emu.Width(), Rows: s.emu.Height()}
}

func (s *screen) resize(cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emu.Resize(cols, rows)
	s.broadcastLocked()
}

func (s *screen) applicationCursorKeys() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appCursorKeys
}

// renderCells renders n cells as text. A wide character takes two cells and is
// written once, so the cell that holds its second half is skipped; an empty
// cell is a space.
func renderCells(n int, cellAt func(x int) *uv.Cell) string {
	var b strings.Builder
	for x := 0; x < n; x++ {
		c := cellAt(x)
		if c == nil || c.Content == "" {
			b.WriteByte(' ')
			continue
		}
		b.WriteString(c.Content)
		if c.Width > 1 {
			x += c.Width - 1
		}
	}
	return b.String()
}

// rawRowsLocked renders each visible row padded out to the width of the
// screen. The padding is what lets a prompt ending in a space be found; every
// other caller trims it.
func (s *screen) rawRowsLocked() []string {
	width, height := s.emu.Width(), s.emu.Height()
	rows := make([]string, height)
	for y := 0; y < height; y++ {
		rows[y] = renderCells(width, func(x int) *uv.Cell { return s.emu.CellAt(x, y) })
	}
	return rows
}

func rstrip(line string) string { return strings.TrimRightFunc(line, unicode.IsSpace) }

func (s *screen) rowsLocked() []string {
	rows := s.rawRowsLocked()
	for i, r := range rows {
		rows[i] = rstrip(r)
	}
	return rows
}

// snapshot returns the bottom lines lines of scrollback and screen together.
//
// The count starts at the bottom row of the screen and goes up, through the
// visible rows and then into scrollback, because the bottom is where the
// program is now and is what is nearly always wanted. lines of zero or less is
// the height of the screen, so the default is exactly what is visible.
// maxChars above zero caps the joined text, the way tailLines does it.
func (s *screen) snapshot(lines, maxChars int) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	height := s.emu.Height()
	if lines <= 0 {
		lines = height
	}
	screenRows := s.rowsLocked()
	var history []string
	if extra := lines - len(screenRows); extra > 0 {
		if sb := s.emu.Scrollback(); sb != nil {
			all := sb.Lines()
			for _, line := range all[max(0, len(all)-extra):] {
				history = append(history, rstrip(renderCells(len(line), func(x int) *uv.Cell { return &line[x] })))
			}
		}
	}
	picked, truncated := tailLines(append(history, screenRows...), lines, maxChars)
	pos := s.emu.CursorPosition()
	return Snapshot{
		Rows:      picked,
		FirstRow:  height - len(picked),
		Total:     s.emu.ScrollbackLen() + len(screenRows),
		Truncated: truncated,
		Cursor:    Cursor{Row: pos.Y, Col: pos.X, Visible: s.cursorVisible},
		Size:      Size{Cols: s.emu.Width(), Rows: height},
		AltScreen: s.emu.IsAltScreen(),
		Title:     s.title,
	}
}

// text is the visible screen with its trailing blank rows dropped.
func (s *screen) text() string {
	s.mu.Lock()
	rows := s.rowsLocked()
	s.mu.Unlock()
	for len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	return strings.Join(rows, "\n")
}

// tailLines picks the newest last lines, oldest first, and then, when maxChars
// is above zero, bounds them so that strings.Join(result, "\n") is at most
// maxChars characters. It counts runes rather than bytes, since a budget is
// about how much text there is and not how it is encoded.
//
// Older lines go first, for the reason the count starts at the bottom. A
// newest line that does not fit on its own is cut and ends with
// TruncationMarker rather than being dropped, because an empty answer would
// say the screen was empty, which is the one thing that is certainly not true.
func tailLines(lines []string, last, maxChars int) ([]string, bool) {
	if last <= 0 {
		return []string{}, false
	}
	picked := lines[max(0, len(lines)-last):]
	if maxChars <= 0 {
		return append([]string{}, picked...), false
	}
	var kept []string
	used := 0
	for i := len(picked) - 1; i >= 0; i-- {
		line := picked[i]
		cost := utf8.RuneCountInString(line)
		if len(kept) > 0 {
			cost++
		}
		if used+cost <= maxChars {
			kept = append(kept, line)
			used += cost
			continue
		}
		if len(kept) == 0 {
			r := []rune(line)
			kept = append(kept, string(r[:maxChars-1])+TruncationMarker)
		}
		slices.Reverse(kept)
		return kept, true
	}
	slices.Reverse(kept)
	if kept == nil {
		kept = []string{}
	}
	return kept, false
}

// cells returns the visible screen as runs of identically styled text, one
// slice of spans per row, with a trailing run of unstyled blanks dropped.
func (s *screen) cells() [][]Span {
	s.mu.Lock()
	defer s.mu.Unlock()
	width, height := s.emu.Width(), s.emu.Height()
	out := make([][]Span, height)
	for y := 0; y < height; y++ {
		spans := []Span{}
		var current Style
		var text strings.Builder
		flush := func() {
			if text.Len() > 0 {
				spans = append(spans, Span{Text: text.String(), Style: current})
				text.Reset()
			}
		}
		for x := 0; x < width; x++ {
			c := s.emu.CellAt(x, y)
			content, style := " ", Style{}
			if c != nil {
				style = styleOf(&c.Style)
				if c.Content != "" {
					content = c.Content
				}
				if c.Width > 1 {
					x += c.Width - 1
				}
			}
			if text.Len() > 0 && style != current {
				flush()
			}
			current = style
			text.WriteString(content)
		}
		flush()
		if n := len(spans); n > 0 && spans[n-1].Style == (Style{}) {
			spans[n-1].Text = rstrip(spans[n-1].Text)
			if spans[n-1].Text == "" {
				spans = spans[:n-1]
			}
		}
		out[y] = spans
	}
	return out
}

// wait blocks until pred is true, and returns nil, or until ctx is done, and
// returns ctx.Err().
//
// pred runs with the lock held, so it may use only the fields and the
// *Locked helpers; calling anything that takes the lock would wait for
// itself. It is checked again on every change and at least every waitTick.
func (s *screen) wait(ctx context.Context, pred func(*screen) bool) error {
	tick := time.NewTicker(waitTick)
	defer tick.Stop()
	for {
		s.mu.Lock()
		ok := pred(s)
		changed := s.changed
		s.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		case <-tick.C:
		}
	}
}

// containsLocked reports whether needle and pattern are both on the visible
// screen, an empty needle and a nil pattern counting as found. Each is looked
// for in the trimmed rows joined by newlines, so text spanning lines can be
// found, and in each row as it is, padding and all, so a prompt ending in a
// space can be.
func (s *screen) containsLocked(needle string, pattern *regexp.Regexp) bool {
	raw := s.rawRowsLocked()
	trimmed := make([]string, len(raw))
	for i, r := range raw {
		trimmed[i] = rstrip(r)
	}
	texts := append([]string{strings.Join(trimmed, "\n")}, raw...)
	anyMatch := func(match func(string) bool) bool {
		return slices.ContainsFunc(texts, match)
	}
	if needle != "" && !anyMatch(func(t string) bool { return strings.Contains(t, needle) }) {
		return false
	}
	if pattern != nil && !anyMatch(pattern.MatchString) {
		return false
	}
	return true
}

// basicNames are the eight basic colors, as the emulator numbers them.
var basicNames = [8]string{"black", "red", "green", "brown", "blue", "magenta", "cyan", "white"}

// colorName names a color the way Style reports one, and an empty name for
// the terminal's default.
func colorName(c color.Color) string {
	switch v := c.(type) {
	case nil:
		return ""
	case ansi.BasicColor:
		if v < 8 {
			return basicNames[v]
		}
		return "bright" + basicNames[v-8]
	default:
		r, g, b, _ := c.RGBA()
		return fmt.Sprintf("%02x%02x%02x", r>>8, g>>8, b>>8)
	}
}

func styleOf(st *uv.Style) Style {
	return Style{
		Fg:            colorName(st.Fg),
		Bg:            colorName(st.Bg),
		Bold:          st.Attrs&uv.AttrBold != 0,
		Italics:       st.Attrs&uv.AttrItalic != 0,
		Underscore:    st.Underline != uv.UnderlineNone,
		Strikethrough: st.Attrs&uv.AttrStrikethrough != 0,
		Reverse:       st.Attrs&uv.AttrReverse != 0,
		Blink:         st.Attrs&(uv.AttrBlink|uv.AttrRapidBlink) != 0,
	}
}
