package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/terminal"
	"github.com/ChrisVilches/agi/tools"
)

// The tool names. They are constants because the four are one tool split by
// what a call does, and each description names the others: a renamed tool
// whose siblings still spoke of the old name would be sending the model to a
// tool that does not exist.
const (
	TerminalOpen  = "terminal_open"
	TerminalSend  = "terminal_send"
	TerminalRead  = "terminal_read"
	TerminalClose = "terminal_close"
)

const (
	// DefaultMaxTerminals is how many terminals one agent may have open at
	// once when the command line says nothing.
	//
	// Every open terminal is a live process and an OS thread held for as long
	// as it stays open, and an agent that keeps opening them without closing
	// any is one that has lost track of them. Eight is more than any task that
	// knows what it is doing needs at once — a shell, a session on another
	// machine, a REPL, and room to spare — and few enough that an agent which
	// has lost track is told so long before the machine notices.
	DefaultMaxTerminals = 8

	// terminalShell is what a terminal runs. It is bash for the reason the
	// bash tool runs bash: one shell the model can count on, rather than
	// whichever the user's $SHELL happens to name and a prompt decorated by an
	// rc file it has never seen. It is still an ordinary interactive bash, rc
	// file and all, because a terminal is meant to behave like the one the
	// user would have opened.
	terminalShell = "bash"

	// settle is how long output has to have stopped before a wait ends early.
	// It is long enough that a program writing in bursts is not caught
	// between two of them, and short enough that a command which printed and
	// returned to its prompt comes back at once.
	settle = 500 * time.Millisecond

	// openWait is how long opening a terminal waits for the shell to show its
	// first prompt. It is generous because an rc file can be slow — one that
	// loads a version manager takes seconds — and a terminal returned before
	// its prompt is one whose first keys arrive before the shell is reading,
	// to be echoed and then lost.
	openWait = 10 * time.Second

	// maxWait bounds wait_ms. It is the bound a bash command has, for the same
	// reason: one call that cannot end for minutes is a run stalled on it.
	maxWait = DefaultTimeout

	// maxScreen caps the characters one read returns. A read is asked for in
	// lines, and a line can be any length, so this is what keeps a terminal
	// full of something unreadable — a minified file, a progress bar drawn a
	// thousand times — from taking over the conversation. The newest lines are
	// the ones kept, because the bottom of a terminal is where the program is
	// now.
	maxScreen = 20_000
)

// Terminals is one agent's open terminals, and the four tools that use them.
//
// It belongs to exactly one agent. The run's own agent has one, each fork has
// one and each subagent has one, and none can reach another's: a terminal is
// state, and two agents typing into one shell would be a session belonging to
// neither. It follows that ids are numbered per agent from 1 — a fork opening
// its first terminal gets 1 whatever its parent had open — and that a fork's
// terminals are closed when the fork ends. Its answer is the only thing that
// leaves a fork, and a terminal the agent that asked for the fork had never
// heard of would be one nothing ever closed.
//
// Nothing is opened until the agent asks, so an agent that never calls
// terminal_open never starts a process. Ids are never reused within an agent,
// which is what lets a closed terminal be told apart from one that never
// existed — the two mistakes are corrected differently.
//
// A nil Terminals is an agent that was given no terminals, and Close on it does
// nothing, so the code that ends an agent need not ask whether it had any.
type Terminals struct {
	// mu guards the map and the counter and nothing else. It is held to find a
	// terminal and released before anything slow is done with it: a read that
	// waits holds a terminal, not the list of them.
	//
	// Nothing touches one agent's terminals from two goroutines today — an
	// agent's tool calls run one after another, and the agent is finished by
	// the time they are closed. The lock is kept anyway, because a map read
	// while another goroutine grows it is not a wrong answer but a fatal
	// error the runtime will not let anything recover from, and the facts that
	// make it impossible today live in other packages. A guarantee held by
	// facts that far away stops holding the first time one of them changes.
	mu   sync.Mutex
	open map[int]*terminal.Terminal
	// last is the highest id handed out, so every id from 1 to last that is
	// not in open is one that was closed.
	last int
	max  int
}

