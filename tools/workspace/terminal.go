package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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

	// openWait is how long terminal_open waits before it reads the first
	// screen, so that what it returns usually shows the shell's prompt. It is
	// a fixed time and not a test for a prompt, because nothing on a terminal
	// says when a program is ready: a shell has a prompt, a REPL has another,
	// and a program may draw nothing at all. A terminal that needed longer is
	// one the model reads again, and waits longer before typing next time.
	openWait = time.Second

	// maxWait bounds every wait a call asks for. It is the bound a bash command has, for the same
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
	if id < 1 {
		return nil, fmt.Errorf("%s: there is no terminal %d, because terminals are numbered from 1; "+
			"the terminal parameter is the number in the \"Opened terminal N.\" line %s returned, "+
			"written as a plain integer. Open ones: %s", tool, id, TerminalOpen, ts.listLocked())
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
Open an interactive terminal running bash. The result's first line is
"Opened terminal N.", and N — a plain integer, starting at 1 — is the
terminal's number: pass it as the terminal parameter of %s,
%s and %s. The rest of the result is the terminal's
first screen. The terminal stays open across calls until you close it.

It is a real terminal, and what you read is its screen, not the output of your
last command. Nothing is cleared between calls: earlier commands, their output
and old prompts stay on the screen, move up as new lines arrive, and scroll off
the top into the terminal's history, where reading with lines still finds them.
So text you did not just cause is normal and is not a fault. The newest output
is always at the bottom, below the last prompt; read from there upward.

Use a terminal for what a one-off command cannot do: a program that asks
questions or waits for keys, a session that has to stay open between steps —
ssh to another machine, a database shell, a REPL — or a full-screen program
such as vim, less or top. For a command that runs and finishes on its own,
a terminal is the slow way to do it.

Everything you start in a terminal keeps running between your calls, and all of
it is ended when you close the terminal or when your work ends. Close a
terminal with %s once you are done with it:
at most %d may be open at once.`,
			TerminalSend, TerminalRead, TerminalClose, TerminalClose, o.ts.max)),
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

// Report names the terminal the call opened, under the announcement. Every
// later call is shown by that number, so without this line the first
// "terminal 1" the user sees refers to something nothing on the screen named.
func (o *terminalOpen) Report(result string) string {
	var id int
	if _, err := fmt.Sscanf(result, "Opened terminal %d.", &id); err != nil {
		return ""
	}
	return tools.Detail(fmt.Sprintf("  terminal %d", id))
}

func (o *terminalOpen) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p TerminalOpenParams
	if err := decode(TerminalOpen, args, &p); err != nil {
		return "", err
	}
	id, t, err := o.ts.add(func() (*terminal.Terminal, error) {
		return terminal.Start(terminal.Options{Command: []string{terminalShell}, Cols: p.Cols, Rows: p.Rows})
	})
	if err != nil {
		return "", err
	}
	screen, err := view(ctx, t, id, openWait, 0)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Opened terminal %d.\n%s", id, screen), nil
}

// terminalSend types into a terminal.
type terminalSend struct{ ts *Terminals }

// TerminalSendParams is the input schema for terminal_send.
type TerminalSendParams struct {
	Terminal int `json:"terminal"`
	// Explanation is why the agent is typing this and what it expects to
	// happen. It is required for the reason bash's is: asking for the reason
	// alongside the keys makes the model state its intent before acting, and
	// the reason stays in the conversation, where a later turn reads why the
	// keys were typed and not only what they were — which in a terminal is
	// the harder of the two to reconstruct, since the keys mean whatever the
	// program on the other end makes of them.
	Explanation string `json:"explanation"`
	// Text is typed exactly as written, and Keys are pressed after it.
	Text string   `json:"text,omitempty"`
	Keys []string `json:"keys,omitempty"`
	// WaitBeforeMS is waited before anything is typed, for a program that is
	// still starting — a shell reading a slow rc file, ssh connecting, a REPL
	// loading — and would lose keys sent to it too soon.
	WaitBeforeMS int `json:"wait_before_ms,omitempty"`
	// WaitAfterMS is waited after typing, before the screen is read.
	WaitAfterMS int `json:"wait_after_ms,omitempty"`
	Lines       int `json:"lines,omitempty"`
}

func (s *terminalSend) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: TerminalSend,
		Description: strings.TrimSpace(fmt.Sprintf(`
Type into a terminal opened with %s: text first, then keys.

  {"terminal": 1, "explanation": "list the directory to find the config file",
   "text": "ls -la", "keys": ["ENTER"], "wait_after_ms": 2000}

terminal is the number from the "Opened terminal N." line %s returned,
written as a plain integer (1, not "1"). Only explanation and terminal are
required, but a call must give text, keys, or both.

text is typed exactly as written, character by character. keys is an array of
key names, even for one key, pressed after the text in order: ENTER, TAB, ESC,
BACKSPACE, SPACE, UP, DOWN, LEFT, RIGHT, HOME, END, PAGE_UP, PAGE_DOWN, INSERT,
DELETE, F1 to F12, CTRL_A to CTRL_Z (CTRL_C interrupts, CTRL_D ends input), and
ALT_ followed by a key.
Nothing is run until you press ENTER. Write keys by name rather than as escape
sequences inside text.

wait_before_ms is waited before anything is typed. A program that is still
starting — a shell, ssh connecting, a REPL loading — can lose keys sent to it
too soon; if a screen shows that typed text was lost or arrived before the
program was ready, wait longer before typing next time.

To see what the keys did in the same call, give wait_after_ms, lines, or both:
the call waits wait_after_ms after typing and then returns the screen the way
%s does — the whole screen, earlier commands and their output
included, with what your keys did at the bottom. Give neither and nothing is read: the call only confirms
the keys were sent, and you read the terminal yourself later. Every wait is
waited in full.`,
			TerminalOpen, TerminalOpen, TerminalRead)),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"terminal": terminalParameter(),
				"explanation": map[string]any{
					"type": "string",
					"description": "One sentence, in the second person, saying why you are " +
						"typing this and what you expect it to do. It is shown to the user " +
						"as the keys are sent.",
				},
				"text": map[string]any{
					"type": "string",
					"description": "Text to type, exactly as written. A command in it is not " +
						"run until ENTER is pressed, so add [\"ENTER\"] as keys to run one.",
				},
				"keys": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
					"description": "Key names to press after the text, in order, as an array " +
						"even for one key: [\"ENTER\"], or [\"CTRL_C\"].",
				},
				"wait_before_ms": waitParameter("before typing"),
				"wait_after_ms":  waitParameter("after typing, before reading the screen"),
				"lines":          linesParameter(),
			},
			"required":             []string{"terminal", "explanation"},
			"additionalProperties": false,
		},
	}
}

// parseSend decodes and validates the model's arguments, translating nothing
// yet: the keys are checked by the terminal, which knows the names.
func parseSend(args json.RawMessage) (TerminalSendParams, error) {
	var p TerminalSendParams
	if err := decode(TerminalSend, args, &p); err != nil {
		return p, err
	}
	if strings.TrimSpace(p.Explanation) == "" {
		return p, fmt.Errorf("%s: the explanation parameter is required; say in one sentence why "+
			"you are typing this and what you expect it to do", TerminalSend)
	}
	if p.Text == "" && len(p.Keys) == 0 {
		return p, fmt.Errorf("%s: nothing to send; give text, keys, or both", TerminalSend)
	}
	if err := checkWait(TerminalSend, "wait_before_ms", p.WaitBeforeMS); err != nil {
		return p, err
	}
	if err := checkWait(TerminalSend, "wait_after_ms", p.WaitAfterMS); err != nil {
		return p, err
	}
	return p, checkLines(TerminalSend, p.Lines)
}

// Announce shows which terminal and why, and then what is about to be typed,
// in full and dimmed so the reason reads first — the shape bash's has. Like a
// bash command, the keys are the thing about to happen to the user's machine,
// and the part worth watching for is as likely to be at the end as at the
// start. Both waits are said on the first line, dimmed, every time and each
// named for which side of the typing it is on, because they are the one
// judgement the call asked of the model and why the line may sit there a while
// before anything happens — or before anything comes back.
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
	head := fmt.Sprintf("terminal %d: %s", p.Terminal, strings.TrimSpace(p.Explanation))
	head += tools.Detail(fmt.Sprintf(" (wait %s before typing, %s after)", millis(p.WaitBeforeMS), millis(p.WaitAfterMS)))
	return head + "\n" + tools.Detail(block(">", strings.Join(typed, " ")))
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
	if err := pause(ctx, time.Duration(p.WaitBeforeMS)*time.Millisecond); err != nil {
		return "", err
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
	if p.WaitAfterMS == 0 && p.Lines == 0 {
		return fmt.Sprintf("Sent to terminal %d.", p.Terminal), nil
	}
	return view(ctx, t, p.Terminal, time.Duration(p.WaitAfterMS)*time.Millisecond, p.Lines)
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
Read what a terminal opened with %s is showing. Nothing is typed.

  {"terminal": 1, "lines": 60, "wait_ms": 5000}

terminal is the number from the "Opened terminal N." line %s returned,
written as a plain integer (1, not "1").

Without lines you get the visible screen. With lines you get that many lines
counted up from the bottom, reaching past the top of the screen into what has
scrolled off it, so lines is how you look further back. Blank lines at the
bottom are left out. Either way the lines are the terminal's and not one
command's: whatever was printed before is still there above the newest output,
which is always at the bottom, and it scrolls into history rather than being
cleared. A full-screen program such as vim draws on a screen of its
own and has no history, so for one of those only the visible screen exists.

wait_ms is how long to wait before reading, for a command that is still
working, and the whole of it is waited whatever the program does meanwhile.
Choose it from how long you expect the command to take. If what you read shows
the program has not finished, read again.`, TerminalOpen, TerminalOpen)),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"terminal": terminalParameter(),
				"wait_ms":  waitParameter("before reading the screen"),
				"lines":    linesParameter(),
			},
			"required":             []string{"terminal"},
			"additionalProperties": false,
		},
	}
}

