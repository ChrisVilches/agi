// Package terminal runs a program on a real PTY and renders what it writes
// onto a virtual screen, so that an agent can drive a program that expects a
// person at a keyboard — a shell it keeps open, an ssh session, a REPL, vim,
// top — by sending it keys and reading the screen back.
//
// It exists because bash, the workspace tool, cannot do any of that. A bash
// call runs one command line to completion with its output on a pipe, and a
// pipe is the wrong thing to be on for most programs a person drives: a
// program that asks whether a terminal is there gets the answer no, one that
// prompts waits for an answer nobody will type, and nothing survives the call,
// so a session that took a password to open is gone by the next one. Here the
// program is on a terminal for as long as the agent wants it, and each call is
// one more thing typed at it or one more look at the screen.
//
// Three things are true of every terminal and are what the rest of the package
// is built around. The PTY is the only source of what the program did. The
// emulator, github.com/charmbracelet/x/vt, is the only source of what the
// screen looks like — escape sequences are fed to it and never parsed here,
// since a second parser would be a second opinion about the screen and the two
// would disagree in exactly the cases that matter. And this API is the only
// way in or out.
//
//	t, err := terminal.Start(terminal.Options{
//		Command: []string{"bash", "--norc", "--noprofile"},
//		Env:     []string{"PS1=$ "},
//	})
//	if err != nil { ... }
//	defer t.Close()
//
//	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
//	defer cancel()
//	t.Wait(ctx, terminal.WaitFor{Text: "$ "})
//	t.FeedKeys("echo hello\n")
//	t.Wait(ctx, terminal.WaitFor{Regex: regexp.MustCompile(`(?m)^hello$`)})
//	fmt.Println(t.Text())
//
// A Terminal is safe for concurrent use, and every one has to be closed: Close
// is what ends the program and everything it started. A host that dies without
// calling it still takes the program down with it, because the kernel sends
// the program SIGHUP when its parent goes and the PTY hangs up; what the
// program started is then only ended on a best-effort basis, which is
// described on Close.
//
// Nothing here is told it is part of an agent. The package knows nothing of
// tools, conversations or the agent loop, the way hooks knows nothing of them,
// so that what a tool exposes of a terminal can be decided — and redecided —
// without touching how a terminal works.
package terminal

// TODO: this is Linux and nothing here works anywhere else. Pdeathsig does not
// exist outside Linux, so the package does not compile rather than quietly
// leaving programs running, which is the right way round for something whose
// job includes making sure nothing outlives the run. It also reads /proc to
// find every process group in a session, and leans on how Linux reports a PTY
// whose last slave has closed. agi has other unix assumptions — session's
// flock, tools.KillGroupOnCancel — so this is not the only thing in the way.
//
// Do not answer it with a no-op elsewhere. A terminal that starts a program
// and cannot promise to end it is worse than one that does not build.
//
// KNOWN LIMITATION: this costs more than terminals. cli imports the terminal
// tools whether or not a run enables them, so agi as a whole does not build on
// macOS — it did before this package existed, since the other unix
// assumptions above are ones macOS shares. The fix, when it is wanted, is a
// build tag rather than a no-op: this package as it is on Linux, and elsewhere
// a Start that refuses with "terminals need Linux", so that --enable-terminals
// fails at the start of a run and every other run is unaffected. It is
// deliberately not done yet.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// DefaultCols and DefaultRows are the size a terminal is given when
	// Options names none. They are the size every terminal has had since
	// terminals were hardware, which is the size a program that never asks is
	// written for.
	DefaultCols = 80
	DefaultRows = 24

	// DefaultTerm is what TERM says when Options names nothing else. It is
	// what most terminal emulators in use say about themselves, so a program
	// reading it finds the capabilities it expects; and the emulator here
	// understands what a program sends on the strength of it.
	DefaultTerm = "xterm-256color"

	// MaxDim bounds both sides of the size. Nothing reads a screen a thousand
	// rows tall, and a bound is what stops a mistaken size from allocating a
	// screen nobody will.
	MaxDim = 1000

	// DefaultScrollback is how many lines that scrolled off the top are kept
	// when Options names no number.
	DefaultScrollback = 10000

	// MaxScrollback bounds Options.Scrollback, for the reason MaxDim bounds the
	// size.
	MaxScrollback = 1_000_000

	// closeGrace is how long Close gives the program to go of its own accord
	// before it is killed, split between the two signals that ask it to. It is
	// short because Close is called when the work is over and nobody is
	// waiting for anything but the run to end.
	closeGrace = 2 * time.Second

	// readTimeout is how long one read of the PTY waits for output. It is what
	// bounds every question the reader asks between reads — whether Close was
	// called, whether the program has gone — so it is short.
	readTimeout = 100 * time.Millisecond
)