// NewTerminals returns an agent's terminals, of which at most limit may be
// open at once. A limit below one is raised to one, since a tool that could
// never open anything is not a tool.
func NewTerminals(limit int) *Terminals {
	return &Terminals{open: make(map[int]*terminal.Terminal), max: max(limit, 1)}
}

// Tools returns the four tools, which share this agent's terminals. They are
// registered together or not at all.
func (ts *Terminals) Tools() []tools.Tool {
	return []tools.Tool{
		&terminalOpen{ts: ts},
		&terminalSend{ts: ts},
		&terminalRead{ts: ts},
		&terminalClose{ts: ts},
	}
}

// Close closes every terminal still open, which ends what runs in each and
// everything it started. It is how an agent's terminals end when the agent
// does, however it ends.
//
// They are closed in parallel, since closing one can wait out a grace period
// for a program that does not go when it is asked, and an agent that left
// several such programs open would otherwise keep the run waiting for each in
// turn.
func (ts *Terminals) Close() {
	if ts == nil {
		return
	}
	ts.mu.Lock()
	open := ts.open
	ts.open = map[int]*terminal.Terminal{}
	ts.mu.Unlock()

	var wg sync.WaitGroup
	for _, t := range open {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t.Close()
		}()
	}
	wg.Wait()
}

// add opens a terminal under the next id, refusing when the agent already has
// as many open as it may.
func (ts *Terminals) add(start func() (*terminal.Terminal, error)) (int, *terminal.Terminal, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if len(ts.open) >= ts.max {
		return 0, nil, fmt.Errorf("%s: %d terminals are already open, which is as many as this run "+
			"allows; close one you no longer need with %s first. Open: %s",
			TerminalOpen, len(ts.open), TerminalClose, ts.listLocked())
	}
	// Starting a terminal is quick — it forks a shell and returns — so it is
	// done with the lock held, which is what keeps two opens from both taking
	// the last free place.
	t, err := start()
	if err != nil {
		return 0, nil, fmt.Errorf("%s: %w", TerminalOpen, err)
	}
	ts.last++
	ts.open[ts.last] = t
	return ts.last, t, nil
}

// get returns terminal id, or an error saying which of the ways it could be
// missing applies, since each is corrected differently: a closed terminal by
// opening a new one, an id never handed out by using one that was.
//
// An id from an earlier run of a resumed conversation is the second kind and
// is said to be, because ids are numbered again from 1 in every run and
// nothing else in the conversation says the terminals it mentions are gone.
func (ts *Terminals) get(tool string, id int) (*terminal.Terminal, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.getLocked(tool, id)
}

func (ts *Terminals) getLocked(tool string, id int) (*terminal.Terminal, error) {
	if t, ok := ts.open[id]; ok {
		return t, nil
	}
	if id >= 1 && id <= ts.last {
		return nil, fmt.Errorf("%s: terminal %d was closed, and nothing that was running in it is "+
			"there any more; open a new one with %s", tool, id, TerminalOpen)
	}
	return nil, fmt.Errorf("%s: there is no terminal %d; open ones: %s. Terminals do not survive from "+
		"one run to the next, so a terminal mentioned earlier in the conversation may belong to a "+
		"run that has ended", tool, id, ts.listLocked())
}

// remove takes terminal id out of the list and returns it, for closing.
func (ts *Terminals) remove(id int) (*terminal.Terminal, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t, err := ts.getLocked(TerminalClose, id)
	if err != nil {
		return nil, err
	}
	delete(ts.open, id)
	return t, nil
}

// listLocked names the open terminals in id order, saying which of them have a
// program that has exited, since those are the ones worth closing.
func (ts *Terminals) listLocked() string {
	if len(ts.open) == 0 {
		return "none"
	}
	ids := make([]int, 0, len(ts.open))
	for id := range ts.open {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = strconv.Itoa(id)
		if !ts.open[id].Status().Running {
			names[i] += " (exited)"
		}
	}
	return strings.Join(names, ", ")
}

