package terminal

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests feed the screen bytes directly, with no program and no PTY,
// which is what keeping the emulator behind screen buys: what a sequence of
// bytes draws can be checked on its own.

func feed(s *screen, text string) { s.feed([]byte(text)) }

// fill writes n numbered lines and leaves the cursor on the last of them, so
// that a screen n rows tall has something on every row. A snapshot is the
// lines a program drew, so a test about how many rows there are has to put a
// line on each of them.
func fill(s *screen, n int) {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = "l" + strconv.Itoa(i+1)
	}
	feed(s, strings.Join(lines, "\r\n"))
}

// history is the scrollback part of a snapshot tall enough to hold all of it.
func history(s *screen) []string {
	snap := s.snapshot(1_000_000, 0)
	return snap.Rows[:max(0, -snap.FirstRow)]
}

// The default snapshot is the visible screen, which is the lines the program
// drew and not the fixed-size grid they sit in: a screen is blank wherever
// nothing was written, and the blank rows under the last line are padding that
// no caller wants. text says the same lines, joined.
func TestSnapshotIsTheLinesAndNotTheGrid(t *testing.T) {
	s := newScreen(20, 5, DefaultScrollback, nil)
	feed(s, "hi\r\nthere")
	snap := s.snapshot(0, 0)
	if want := []string{"hi", "there"}; !reflect.DeepEqual(snap.Rows, want) {
		t.Fatalf("rows = %q, want %q", snap.Rows, want)
	}
	if snap.Size != (Size{20, 5}) || snap.FirstRow != 0 || snap.Total != 2 {
		t.Fatalf("snapshot = %+v, want a 20x5 screen holding two lines from row 0", snap)
	}
	if want := (Cursor{Row: 1, Col: 5, Visible: true}); snap.Cursor != want {
		t.Fatalf("cursor = %+v, want %+v", snap.Cursor, want)
	}
	if got := s.text(); got != "hi\nthere" {
		t.Fatalf("text = %q, want %q", got, "hi\nthere")
	}
}

func TestEraseAndHomeLeaveOnlyWhatFollows(t *testing.T) {
	s := newScreen(20, 5, DefaultScrollback, nil)
	feed(s, "garbage\r\nmore garbage")
	feed(s, "\x1b[2J\x1b[Hhello")
	if got := s.text(); got != "hello" {
		t.Fatalf("text = %q, want %q", got, "hello")
	}
}

// A screen is the end state of everything drawn on it, not the bytes in the
// order they arrived, which is the whole reason an emulator is used rather
// than the program's output.
func TestCursorMovementReconstructsTheFinalScreen(t *testing.T) {
	s := newScreen(20, 5, DefaultScrollback, nil)
	feed(s, "\x1b[3;5HX")
	feed(s, "\x1b[1;1HTop")
	feed(s, "\x1b[3;1H\x1b[2CY")
	feed(s, "\x1b[1;1H\x1b[K")
	rows := s.snapshot(0, 0).Rows
	if rows[0] != "" || rows[2] != "  Y X" {
		t.Fatalf("rows = %q, want an empty first row and %q on the third", rows, "  Y X")
	}
}

// A progress bar redraws one line with a carriage return, and only its last
// state is on the screen.
func TestCarriageReturnOverwrites(t *testing.T) {
	s := newScreen(20, 3, DefaultScrollback, nil)
	feed(s, "progress 10%\rprogress 99%")
	if got := s.snapshot(0, 0).Rows[0]; got != "progress 99%" {
		t.Fatalf("row = %q, want %q", got, "progress 99%")
	}
}

// The PTY hands output over in whatever pieces it likes, and a character split
// between two reads still has to arrive whole.
func TestUTF8SplitAcrossReads(t *testing.T) {
	s := newScreen(20, 3, DefaultScrollback, nil)
	data := []byte("héllo ✓")
	for i := range data {
		s.feed(data[i : i+1])
	}
	if got := s.snapshot(0, 0).Rows[0]; got != "héllo ✓" {
		t.Fatalf("row = %q, want %q", got, "héllo ✓")
	}
}

// A wide character takes two columns and is written once.
func TestWideCharacters(t *testing.T) {
	s := newScreen(10, 2, DefaultScrollback, nil)
	feed(s, "漢a")
	snap := s.snapshot(0, 0)
	if snap.Rows[0] != "漢a" {
		t.Fatalf("row = %q, want %q", snap.Rows[0], "漢a")
	}
	if snap.Cursor.Col != 3 {
		t.Fatalf("cursor column = %d, want 3", snap.Cursor.Col)
	}
}

