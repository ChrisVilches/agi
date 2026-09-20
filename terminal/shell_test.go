package terminal_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ChrisVilches/agi/terminal"
)

func TestEchoHello(t *testing.T) {
	term := startShell(t, terminal.Options{})
	feed(t, term, "echo hello\n")
	// The next prompt, not only the output: the prompt is the shell saying it
	// has finished with the line.
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^hello\n\$`)})
	s := screen(term, 0, 0)
	if len(s.Rows) != 24 {
		t.Fatalf("rows = %d, want the 24 of the whole screen", len(s.Rows))
	}
	if want := []string{"$ echo hello", "hello", "$"}; !slices.Equal(s.Rows[:3], want) {
		t.Fatalf("rows = %q, want %q", s.Rows[:3], want)
	}
	if want := (terminal.Cursor{Row: 2, Col: 2, Visible: true}); s.Cursor != want {
		t.Fatalf("cursor = %+v, want %+v", s.Cursor, want)
	}
}

func TestPythonREPL(t *testing.T) {
	need(t, "python3")
	term := startShell(t, terminal.Options{})
	feed(t, term, "python3 -q\n")
	mustWait(t, term, 10, terminal.WaitFor{Text: ">>> "})
	feed(t, term, `print("hello" + "!")`+"\n")
	// Waiting for the prompt after the output matters here more than anywhere:
	// readline can discard keys typed before it set the terminal up.
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^hello!\n>>>`)})
	key(t, term, "CTRL_D")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^\$\s*$`)})
}

// Ctrl-C is typed, not signalled, and the kernel interrupts the foreground job
// with it: 130 is 128 plus SIGINT.
func TestCtrlCInterruptsTheForegroundJob(t *testing.T) {
	term := startShell(t, terminal.Options{})
	feed(t, term, "sleep 100\n")
	wait(term, 5, terminal.WaitFor{Idle: duration(0.3)})
	feed(t, term, "\x03")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^\^C$`)})
	feed(t, term, "echo rc=$?\n")
	mustWait(t, term, 5, terminal.WaitFor{Text: "rc=130"})
}

func TestTheCtrlCKeyInterruptsToo(t *testing.T) {
	term := startShell(t, terminal.Options{})
	feed(t, term, "sleep 100\n")
	wait(term, 5, terminal.WaitFor{Idle: duration(0.3)})
	key(t, term, "CTRL_C")
	feed(t, term, "echo back\n")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^back$`)})
}

func TestUpRecallsHistory(t *testing.T) {
	term := startShell(t, terminal.Options{})
	feed(t, term, "echo first\n")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^first$`)})
	key(t, term, "UP", "ENTER")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^first\n\$ echo first\nfirst$`)})
}

// vim opens on the alternate screen, is typed into, writes the file, and on
// quitting gives the shell's screen back as it was.
func TestVim(t *testing.T) {
	need(t, "vim")
	path := filepath.Join(t.TempDir(), "test.txt")
	term := startShell(t, terminal.Options{})
	feed(t, term, "echo before-vim\n")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^before-vim$`)})
	shellRows := screen(term, 0, 0).Rows

	feed(t, term, fmt.Sprintf("vim -u NONE -N -i NONE %s\n", path))
	mustWait(t, term, 10, terminal.WaitFor{Regex: re(`^~`), Idle: duration(0.3)})
	s := screen(term, 0, 0)
	if !s.AltScreen || len(s.Rows) != 24 || s.Rows[1] != "~" || s.Cursor.Row != 0 || s.Cursor.Col != 0 {
		t.Fatalf("vim's first screen = %+v", s)
	}

	feed(t, term, "ihello world")
	mustWait(t, term, 5, terminal.WaitFor{Text: "-- INSERT --"})
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^hello world$`), Idle: duration(0.2)})
	s = screen(term, 0, 0)
	if s.Rows[0] != "hello world" || !strings.HasPrefix(s.Rows[23], "-- INSERT --") {
		t.Fatalf("rows = %q, want the text on the first and insert mode on the last", s.Rows)
	}
	if want := (terminal.Cursor{Row: 0, Col: 11, Visible: true}); s.Cursor != want {
		t.Fatalf("cursor = %+v, want %+v", s.Cursor, want)
	}

	feed(t, term, "\x1b:wq\r")
	mustWait(t, term, 5, terminal.WaitFor{Text: "before-vim", Idle: duration(0.3)})
	s = screen(term, 0, 0)
	if s.AltScreen || !slices.Equal(s.Rows[:2], shellRows[:2]) {
		t.Fatalf("the shell's screen was not given back: %q", s.Rows[:3])
	}
	if data, _ := os.ReadFile(path); string(data) != "hello world\n" {
		t.Fatalf("the file holds %q, want %q", data, "hello world\n")
	}
}

// top redraws one screen in place rather than scrolling, and fills a taller
// screen when it is resized. top redraws on a timer of its own, so these are
// the sleeps the package otherwise does without.
func TestTopRedrawsInPlaceAndFollowsAResize(t *testing.T) {
	need(t, "top")
	term := startShell(t, terminal.Options{})
	feed(t, term, "top -d 0.3\n")
	mustWait(t, term, 10, terminal.WaitFor{Regex: re(`^top - `)})
	time.Sleep(1200 * time.Millisecond)
	s := screen(term, 0, 0)
	headers := 0
	for _, r := range s.Rows {
		if strings.HasPrefix(r, "top - ") {
			headers++
		}
	}
	if len(s.Rows) != 24 || headers != 1 || !strings.HasPrefix(s.Rows[0], "top - ") {
		t.Fatalf("top should redraw in place, and %d headers are on the screen: %q", headers, s.Rows)
	}

	if err := term.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	s = screen(term, 0, 0)
	if s.Size != (terminal.Size{Cols: 120, Rows: 40}) || len(s.Rows) != 40 || !strings.HasPrefix(s.Rows[0], "top - ") {
		t.Fatalf("after resizing, size = %+v with %d rows", s.Size, len(s.Rows))
	}
	filled := 0
	for _, r := range s.Rows[24:] {
		if strings.TrimSpace(r) != "" {
			filled++
		}
	}
	if filled <= 5 {
		t.Fatalf("top did not fill the taller screen: %q", s.Rows[24:])
	}
	feed(t, term, "q")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^\$\s*$`)})
}