var (
	// ErrNotRunning is what input reports once there is nothing to send it to:
	// the program has exited, or the terminal has been closed. It is a
	// sentinel because the two are worth telling apart from a write that
	// failed, and a caller that drives a program which may exit at any moment
	// — an ssh session the far end closed — has to be able to.
	ErrNotRunning = errors.New("terminal: the program is not running")

	// ErrNoCondition is what Wait reports when it was given nothing to wait
	// for. Waiting on nothing would return at once and read as though
	// something had happened, which is the one wrong answer worse than an
	// error.
	ErrNoCondition = errors.New("terminal: WaitFor needs Text, Regex, Idle or Exit")
)

// Options configures Start. The zero value runs the user's shell at 80x24.
type Options struct {
	// Command is the program and its arguments, looked up on PATH. Left empty
	// it is $SHELL when that is executable, then /bin/bash, then /bin/sh. A
	// shell started this way reads the user's rc files, so a caller wanting a
	// predictable prompt runs `bash --norc --noprofile` with PS1 set.
	Command []string

	// Cols and Rows are the size the terminal starts at. Zero means
	// DefaultCols and DefaultRows, and neither may exceed MaxDim.
	Cols, Rows int

	// Env holds KEY=VALUE entries laid over the host's own environment, each
	// replacing a variable of the same name. An entry with no = is refused
	// rather than guessed at.
	Env []string

	// Term is the TERM the program is given, DefaultTerm when empty. It is set
	// before Env is applied, so an Env entry naming TERM wins.
	Term string

	// Dir is the program's working directory, the host's own when empty. The
	// host's working directory is never changed.
	Dir string

	// Scrollback is how many lines that scrolled off the screen are kept. Zero
	// means DefaultScrollback, and it may not exceed MaxScrollback.
	Scrollback int
}

// Terminal is one program running on a PTY, and the screen it has drawn.
type Terminal struct {
	argv   []string
	pty    *ptySession
	screen *screen
	child  *child

	closing    atomic.Bool
	closeOnce  sync.Once
	readerDone chan struct{}

	// done is closed once two things are both true: the program has been
	// reaped, and everything it wrote has reached the screen. Either can
	// happen last — a program's final output can still be in the PTY when it
	// is reaped, and a program can be slow to be reaped after its last write —
	// so each side sets its own flag and then calls tryDone, and whichever
	// arrives second is the one that closes it. Closing it on the first would
	// let a caller read a screen missing the program's last words.
	drained  atomic.Bool
	done     chan struct{}
	doneOnce sync.Once
}

// defaultShell is the program a terminal runs when it is told none: the user's
// own shell when $SHELL names one that can be run, and otherwise the shells
// that are nearly always there.
func defaultShell() string {
	if sh := os.Getenv("SHELL"); sh != "" && unix.Access(sh, unix.X_OK) == nil {
		return sh
	}
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash"
	}
	return "/bin/sh"
}

// mergeEnv lays KEY=VALUE overrides over base, replacing a variable already
// there and appending one that is not. A replaced variable keeps its place,
// so the environment a program sees is the host's in the host's order.
func mergeEnv(base []string, overrides ...string) []string {
	index := map[string]int{}
	merged := append([]string(nil), base...)
	for i, kv := range merged {
		key, _, _ := strings.Cut(kv, "=")
		index[key] = i
	}
	for _, kv := range overrides {
		key, _, _ := strings.Cut(kv, "=")
		if i, ok := index[key]; ok {
			merged[i] = kv
			continue
		}
		index[key] = len(merged)
		merged = append(merged, kv)
	}
	return merged
}