// Leaving the alternate screen puts back the screen as it was before, cursor
// and all, which is what a person sees when they quit vim.
func TestLeavingTheAlternateScreenRestoresTheMainOne(t *testing.T) {
	s := newScreen(20, 5, DefaultScrollback, nil)
	feed(s, "$ vim\r\n")
	before := s.snapshot(0, 0)
	feed(s, "\x1b[?1049h\x1b[H~\r\n~\r\nVIM STUFF")
	alt := s.snapshot(0, 0)
	if !alt.AltScreen || alt.Rows[0] != "~" || alt.Rows[2] != "VIM STUFF" {
		t.Fatalf("on the alternate screen = %+v", alt)
	}
	feed(s, "\x1b[?1049l")
	after := s.snapshot(0, 0)
	if after.AltScreen || !reflect.DeepEqual(after.Rows, before.Rows) || after.Cursor != before.Cursor {
		t.Fatalf("after leaving it = %+v, want it as it was: %+v", after, before)
	}
}

// A resize changes the size a snapshot reports and how much of a line fits on
// a row. It is read on a screen written full, since the rows of an empty one
// are blank padding and a snapshot leaves those out whatever the size is.
func TestResizeChangesTheSnapshot(t *testing.T) {
	s := newScreen(80, 24, DefaultScrollback, nil)
	s.resize(120, 40)
	fill(s, 40)
	snap := s.snapshot(0, 0)
	if snap.Size != (Size{120, 40}) || len(snap.Rows) != 40 {
		t.Fatalf("size = %+v with %d rows, want 120x40 with 40", snap.Size, len(snap.Rows))
	}
	s.resize(10, 3)
	if n := len(s.snapshot(0, 0).Rows); n != 3 {
		t.Fatalf("rows = %d, want 3", n)
	}
}

// Attributes are the emulator's business and never the screen's: the text a
// styled run drew is on the screen, and nothing of how it was styled is.
func TestAttributesLeaveOnlyTheirText(t *testing.T) {
	s := newScreen(20, 2, DefaultScrollback, nil)
	feed(s, "a\x1b[1;31mRED\x1b[0mb")
	if got, want := s.rowsLocked()[0], "aREDb"; got != want {
		t.Fatalf("row = %q, want %q", got, want)
	}
}

func TestCursorVisibilityIsTracked(t *testing.T) {
	s := newScreen(20, 2, DefaultScrollback, nil)
	feed(s, "\x1b[?25l")
	if s.snapshot(0, 0).Cursor.Visible {
		t.Fatal("the cursor was hidden and is reported visible")
	}
	feed(s, "\x1b[?25h")
	if !s.snapshot(0, 0).Cursor.Visible {
		t.Fatal("the cursor was shown again and is reported hidden")
	}
}

// The cursor-key mode is tracked because Key reads it to decide how to send
// an arrow.
func TestTitleAndCursorKeyModeAreTracked(t *testing.T) {
	s := newScreen(20, 2, DefaultScrollback, nil)
	feed(s, "\x1b]2;my title\x07\x1b[?1h")
	if title := s.snapshot(0, 0).Title; title != "my title" || !s.applicationCursorKeys() {
		t.Fatalf("title = %q, application cursor keys = %v, want %q and true", title, s.applicationCursorKeys(), "my title")
	}
	feed(s, "\x1b[?1l")
	if s.applicationCursorKeys() {
		t.Fatal("application cursor keys were turned off and are still reported on")
	}
}