// A resize reaches the kernel's PTY, so the program reads the new size rather
// than only being drawn on a bigger screen.
func TestResizeReachesTheKernelPTY(t *testing.T) {
	term := startShell(t, terminal.Options{})
	feed(t, term, "stty size\n")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^24 80$`)})
	if err := term.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	feed(t, term, "stty size; echo cols=$COLUMNS\n")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^40 120$`)})
	mustWait(t, term, 5, terminal.WaitFor{Text: "cols=120"})
	if s := screen(term, 0, 0); len(s.Rows) != 40 {
		t.Fatalf("rows = %d, want 40", len(s.Rows))
	}
	if st := term.Status(); st.Size != (terminal.Size{Cols: 120, Rows: 40}) {
		t.Fatalf("status size = %+v, want 120x40", st.Size)
	}
}

func TestResizeWhileVimIsRunning(t *testing.T) {
	need(t, "vim")
	term := startShell(t, terminal.Options{})
	feed(t, term, fmt.Sprintf("vim -u NONE -N -i NONE --cmd 'set ruler' %s\n",
		filepath.Join(t.TempDir(), "r.txt")))
	mustWait(t, term, 10, terminal.WaitFor{Regex: re(`^~`), Idle: duration(0.3)})
	if err := term.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	wait(term, 5, terminal.WaitFor{Idle: duration(0.4)})
	rows := screen(term, 0, 0).Rows
	if len(rows) != 40 || rows[38] != "~" || !strings.Contains(rows[39], "0,0-1") {
		t.Fatalf("vim did not redraw at 120x40: %q", rows[36:])
	}
	feed(t, term, "\x1b:q!\r")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^\$\s*$`)})
}

// Escape sequences a program prints are drawn, never shown.
func TestPrintedEscapeSequencesAreDrawn(t *testing.T) {
	term := startShell(t, terminal.Options{})
	feed(t, term, "clear; printf '\\033[2J\\033[Hhello'; sleep 0.3\n")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^hello`)})
	rows := screen(term, 0, 0).Rows
	if !strings.HasPrefix(rows[0], "hello") {
		t.Fatalf("the first row = %q, want it to start with %q", rows[0], "hello")
	}
	for _, r := range rows {
		if strings.Contains(r, "\x1b") {
			t.Fatalf("an escape sequence reached the screen as text: %q", r)
		}
	}
}

func TestPrintedCursorMovementIsDrawn(t *testing.T) {
	term := startShell(t, terminal.Options{})
	feed(t, term, "clear; printf '\\033[5;10HX\\033[1;1HA\\033[5;1HB'; sleep 0.5\n")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^B\s+X`)})
	rows := screen(term, 0, 0).Rows
	if !strings.HasPrefix(rows[0], "A") || rows[4] != "B        X" {
		t.Fatalf("rows = %q", rows[:5])
	}
}

func TestPrintedColorsReachCells(t *testing.T) {
	term := startShell(t, terminal.Options{})
	feed(t, term, "clear; printf '\\033[1;31mRED\\033[0m plain\\n'\n")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^RED plain$`)})
	var found bool
	for _, row := range term.Cells() {
		for _, span := range row {
			if span.Text == "RED" {
				found = true
				if want := (terminal.Style{Fg: "red", Bold: true}); span.Style != want {
					t.Fatalf("the style of RED = %+v, want %+v", span.Style, want)
				}
			}
		}
	}
	if !found {
		t.Fatalf("no span holds RED: %+v", term.Cells())
	}
}