// checkSize refuses a size the terminal cannot have. Start and Resize share it
// so that a size one accepts the other does too.
func checkSize(cols, rows int) error {
	if cols < 1 || cols > MaxDim || rows < 1 || rows > MaxDim {
		return fmt.Errorf("terminal: columns and rows must each be between 1 and %d, got %dx%d", MaxDim, cols, rows)
	}
	return nil
}

// Start runs a program on a new PTY.
//
// Everything about the options is checked before anything is started, so a
// mistake costs an error and leaves nothing behind; and a program that cannot
// be started is an error too, with nothing left running either.
func Start(opts Options) (*Terminal, error) {
	cols, rows := opts.Cols, opts.Rows
	if cols == 0 {
		cols = DefaultCols
	}
	if rows == 0 {
		rows = DefaultRows
	}
	if err := checkSize(cols, rows); err != nil {
		return nil, err
	}
	scrollback := opts.Scrollback
	if scrollback == 0 {
		scrollback = DefaultScrollback
	}
	if scrollback < 1 || scrollback > MaxScrollback {
		return nil, fmt.Errorf("terminal: scrollback must be between 1 and %d lines, got %d", MaxScrollback, scrollback)
	}
	for _, kv := range opts.Env {
		if !strings.Contains(kv, "=") {
			return nil, fmt.Errorf("terminal: an environment entry must be KEY=VALUE, got %q", kv)
		}
	}
	argv := append([]string(nil), opts.Command...)
	if len(argv) == 0 {
		argv = []string{defaultShell()}
	}
	term := opts.Term
	if term == "" {
		term = DefaultTerm
	}

	t := &Terminal{
		argv:       argv,
		child:      newChild(),
		readerDone: make(chan struct{}),
		done:       make(chan struct{}),
	}
	t.pty = &ptySession{
		argv: argv,
		env:  mergeEnv(os.Environ(), append([]string{"TERM=" + term}, opts.Env...)...),
		dir:  opts.Dir,
		cols: cols,
		rows: rows,
	}
	t.screen = newScreen(cols, rows, scrollback, t.replyToProgram)

	pid, err := t.pty.spawn(func(state *os.ProcessState) {
		t.child.setExited(state)
		t.tryDone()
	})
	if err != nil {
		t.screen.close()
		return nil, fmt.Errorf("terminal: starting %q: %w", argv[0], err)
	}
	t.child.pid = pid
	go t.readPTY()
	return t, nil
}

// readPTY hands what the program writes to the screen, until the PTY reports
// end of file or Close asks it to stop.
//
// Reads wait at most readTimeout, so that a program which has gone quiet does
// not keep the loop from noticing either of those.
func (t *Terminal) readPTY() {
	defer close(t.readerDone)
	defer t.setDrained()
	buf := make([]byte, 65536)
	for !t.closing.Load() {
		n, eof := t.pty.read(buf, readTimeout)
		if eof {
			// No process holds the slave side any more, so nothing more can
			// arrive.
			return
		}
		if n > 0 {
			t.feed(buf[:n])
			continue
		}
		if t.child.gone() {
			// The program has been reaped and a whole read went by with
			// nothing in it, so what it wrote is on the screen. Something it
			// started may still hold the slave open and go on writing, which
			// is why the loop does not end here: that output is rendered
			// too, but the program the caller asked for is done.
			t.setDrained()
		}
	}
}

// feed hands output to the screen, and is the one place a panic is recovered.
// The emulator is somebody else's parser of input that an arbitrary program
// produced, and a sequence it cannot handle must cost that sequence and not
// the reader: a reader that died would leave a terminal whose screen never
// changed again and whose Done never closed.
func (t *Terminal) feed(data []byte) {
	defer func() { _ = recover() }()
	t.screen.feed(data)
}

func (t *Terminal) setDrained() {
	t.drained.Store(true)
	t.tryDone()
}

// tryDone closes done if both of the things it waits for are true, and wakes
// every waiter either way: a Wait for Exit is waiting on exactly this.
func (t *Terminal) tryDone() {
	if t.drained.Load() && t.child.gone() {
		t.doneOnce.Do(func() { close(t.done) })
	}
	t.screen.notify()
}

func (t *Terminal) isDone() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

// replyToProgram sends the emulator's answers to the program's questions —
// where the cursor is, what kind of terminal this is — back through the PTY,
// the way a real terminal answers them.
func (t *Terminal) replyToProgram(data []byte) {
	_ = t.pty.write(data)
}