// terminalOpen opens a terminal.
type terminalOpen struct{ ts *Terminals }

// TerminalOpenParams is the input schema for terminal_open.
type TerminalOpenParams struct {
	Cols int `json:"cols,omitempty"`
	Rows int `json:"rows,omitempty"`
}

func (o *terminalOpen) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: TerminalOpen,
		Description: strings.TrimSpace(fmt.Sprintf(`
Open an interactive terminal running bash, and return its number and its first
screen. It stays open across calls until you close it: type into it with %s
and look at it with %s.

Use a terminal for what a one-off command cannot do: a program that asks
questions or waits for keys, a session that has to stay open between steps —
ssh to another machine, a database shell, a REPL — or a full-screen program
such as vim, less or top. For a command that runs and finishes on its own,
a terminal is the slow way to do it.

Everything you start in a terminal keeps running between your calls, and all of
it is ended when you close the terminal or when your work ends. Close a
terminal with %s once you are done with it; only a few may be open at once.`,
			TerminalSend, TerminalRead, TerminalClose)),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cols": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Width in columns. Defaults to %d.", terminal.DefaultCols),
				},
				"rows": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Height in rows. Defaults to %d.", terminal.DefaultRows),
				},
			},
			"additionalProperties": false,
		},
	}
}

func (o *terminalOpen) Announce(args json.RawMessage) string {
	var p TerminalOpenParams
	_ = json.Unmarshal(args, &p)
	if p.Cols > 0 || p.Rows > 0 {
		return fmt.Sprintf("a new terminal, %dx%d", orDefault(p.Cols, terminal.DefaultCols), orDefault(p.Rows, terminal.DefaultRows))
	}
	return "a new terminal"
}

func (o *terminalOpen) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p TerminalOpenParams
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("%s: arguments are not valid JSON: %w", TerminalOpen, err)
	}
	id, t, err := o.ts.add(func() (*terminal.Terminal, error) {
		return terminal.Start(terminal.Options{Command: []string{terminalShell}, Cols: p.Cols, Rows: p.Rows})
	})
	if err != nil {
		return "", err
	}
	// Something has to be on the screen before the quiet counts, since a
	// shell still reading its rc file is quiet too, and that quiet is the one
	// that must not be taken for a prompt.
	waiting, cancel := context.WithTimeout(ctx, openWait)
	err = t.Wait(waiting, terminal.WaitFor{Regex: anything, Idle: settle})
	cancel()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	snap := t.Screen(terminal.ScreenOptions{MaxChars: maxScreen})
	return fmt.Sprintf("Opened terminal %d.\n%s", id, render(t, id, snap, err == nil)), nil
}

// anything matches a screen with anything on it.
var anything = regexp.MustCompile(`\S`)

// terminalSend types into a terminal.
type terminalSend struct{ ts *Terminals }

// TerminalSendParams is the input schema for terminal_send.
type TerminalSendParams struct {
	Terminal int      `json:"terminal"`
	Text     string   `json:"text,omitempty"`
	Keys     []string `json:"keys,omitempty"`
	WaitMS   int      `json:"wait_ms,omitempty"`
	Lines    int      `json:"lines,omitempty"`
}

func (s *terminalSend) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: TerminalSend,
		Description: strings.TrimSpace(fmt.Sprintf(`
Type into a terminal opened with %s: text first, then keys.

  {"terminal": 1, "text": "ls -la", "keys": ["ENTER"], "wait_ms": 2000}

text is typed exactly as written, character by character. keys are named keys
pressed after it, in order: ENTER, TAB, ESC, BACKSPACE, SPACE, UP, DOWN, LEFT,
RIGHT, HOME, END, PAGE_UP, PAGE_DOWN, INSERT, DELETE, F1 to F12, CTRL_A to
CTRL_Z (CTRL_C interrupts, CTRL_D ends input), and ALT_ followed by a key.
Nothing is run until you press ENTER. Write keys by name rather than as escape
sequences inside text.

Give wait_ms or lines to read the terminal in the same call, exactly as %s
would; give neither and nothing is read, and the call only says it was sent.`,
			TerminalOpen, TerminalRead)),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"terminal": map[string]any{
					"type":        "integer",
					"description": "The number " + TerminalOpen + " returned.",
				},
				"text": map[string]any{
					"type":        "string",
					"description": "Text to type, exactly as written.",
				},
				"keys": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Named keys to press after the text, in order.",
				},
				"wait_ms": waitParameter(),
				"lines":   linesParameter(),
			},
			"required":             []string{"terminal"},
			"additionalProperties": false,
		},
	}
}