func parseRead(args json.RawMessage) (TerminalReadParams, error) {
	var p TerminalReadParams
	if err := decode(TerminalRead, args, &p); err != nil {
		return p, err
	}
	if err := checkWait(TerminalRead, "wait_ms", p.WaitMS); err != nil {
		return p, err
	}
	return p, checkLines(TerminalRead, p.Lines)
}

// Announce says which terminal, how much of it, and how long the call waits
// before reading, every time, since the wait is the one judgement the call
// asked of the model.
func (r *terminalRead) Announce(args json.RawMessage) string {
	p, err := parseRead(args)
	if err != nil {
		return ""
	}
	what := "the screen"
	if p.Lines > 0 {
		what = fmt.Sprintf("the last %d lines", p.Lines)
	}
	return fmt.Sprintf("terminal %d, %s", p.Terminal, what) +
		tools.Detail(fmt.Sprintf(" (wait %s before reading)", millis(p.WaitMS)))
}

// millis spells a wait the model gave in milliseconds the way a person reads
// one: 1.5s, 250ms, 0s.
func millis(ms int) time.Duration {
	return time.Duration(ms) * time.Millisecond
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
Close a terminal opened with %s, ending the program in it and
everything that program started, including anything left running in the background.

  {"terminal": 1}

terminal is the number from the "Opened terminal N." line %s returned,
written as a plain integer (1, not "1"). Close a terminal as soon as you are
done with it: at most %d may be open at once. A closed terminal's number is not
used again, and a terminal opened later gets a new one.`, TerminalOpen, TerminalOpen, c.ts.max)),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"terminal": terminalParameter(),
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
	if err := decode(TerminalClose, args, &p); err != nil {
		return "", err
	}
	t, err := c.ts.remove(p.Terminal)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Closed terminal %d; its program %s.", p.Terminal, ended(t.Close())), nil
}

// decode reads a call's arguments into p, and says in words what was wrong
// with them when they do not fit, since the decoder's own message names Go
// types the model has never seen.
//
// Two mistakes are worth spelling out because they are the ones models make.
// A number written as a string — "terminal": "1" — is refused naming the
// parameter and the form it takes. A parameter the tool does not have is
// refused rather than ignored, because ignoring it is worse than an error:
// "id": 1 in place of "terminal": 1 would otherwise decode as terminal 0 and be
// refused as a terminal that does not exist, which sends the model looking for
// the wrong mistake. It also makes the schema's additionalProperties true.
func decode(tool string, args json.RawMessage, p any) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	err := dec.Decode(p)
	if err == nil {
		return nil
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return fmt.Errorf("%s: the %q parameter must be %s, and it was given a JSON %s",
			tool, typeErr.Field, jsonForm(typeErr.Type), typeErr.Value)
	}
	if field, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		return fmt.Errorf("%s: there is no parameter %s; the parameters are %s",
			tool, field, strings.Join(parameterNames(p), ", "))
	}
	return fmt.Errorf("%s: arguments are not valid JSON: %w", tool, err)
}

// jsonForm says how a value of type t is written in JSON, for a model that
// wrote it some other way.
func jsonForm(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Int, reflect.Int64, reflect.Int32:
		return "a plain integer without quotes, such as 1 rather than \"1\""
	case reflect.String:
		return "a string, in quotes"
	case reflect.Slice:
		return "an array, such as [\"ENTER\"], even when it holds one item"
	default:
		return "a JSON " + t.Kind().String()
	}
}

// parameterNames lists the JSON names of the fields p decodes into, in the
// order the struct declares them.
func parameterNames(p any) []string {
	t := reflect.TypeOf(p).Elem()
	names := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		names = append(names, name)
	}
	return names
}

// terminalParameter, waitParameter and linesParameter are written once because
// every terminal number, wait and line count means the same thing in each tool
// that takes one.
func terminalParameter() map[string]any {
	return map[string]any{
		"type": "integer",
		"description": "Which terminal: the number N from the \"Opened terminal N.\" line " +
			TerminalOpen + " returned, as a plain integer such as 1. Terminals are numbered from 1.",
	}
}

func waitParameter(when string) map[string]any {
	return map[string]any{
		"type": "integer",
		"description": fmt.Sprintf("Milliseconds to wait %s, from 0 to %d. All of it is waited. Leave it out for no wait.",
			when, maxWait.Milliseconds()),
	}
}

func linesParameter() map[string]any {
	return map[string]any{
		"type": "integer",
		"description": "How many lines to return, counted up from the last line, reaching into " +
			"what has scrolled off the top. Leave it out for the visible screen.",
	}
}

// checkWait and checkLines refuse what a call could not mean. A negative number
// is refused rather than read as the default, since a model that wrote one
// meant something by it and is better told it is not a thing.
func checkWait(tool, name string, ms int) error {
	if ms < 0 || time.Duration(ms)*time.Millisecond > maxWait {
		return fmt.Errorf("%s: %s must be between 0 and %d", tool, name, maxWait.Milliseconds())
	}
	return nil
}

func checkLines(tool string, lines int) error {
	if lines < 0 {
		return fmt.Errorf("%s: lines must be positive, or left out for the visible screen", tool)
	}
	return nil
}

// pause waits for d, or until the run is stopped, which it reports as the
// cancellation: the call did not fail on its own terms, and there is no next
// turn to say anything in, which is how bash reports one too.
func pause(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// view waits for wait and then renders what the terminal shows, as the model
// reads it.
//
// The wait is the whole of wait and never less. Ending it early when output
// stops would be a guess that a quiet program is a finished one, and the guess
// is wrong exactly when it matters: a program that prints a line, sleeps, and
// prints the line that was being waited for. The model chose the time, and is
// the only one that knows what the program is doing.
//
// A run that is stopped ends the wait where it is; see pause.
func view(ctx context.Context, t *terminal.Terminal, id int, wait time.Duration, lines int) (string, error) {
	if err := pause(ctx, wait); err != nil {
		return "", err
	}
	return render(t, id, t.Screen(terminal.ScreenOptions{Lines: lines, MaxChars: maxScreen})), nil
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
func render(t *terminal.Terminal, id int, snap terminal.Snapshot) string {
	status := t.Status()
	facts := []string{fmt.Sprintf("terminal %d", id)}
	if status.Running {
		facts = append(facts, "running")
	} else {
		facts = append(facts, "the program has "+ended(status))
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