// Done returns a channel that is closed once the program has exited and
// everything it wrote has been rendered, so a screen read after it is the
// program's last.
func (t *Terminal) Done() <-chan struct{} { return t.done }

func (t *Terminal) writeInput(data []byte) error {
	if t.closing.Load() || t.child.gone() {
		return ErrNotRunning
	}
	if err := t.pty.write(data); err != nil {
		return fmt.Errorf("terminal: writing input: %w", err)
	}
	return nil
}

// FeedKeys sends text to the program as typed input, byte for byte: "\n" or
// "\r" is Enter — "\r" is what a full-screen program such as vim expects —
// "\x03" is Ctrl-C, "\x1b" is Esc, and "\x1b[A" is Up.
//
// Ctrl-C goes through the terminal's line discipline exactly as it does when a
// person presses it, so the foreground job is sent SIGINT by the kernel. That
// is the reason a key is never emulated with a signal: the kernel knows which
// job is in the foreground and this package does not.
func (t *Terminal) FeedKeys(keys string) error {
	return t.writeInput([]byte(keys))
}

// Key sends named keys in order, Key("CTRL_C") or Key("UP", "ENTER"), and
// KeyNames lists them. A key with more than one encoding is sent the way the
// program has asked for it: an arrow key follows the program's cursor-key
// mode, which is what lets one name mean Up both in a shell and in vim.
//
// Every name is translated before anything is sent, so a name it does not know
// sends nothing rather than the keys before it.
func (t *Terminal) Key(names ...string) error {
	if len(names) == 0 {
		return errors.New("terminal: Key needs at least one key name")
	}
	application := t.screen.applicationCursorKeys()
	var data []byte
	for _, name := range names {
		b, err := keyBytes(name, application)
		if err != nil {
			return err
		}
		data = append(data, b...)
	}
	return t.writeInput(data)
}

// ScreenOptions selects what Screen returns. The zero value is the visible
// screen and nothing else.
type ScreenOptions struct {
	// Lines is how many lines to return, counted up from the bottom row of the
	// screen through the visible rows and then on into scrollback. Zero or less
	// means the height of the screen, which is exactly what is visible. Blank
	// rows count: the bottom of a screen with little on it is blank, and that
	// is what it looks like.
	Lines int

	// MaxChars, when above zero, caps strings.Join(rows, "\n") in characters.
	// The oldest lines are the ones that go, because the bottom of a terminal
	// is where the program is now; a bottom line too long to fit on its own is
	// cut and ends with TruncationMarker.
	MaxChars int
}

// Screen returns the bottom lines of scrollback and screen together, oldest
// first.
//
// Only the normal screen feeds scrollback. A full-screen program — vim, less,
// top — draws on the alternate screen and adds nothing to it, and what was
// already there stays readable while it runs.
func (t *Terminal) Screen(opts ScreenOptions) Snapshot {
	return t.screen.snapshot(max(opts.Lines, 0), max(opts.MaxChars, 0))
}

// Text returns the visible screen with its trailing blank rows dropped.
func (t *Terminal) Text() string { return t.screen.text() }

// Cells returns the visible screen as runs of identically styled text, one
// slice per row. A trailing run of unstyled blanks is dropped.
func (t *Terminal) Cells() [][]Span { return t.screen.cells() }

// Resize changes the size of the screen and of the kernel's PTY, and the
// program is sent SIGWINCH. The bounds are the ones Start uses.
//
// The screen changes first. A program redraws when it is told the size
// changed, and the redraw has to land on a screen of the size it was drawn for
// or it is clipped to the old one.
func (t *Terminal) Resize(cols, rows int) error {
	if err := checkSize(cols, rows); err != nil {
		return err
	}
	t.screen.resize(cols, rows)
	if err := t.pty.resize(cols, rows); err != nil {
		return fmt.Errorf("terminal: resizing: %w", err)
	}
	return nil
}

// Status describes the program as it stands.
type Status struct {
	Running bool `json:"running"`
	Pid     int  `json:"pid"`
	// ExitCode is set once the program has exited by itself, and ExitSignal
	// once a signal ended it. Neither is set while it runs, and never both.
	ExitCode   *int     `json:"exit_code"`
	ExitSignal *int     `json:"exit_signal"`
	Size       Size     `json:"size"`
	Command    []string `json:"command"`
}