// A program asking where the cursor is gets an answer. Without the goroutine
// draining the emulator's replies, the feed below would never return.
func TestCursorPositionQueryIsAnswered(t *testing.T) {
	var mu sync.Mutex
	var replies []string
	s := newScreen(20, 5, DefaultScrollback, func(b []byte) {
		mu.Lock()
		replies = append(replies, string(b))
		mu.Unlock()
	})
	defer s.close()
	feed(s, "\x1b[2;3H\x1b[6n")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := append([]string(nil), replies...)
		mu.Unlock()
		if len(got) > 0 {
			if got[0] != "\x1b[2;3R" {
				t.Fatalf("reply = %q, want %q", got, "\x1b[2;3R")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the cursor position query was never answered")
}

// "$ " is found with its trailing space, which only the unstripped rows still
// have, and text that never appears runs out the context.
func TestWaitAndContains(t *testing.T) {
	s := newScreen(20, 3, DefaultScrollback, nil)
	go func() {
		time.Sleep(50 * time.Millisecond)
		feed(s, "$ ")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.wait(ctx, func(sc *screen) bool { return sc.containsLocked("$ ", nil) }); err != nil {
		t.Fatalf("waiting for the prompt = %v, want it found", err)
	}
	short, cancelShort := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelShort()
	err := s.wait(short, func(sc *screen) bool { return sc.containsLocked("nope", nil) })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting for text that never appears = %v, want %v", err, context.DeadlineExceeded)
	}
}

// What scrollback holds is the emulator's decision. These tests pin down the
// decisions a caller reading history relies on.

func TestLineFeedAtTheBottomFeedsScrollback(t *testing.T) {
	s := newScreen(10, 3, DefaultScrollback, nil)
	feed(s, "l1\r\nl2\r\nl3\r\nl4\r\nl5")
	snap := s.snapshot(10, 0)
	if !reflect.DeepEqual(snap.Rows, []string{"l1", "l2", "l3", "l4", "l5"}) ||
		snap.FirstRow != -2 || snap.Total != 5 || snap.Truncated {
		t.Fatalf("snapshot = %+v", snap)
	}
	if got := history(s); !reflect.DeepEqual(got, []string{"l1", "l2"}) {
		t.Fatalf("history = %q, want %q", got, []string{"l1", "l2"})
	}
	if got := s.snapshot(0, 0).Rows; !reflect.DeepEqual(got, []string{"l3", "l4", "l5"}) {
		t.Fatalf("rows = %q, want %q", got, []string{"l3", "l4", "l5"})
	}
}

// Scrolling up and deleting the top line both push a line into scrollback;
// deleting a line anywhere else does not, since it has not left the screen.
func TestScrollUpAndDeleteLineAtTheTopFeedScrollback(t *testing.T) {
	s := newScreen(10, 3, DefaultScrollback, nil)
	feed(s, "a\r\nb\r\nc")
	feed(s, "\x1b[2S")
	if got := history(s); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("history after scrolling up = %q, want %q", got, []string{"a", "b"})
	}
	if got := s.snapshot(0, 0).Rows; !reflect.DeepEqual(got, []string{"c"}) {
		t.Fatalf("rows = %q, want %q", got, []string{"c"})
	}
	feed(s, "\x1b[1;1H\x1b[M")
	if got := history(s); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("history after deleting the top line = %q, want %q", got, []string{"a", "b", "c"})
	}
	feed(s, "x\r\ny\x1b[2;1H\x1b[M")
	if got := history(s); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("history after deleting a lower line = %q, want it unchanged", got)
	}
}

func TestScrollDownFeedsNothing(t *testing.T) {
	s := newScreen(10, 3, DefaultScrollback, nil)
	feed(s, "a\r\nb\r\nc\x1b[1T")
	if got := s.snapshot(0, 0).Rows; !reflect.DeepEqual(got, []string{"", "a", "b"}) {
		t.Fatalf("rows = %q, want %q", got, []string{"", "a", "b"})
	}
	if got := history(s); len(got) != 0 {
		t.Fatalf("history = %q, want none", got)
	}
}

// Erasing the screen keeps what was on it, minus the blank lines, and erasing
// the scrollback empties it. clear sends both, which is why it leaves nothing.
func TestEraseKeepsTheScreenAndEraseScrollbackEmptiesIt(t *testing.T) {
	s := newScreen(10, 4, DefaultScrollback, nil)
	feed(s, "a\r\n\r\nb")
	feed(s, "\x1b[2J")
	if got := history(s); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("history after erasing the screen = %q, want %q", got, []string{"a", "b"})
	}
	feed(s, "\x1b[3J")
	if got := history(s); len(got) != 0 {
		t.Fatalf("history after erasing the scrollback = %q, want none", got)
	}
}

// What a full-screen program draws never becomes history, and the history
// there was stays readable while it runs.
func TestTheAlternateScreenFeedsNothing(t *testing.T) {
	s := newScreen(10, 2, DefaultScrollback, nil)
	feed(s, "a\r\nb\r\nc")
	feed(s, "\x1b[?1049h1\r\n2\r\n3\r\n4\x1b[2J")
	if got := history(s); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("history on the alternate screen = %q, want %q", got, []string{"a"})
	}
	feed(s, "\x1b[?1049l")
	if got := history(s); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("history after leaving it = %q, want %q", got, []string{"a"})
	}
}

// Scrollback keeps the newest lines it has room for.
func TestScrollbackKeepsTheNewestLines(t *testing.T) {
	s := newScreen(10, 2, 3, nil)
	for i := 0; i < 10; i++ {
		if i > 0 {
			feed(s, "\r\n")
		}
		feed(s, string(rune('0'+i)))
	}
	if got := history(s); !reflect.DeepEqual(got, []string{"5", "6", "7"}) {
		t.Fatalf("history = %q, want %q", got, []string{"5", "6", "7"})
	}
	if got := s.snapshot(3, 0).Rows; !reflect.DeepEqual(got, []string{"7", "8", "9"}) {
		t.Fatalf("bottom three = %q, want %q", got, []string{"7", "8", "9"})
	}
}