// parseSend decodes and validates the model's arguments, translating nothing
// yet: the keys are checked by the terminal, which knows the names.
func parseSend(args json.RawMessage) (TerminalSendParams, error) {
	var p TerminalSendParams
	if err := json.Unmarshal(args, &p); err != nil {
		return p, fmt.Errorf("%s: arguments are not valid JSON: %w", TerminalSend, err)
	}
	if p.Text == "" && len(p.Keys) == 0 {
		return p, fmt.Errorf("%s: nothing to send; give text, keys, or both", TerminalSend)
	}
	if err := checkRead(TerminalSend, p.WaitMS, p.Lines); err != nil {
		return p, err
	}
	return p, nil
}

// Announce shows what is about to be typed, in full, and where. Like a bash
// command, it is the thing about to happen to the user's machine, and the part
// worth watching for is as likely to be at the end as at the start.
func (s *terminalSend) Announce(args json.RawMessage) string {
	p, err := parseSend(args)
	if err != nil {
		return ""
	}
	var typed []string
	if p.Text != "" {
		typed = append(typed, p.Text)
	}
	if len(p.Keys) > 0 {
		typed = append(typed, "["+strings.Join(p.Keys, "] [")+"]")
	}
	return fmt.Sprintf("terminal %d\n%s", p.Terminal, tools.Detail(block(">", strings.Join(typed, " "))))
}

func (s *terminalSend) Call(ctx context.Context, args json.RawMessage) (string, error) {
	p, err := parseSend(args)
	if err != nil {
		return "", err
	}
	t, err := s.ts.get(TerminalSend, p.Terminal)
	if err != nil {
		return "", err
	}
	// Every key name is checked before anything is typed, so a call with a
	// name the terminal does not know types nothing rather than its text
	// alone, and the corrected call is not typed on top of half of this one.
	for _, name := range p.Keys {
		if err := terminal.CheckKey(name); err != nil {
			return "", fmt.Errorf("%s: %w; the names are %s", TerminalSend, err, strings.Join(terminal.KeyNames(), ", "))
		}
	}
	if p.Text != "" {
		if err := t.FeedKeys(p.Text); err != nil {
			return "", sendError(t, p.Terminal, err)
		}
	}
	if len(p.Keys) > 0 {
		if err := t.Key(p.Keys...); err != nil {
			return "", sendError(t, p.Terminal, err)
		}
	}
	if p.WaitMS == 0 && p.Lines == 0 {
		return fmt.Sprintf("Sent to terminal %d.", p.Terminal), nil
	}
	return view(ctx, t, p.Terminal, time.Duration(p.WaitMS)*time.Millisecond, p.Lines)
}

// sendError explains input that could not be typed. A program that has exited
// is the case worth spelling out, because the terminal is still there and can
// still be read: its last screen is often the explanation — ssh saying the
// connection closed — and the model should read it before it closes the
// terminal.
func sendError(t *terminal.Terminal, id int, err error) error {
	if errors.Is(err, terminal.ErrNotRunning) {
		return fmt.Errorf("%s: the program in terminal %d has %s, so there is nothing to type into; "+
			"read its last screen with %s, then close it", TerminalSend, id, ended(t.Status()), TerminalRead)
	}
	return fmt.Errorf("%s: %w", TerminalSend, err)
}

// terminalRead reads a terminal's screen.
type terminalRead struct{ ts *Terminals }

// TerminalReadParams is the input schema for terminal_read.
type TerminalReadParams struct {
	Terminal int `json:"terminal"`
	WaitMS   int `json:"wait_ms,omitempty"`
	Lines    int `json:"lines,omitempty"`
}