// Status reports whether the program is running, how it ended if it has, and
// the size of the terminal it is on.
func (t *Terminal) Status() Status {
	running, code, signal := t.child.status()
	return Status{
		Running:    running,
		Pid:        t.child.pid,
		ExitCode:   code,
		ExitSignal: signal,
		Size:       t.screen.size(),
		Command:    append([]string(nil), t.argv...),
	}
}

// WaitFor is what Wait waits for. Every condition that is set has to hold at
// once; one that is not set is not checked.
type WaitFor struct {
	// Text has to appear on the visible screen. It is looked for twice: in the
	// rows joined by newlines with their trailing spaces trimmed, so text
	// spanning lines can be found, and in each row exactly as it is, so a
	// prompt such as "$ " is found with the space it ends in.
	Text string

	// Regex has to match the visible screen, looked for in the same two
	// places as Text. No flags are added: a pattern wanting ^ and $ to match
	// at each line starts with (?m). One consequence of the two places is
	// easy to miss — trailing spaces never survive into the joined form, so
	// (?m)^>>> $ cannot match across lines, while (?m)^hello\n>>> does.
	Regex *regexp.Regexp

	// Idle has to have passed with no output from the program. It is a guess
	// that a program which has stopped writing has finished, and a program
	// thinking for longer than Idle disproves it, so Text and Regex are the
	// better thing to wait for whenever what comes next is known.
	Idle time.Duration

	// Exit requires the program to have exited and its output to be on the
	// screen, which is the same moment Done is closed.
	Exit bool
}

// Wait blocks until every condition in w holds, and returns nil, or until ctx
// is done, and returns ctx.Err(). A context with no deadline waits for as long
// as it takes. Waiting for nothing is ErrNoCondition.
//
// Output arrives in the background, so a screen read straight after FeedKeys
// may not show what the keys did yet; Wait for it first. And wait for the
// prompt that follows the output rather than the output alone: a program that
// sets up the terminal as it starts — readline does — can discard keys typed
// ahead of it, and the prompt is the program saying it is ready.
//
// The conditions are checked on every chunk of output and at least every
// 50ms, the second because Idle only becomes true by time passing. Waiting
// holds up nothing else: every other method can be called meanwhile.
func (t *Terminal) Wait(ctx context.Context, w WaitFor) error {
	if w.Text == "" && w.Regex == nil && w.Idle <= 0 && !w.Exit {
		return ErrNoCondition
	}
	return t.screen.wait(ctx, func(s *screen) bool {
		if w.Exit && !t.isDone() {
			return false
		}
		if w.Idle > 0 && time.Since(s.lastOutput) < w.Idle {
			return false
		}
		if (w.Text != "" || w.Regex != nil) && !s.containsLocked(w.Text, w.Regex) {
			return false
		}
		return true
	})
}

// Close ends the program and everything it started, closes the PTY, and
// returns the final status. It may be called any number of times, from any
// number of goroutines, and the screen stays readable afterwards; only input
// is refused, with ErrNotRunning.
//
// Ending the program means ending its whole session and not only the program.
// An interactive shell puts each job it runs in a process group of its own, so
// signalling the shell's group would leave every job behind; each group in the
// session is signalled instead — SIGHUP, then SIGTERM, then SIGKILL, with
// SIGCONT beside each so a stopped job can act on it — and a final SIGKILL
// sweeps up whatever ignored the rest. That is the guarantee, and it is
// Close's alone.
//
// A host that dies without calling Close gets less, and it is a known
// limitation rather than an oversight. The program itself is ended — it is
// sent SIGHUP by the kernel and its PTY hangs up — but the jobs it started are
// ended only if the shell passes SIGHUP on to them, which bash does when it
// handles the signal before it notices the hang-up, and not otherwise.
// Nothing in the host is left running to do better, which is why Close is the
// guaranteed path and there is a test that only reports the other one.
func (t *Terminal) Close() Status {
	t.closeOnce.Do(func() {
		t.closing.Store(true)
		t.child.terminate(closeGrace)
		select {
		case <-t.readerDone:
		case <-time.After(time.Second):
		}
		t.pty.close()
		t.screen.close()
	})
	return t.Status()
}