func TestScrollbackKeepsWideCharacters(t *testing.T) {
	s := newScreen(10, 1, DefaultScrollback, nil)
	feed(s, "漢字ok\r\n")
	if got := history(s); !reflect.DeepEqual(got, []string{"漢字ok"}) {
		t.Fatalf("history = %q, want %q", got, []string{"漢字ok"})
	}
}

// The budget keeps the newest lines, cuts a newest line that cannot fit on its
// own rather than returning nothing, and counts characters rather than bytes.
func TestTailLines(t *testing.T) {
	lines := []string{"aaa", "bb", "c"}
	cases := []struct {
		lines     []string
		last      int
		maxChars  int
		want      []string
		truncated bool
	}{
		{lines, 2, 0, []string{"bb", "c"}, false},
		{lines, 10, 4, []string{"bb", "c"}, true},
		{lines, 10, 8, []string{"aaa", "bb", "c"}, false},
		{[]string{"abcdef"}, 1, 4, []string{"abc…"}, true},
		{[]string{"abcdef"}, 1, 1, []string{"…"}, true},
		{[]string{"ñañaña"}, 1, 3, []string{"ña…"}, true},
	}
	for _, c := range cases {
		got, truncated := tailLines(c.lines, c.last, c.maxChars)
		if !reflect.DeepEqual(got, c.want) || truncated != c.truncated {
			t.Errorf("tailLines(%q, %d, %d) = %q, %v, want %q, %v",
				c.lines, c.last, c.maxChars, got, truncated, c.want, c.truncated)
		}
	}
}

// Lines are counted up from the bottom row of the screen, so asking for fewer
// than the height gives the bottom of the screen and asking for more reaches
// into scrollback, and FirstRow always maps the cursor onto the rows returned.
func TestSnapshotCountsUpFromTheBottomOfTheScreen(t *testing.T) {
	s := newScreen(10, 4, DefaultScrollback, nil)
	// The screen ends up holding c, d, e and the prompt; a and b scrolled off.
	feed(s, "a\r\nb\r\nc\r\nd\r\ne\r\n$ ")
	full := s.snapshot(0, 0)
	if !reflect.DeepEqual(full.Rows, []string{"c", "d", "e", "$"}) || full.FirstRow != 0 || full.Total != 6 {
		t.Fatalf("default = %+v", full)
	}
	two := s.snapshot(2, 0)
	if !reflect.DeepEqual(two.Rows, []string{"e", "$"}) || two.FirstRow != 2 {
		t.Fatalf("bottom two = %+v", two)
	}
	all := s.snapshot(100, 0)
	if !reflect.DeepEqual(all.Rows, []string{"a", "b", "c", "d", "e", "$"}) || all.FirstRow != -2 {
		t.Fatalf("everything = %+v", all)
	}
	if got := all.Rows[all.Cursor.Row-all.FirstRow]; got != "$" {
		t.Fatalf("the cursor row maps to %q, want %q", got, "$")
	}
	capped := s.snapshot(100, 5)
	if !reflect.DeepEqual(capped.Rows, []string{"d", "e", "$"}) || !capped.Truncated || capped.FirstRow != 1 {
		t.Fatalf("capped = %+v", capped)
	}
}

// The blank rows under the last line are the grid padding itself out rather
// than anything a program drew, so they are neither returned nor counted, and
// a request for more lines than the screen holds is answered from scrollback.
// Counting them was the bug: the rows a window of ten covered on a screen
// holding one were nine of padding, which left the one real line above the
// window and the whole read empty.
func TestTrailingBlankRowsAreNotLines(t *testing.T) {
	s := newScreen(10, 4, DefaultScrollback, nil)
	feed(s, "a\r\nb\r\nc\r\nd\r\ne")
	feed(s, "\x1b[2J\x1b[Htop")
	if snap := s.snapshot(0, 0); !reflect.DeepEqual(snap.Rows, []string{"top"}) || snap.FirstRow != 0 || snap.Total != 6 {
		t.Fatalf("a screen holding one line = %+v, want that line alone out of six", snap)
	}
	snap := s.snapshot(3, 0)
	if !reflect.DeepEqual(snap.Rows, []string{"d", "e", "top"}) || snap.FirstRow != -2 {
		t.Fatalf("the last three lines = %+v, want two out of scrollback and then the screen", snap)
	}
}