func (r *terminalRead) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: TerminalRead,
		Description: strings.TrimSpace(fmt.Sprintf(`
Read what a terminal opened with %s is showing.

  {"terminal": 1, "lines": 60, "wait_ms": 5000}

Without lines you get the visible screen. With lines you get that many lines
counted up from the bottom, reaching past the top of the screen into what has
scrolled off it, so lines is how you look further back. Blank lines at the
bottom are left out. A full-screen program such as vim draws on a screen of its
own and has no history, so for one of those only the visible screen exists.

wait_ms is the longest to wait before reading, for a command that is still
working. The wait ends early once output has stopped for half a second, so a
generous wait costs nothing when the program is quick. The result says whether
output was still arriving when the wait ran out: if it was, or if the program
has not finished, read again.`, TerminalOpen)),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"terminal": map[string]any{
					"type":        "integer",
					"description": "The number " + TerminalOpen + " returned.",
				},
				"wait_ms": waitParameter(),
				"lines":   linesParameter(),
			},
			"required":             []string{"terminal"},
			"additionalProperties": false,
		},
	}
}

func parseRead(args json.RawMessage) (TerminalReadParams, error) {
	var p TerminalReadParams
	if err := json.Unmarshal(args, &p); err != nil {
		return p, fmt.Errorf("%s: arguments are not valid JSON: %w", TerminalRead, err)
	}
	return p, checkRead(TerminalRead, p.WaitMS, p.Lines)
}

func (r *terminalRead) Announce(args json.RawMessage) string {
	p, err := parseRead(args)
	if err != nil {
		return ""
	}
	what := "the screen"
	if p.Lines > 0 {
		what = fmt.Sprintf("the last %d lines", p.Lines)
	}
	if p.WaitMS > 0 {
		return fmt.Sprintf("terminal %d, %s, after up to %s", p.Terminal, what, time.Duration(p.WaitMS)*time.Millisecond)
	}
	return fmt.Sprintf("terminal %d, %s", p.Terminal, what)
}

func (r *terminalRead) Call(ctx context.Context, args json.RawMessage) (string, error) {
	p, err := parseRead(args)
	if err != nil {
		return "", err
	}
	t, err := r.ts.get(TerminalRead, p.Terminal)
	if err != nil {
		return "", err
	}
	return view(ctx, t, p.Terminal, time.Duration(p.WaitMS)*time.Millisecond, p.Lines)
}

// terminalClose closes a terminal.
type terminalClose struct{ ts *Terminals }

// TerminalCloseParams is the input schema for terminal_close.
type TerminalCloseParams struct {
	Terminal int `json:"terminal"`
}

func (c *terminalClose) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: TerminalClose,
		Description: strings.TrimSpace(fmt.Sprintf(`
Close a terminal opened with %s, ending the program in it and everything that
program started, including anything left running in the background.

Close a terminal as soon as you are done with it: only a few may be open at
once. A closed terminal's number is not used again.`, TerminalOpen)),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"terminal": map[string]any{
					"type":        "integer",
					"description": "The number " + TerminalOpen + " returned.",
				},
			},
			"required":             []string{"terminal"},
			"additionalProperties": false,
		},
	}
}

func (c *terminalClose) Announce(args json.RawMessage) string {
	var p TerminalCloseParams
	if err := json.Unmarshal(args, &p); err != nil {
		return ""
	}
	return fmt.Sprintf("terminal %d", p.Terminal)
}

func (c *terminalClose) Call(_ context.Context, args json.RawMessage) (string, error) {
	var p TerminalCloseParams
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("%s: arguments are not valid JSON: %w", TerminalClose, err)
	}
	t, err := c.ts.remove(p.Terminal)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Closed terminal %d; its program %s.", p.Terminal, ended(t.Close())), nil
}

// waitParameter and linesParameter are the two read parameters, written once
// because terminal_send and terminal_read take them with one meaning.
func waitParameter() map[string]any {
	return map[string]any{
		"type": "integer",
		"description": fmt.Sprintf("The longest to wait before reading, in milliseconds, at most %d. "+
			"The wait ends early once output has stopped.", maxWait.Milliseconds()),
	}
}

func linesParameter() map[string]any {
	return map[string]any{
		"type":        "integer",
		"description": "How many lines to return, counted up from the bottom. Defaults to the visible screen.",
	}
}

// checkRead refuses a read the terminal could not do. A negative number is
// refused rather than read as the default, since a model that wrote one meant
// something by it and is better told it is not a thing.
func checkRead(tool string, waitMS, lines int) error {
	if waitMS < 0 || time.Duration(waitMS)*time.Millisecond > maxWait {
		return fmt.Errorf("%s: wait_ms must be between 0 and %d", tool, maxWait.Milliseconds())
	}
	if lines < 0 {
		return fmt.Errorf("%s: lines must be positive, or left out for the visible screen", tool)
	}
	return nil
}

// view waits up to wait for the terminal's output to settle and then renders
// what it shows, as the model reads it.
//
// The wait is the only thing the model has to judge, and it judges it loosely:
// a wait ends as soon as output has been quiet for settle, counted from when
// the wait began rather than from whenever the program last wrote, so a
// generous one
// costs nothing when the program is quick, and the result says whether output
// was still arriving so that a wait too short to see the end is something the
// model can tell and read again after.
//
// A run that is stopped ends the wait where it is, and that is reported as the
// cancellation rather than a screen, for the reason bash does: the call did not
// fail on its own terms, and there is no next turn to read it in.
func view(ctx context.Context, t *terminal.Terminal, id int, wait time.Duration, lines int) (string, error) {
	settled := true
	if wait > 0 {
		waiting, cancel := context.WithTimeout(ctx, wait)
		err := t.Wait(waiting, terminal.WaitFor{Idle: settle, After: time.Now()})
		cancel()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		settled = err == nil
	}
	return render(t, id, t.Screen(terminal.ScreenOptions{Lines: lines, MaxChars: maxScreen}), settled), nil
}

// render writes a snapshot out for the model: a line saying what the terminal
// is doing, and then what it shows.
//
// The line comes first so that it is read before the screen and not mistaken
// for part of it. The screen has its blank lines at the bottom left out — on a
// screen with a prompt near the top they are most of it, and say nothing the
// cursor's position does not — and where the cursor is, is said as the line of
// what was returned that it is on, which is the one coordinate the model can
// use without counting.
func render(t *terminal.Terminal, id int, snap terminal.Snapshot, settled bool) string {
	status := t.Status()
	facts := []string{fmt.Sprintf("terminal %d", id)}
	if status.Running {
		facts = append(facts, "running")
	} else {
		facts = append(facts, "the program has "+ended(status))
	}
	if !settled {
		facts = append(facts, "output was still arriving when the wait ended")
	}
	if snap.AltScreen {
		facts = append(facts, "a full-screen program is showing")
	}

	rows := slices.Clone(snap.Rows)
	for len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	shown := fmt.Sprintf("%d of %d lines", len(rows), snap.Total)
	if snap.Truncated {
		shown += ", older ones cut to fit"
	}
	facts = append(facts, shown)
	if line := snap.Cursor.Row - snap.FirstRow; status.Running && snap.Cursor.Visible && line >= 0 && line < len(snap.Rows) {
		facts = append(facts, fmt.Sprintf("cursor on line %d, column %d", line+1, snap.Cursor.Col+1))
	}

	var b strings.Builder
	b.WriteString("[" + strings.Join(facts, "; ") + "]\n")
	b.WriteString(strings.Join(rows, "\n"))
	return b.String()
}

// ended says how a program that is no longer running came to stop.
func ended(status terminal.Status) string {
	switch {
	case status.ExitCode != nil:
		return fmt.Sprintf("exited with status %d", *status.ExitCode)
	case status.ExitSignal != nil:
		return fmt.Sprintf("was ended by signal %d", *status.ExitSignal)
	default:
		return "ended"
	}
}

func orDefault(n, fallback int) int {
	if n > 0 {
		return n
	}
	return fallback
}
