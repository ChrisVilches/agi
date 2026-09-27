package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ChrisVilches/agi/tools"
)

// terminalTools returns an agent's terminals and its four tools by name, with
// every terminal closed when the test ends.
func terminalTools(t *testing.T, limit int) (*Terminals, map[string]tools.Tool) {
	t.Helper()
	ts := NewTerminals(limit)
	t.Cleanup(ts.Close)
	byName := map[string]tools.Tool{}
	for _, tool := range ts.Tools() {
		byName[tool.Definition().Name] = tool
	}
	return ts, byName
}

func call(t *testing.T, tool tools.Tool, args any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return tool.Call(context.Background(), raw)
}

func mustCall(t *testing.T, tool tools.Tool, args any) string {
	t.Helper()
	out, err := call(t, tool, args)
	if err != nil {
		t.Fatalf("%s(%v) = %v", tool.Definition().Name, args, err)
	}
	return out
}

// open opens a terminal and returns its number, which the tests read back out
// of the result the way the model does.
func open(t *testing.T, byName map[string]tools.Tool) int {
	t.Helper()
	out := mustCall(t, byName[TerminalOpen], map[string]any{})
	var id int
	if _, err := fmt.Sscanf(out, "Opened terminal %d.", &id); err != nil {
		t.Fatalf("terminal_open said %q, want it to say which terminal it opened", out)
	}
	// The tool waits a fixed second and promises nothing about a prompt, so
	// a test that is about to type does what a model is told to do when keys
	// get lost: look first. Polling here keeps the tests from depending on how
	// fast bash starts on whichever machine runs them.
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		screen := mustCall(t, byName[TerminalRead], map[string]any{"terminal": id})
		if _, rows, _ := strings.Cut(screen, "]\n"); strings.TrimSpace(rows) != "" {
			return id
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the shell drew nothing within 10s")
	return 0
}

// The terminal tools are workspace tools: what the model learns about them is
// their descriptions and nothing in the system prompt, since a run may not
// have them at all.
func TestTerminalToolsAreNotSystemPrompters(t *testing.T) {
	for _, tool := range NewTerminals(1).Tools() {
		if _, ok := tool.(tools.SystemPrompter); ok {
			t.Errorf("%s implements SystemPrompter, but only native tools may", tool.Definition().Name)
		}
		if _, ok := tool.(tools.Announcer); !ok {
			t.Errorf("%s does not announce its calls", tool.Definition().Name)
		}
	}
}

// The ordinary use: type a command, press Enter, read what it printed — and a
// terminal stays open between calls, so what one call set is there for the
// next.
func TestTerminalKeepsStateAcrossCalls(t *testing.T) {
	_, byName := terminalTools(t, 2)
	id := open(t, byName)
	if id != 1 {
		t.Fatalf("the first terminal is %d, want 1", id)
	}
	mustCall(t, byName[TerminalSend], map[string]any{"explanation": "test it", "terminal": id, "text": "export MARK=kept-$((40+2))", "keys": []string{"ENTER"}})
	out := mustCall(t, byName[TerminalSend], map[string]any{"explanation": "test it",
		"terminal": id, "text": "echo $MARK", "keys": []string{"ENTER"}, "wait_after_ms": 1500,
	})
	if !strings.Contains(out, "\nkept-42") {
		t.Fatalf("the second call did not see what the first one set:\n%s", out)
	}
	if !strings.HasPrefix(out, "[terminal 1; running;") {
		t.Errorf("the result does not open with what the terminal is doing:\n%s", out)
	}
}

// A send with nothing to read says only that it was sent, so a model that
// wants the screen has to ask for it and one that does not pays nothing.
func TestTerminalSendWithoutReadingOnlySaysSo(t *testing.T) {
	_, byName := terminalTools(t, 1)
	id := open(t, byName)
	out := mustCall(t, byName[TerminalSend], map[string]any{"explanation": "test it", "terminal": id, "text": "true", "keys": []string{"ENTER"}})
	if out != "Sent to terminal 1." {
		t.Fatalf("result = %q, want only that it was sent", out)
	}
}

// lines reaches past the top of the screen into what scrolled off it.
func TestTerminalReadReachesIntoScrollback(t *testing.T) {
	_, byName := terminalTools(t, 1)
	id := open(t, byName)
	mustCall(t, byName[TerminalSend], map[string]any{"explanation": "test it", "terminal": id, "text": "seq 1 200", "keys": []string{"ENTER"}, "wait_after_ms": 1500})
	screen := mustCall(t, byName[TerminalRead], map[string]any{"terminal": id})
	if strings.Contains(screen, "\n150\n") {
		t.Fatalf("150 is on the visible screen, so the test proves nothing:\n%s", screen)
	}
	back := mustCall(t, byName[TerminalRead], map[string]any{"terminal": id, "lines": 100})
	if !strings.Contains(back, "\n150\n") {
		t.Fatalf("100 lines back does not reach 150:\n%s", back)
	}
}

// Numbers count up from 1 and are never handed out again, which is what lets
// a closed terminal be told apart from one that never existed. The two are
// refused in different words because they are corrected differently.
func TestTerminalIDsAreNeverReused(t *testing.T) {
	_, byName := terminalTools(t, 2)
	first := open(t, byName)
	mustCall(t, byName[TerminalClose], map[string]any{"terminal": first})
	if second := open(t, byName); second != 2 {
		t.Fatalf("the terminal opened after closing 1 is %d, want 2", second)
	}

	_, err := call(t, byName[TerminalRead], map[string]any{"terminal": first})
	if err == nil || !strings.Contains(err.Error(), "was closed") {
		t.Errorf("reading a closed terminal = %v, want it said to have been closed", err)
	}
	_, err = call(t, byName[TerminalSend], map[string]any{"explanation": "test it", "terminal": 7, "text": "x"})
	if err == nil || !strings.Contains(err.Error(), "there is no terminal 7") ||
		!strings.Contains(err.Error(), "open ones: 2") || !strings.Contains(err.Error(), "run that has ended") {
		t.Errorf("sending to a terminal that never existed = %v, want it to say so, list "+
			"the open ones, and mention that terminals do not outlive a run", err)
	}
	_, err = call(t, byName[TerminalClose], map[string]any{"terminal": first})
	if err == nil || !strings.Contains(err.Error(), "was closed") {
		t.Errorf("closing a closed terminal twice = %v, want it said to have been closed", err)
	}
}

// The cap is on terminals open at once, not on terminals ever opened, and the
// refusal says which are open so that one can be closed.
func TestTerminalOpenStopsAtTheCap(t *testing.T) {
	_, byName := terminalTools(t, 2)
	open(t, byName)
	open(t, byName)
	_, err := call(t, byName[TerminalOpen], map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "Open: 1, 2") {
		t.Fatalf("a third terminal with a cap of 2 = %v, want it refused, listing 1 and 2", err)
	}
	mustCall(t, byName[TerminalClose], map[string]any{"terminal": 1})
	if id := open(t, byName); id != 3 {
		t.Fatalf("after closing one, the next opened is %d, want 3", id)
	}
}

// A program that exits leaves its terminal readable — its last screen is often
// the reason it exited — while typing into it is refused, saying so.
func TestTerminalWhoseProgramExited(t *testing.T) {
	_, byName := terminalTools(t, 1)
	id := open(t, byName)
	out := mustCall(t, byName[TerminalSend], map[string]any{"explanation": "test it", "terminal": id, "text": "echo goodbye; exit 3", "keys": []string{"ENTER"}, "wait_after_ms": 1500})
	if !strings.Contains(out, "exited with status 3") || !strings.Contains(out, "goodbye") {
		t.Fatalf("the screen after the shell exited:\n%s", out)
	}
	_, err := call(t, byName[TerminalSend], map[string]any{"explanation": "test it", "terminal": id, "text": "x"})
	if err == nil || !strings.Contains(err.Error(), "exited with status 3") || !strings.Contains(err.Error(), TerminalRead) {
		t.Fatalf("typing into an exited terminal = %v, want it refused, pointing at %s", err, TerminalRead)
	}
	if closed := mustCall(t, byName[TerminalClose], map[string]any{"terminal": id}); !strings.Contains(closed, "exited with status 3") {
		t.Errorf("closing it said %q, want how the program had ended", closed)
	}
}

// A key name the terminal does not know sends nothing, not even the text
// before it, so the corrected call is not typed on top of half of this one.
func TestTerminalSendWithAnUnknownKeySendsNothing(t *testing.T) {
	_, byName := terminalTools(t, 1)
	id := open(t, byName)
	_, err := call(t, byName[TerminalSend], map[string]any{"explanation": "test it", "terminal": id, "text": "echo half-typed", "keys": []string{"ENTRE"}})
	if err == nil || !strings.Contains(err.Error(), `"ENTRE"`) || !strings.Contains(err.Error(), "ENTER") {
		t.Fatalf("an unknown key = %v, want it named and the real names listed", err)
	}
	screen := mustCall(t, byName[TerminalRead], map[string]any{"terminal": id, "wait_ms": 500})
	if strings.Contains(screen, "half-typed") {
		t.Fatalf("the text was typed although the call was refused:\n%s", screen)
	}
}

func TestTerminalRefusesCallsItCannotDo(t *testing.T) {
	_, byName := terminalTools(t, 1)
	id := open(t, byName)
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{TerminalSend, map[string]any{"explanation": "test it", "terminal": id}, "nothing to send"},
		{TerminalRead, map[string]any{"terminal": id, "lines": -1}, "lines must be positive"},
		{TerminalRead, map[string]any{"terminal": id, "wait_ms": maxWait.Milliseconds() + 1}, "wait_ms must be"},
		{TerminalSend, map[string]any{"explanation": "test it", "terminal": id, "text": "x", "wait_after_ms": -5}, "wait_after_ms must be"},
		{TerminalSend, map[string]any{"explanation": "test it", "terminal": id, "text": "x", "wait_before_ms": maxWait.Milliseconds() + 1}, "wait_before_ms must be"},
	} {
		if _, err := call(t, byName[c.tool], c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s(%v) = %v, want an error saying %q", c.tool, c.args, err, c.want)
		}
	}
}

// A stopped run ends a wait where it is and reports the cancellation, which is
// how bash reports one too: the call did not fail on its own terms.
func TestTerminalReadIsCancelledWithTheRun(t *testing.T) {
	_, byName := terminalTools(t, 1)
	id := open(t, byName)
	mustCall(t, byName[TerminalSend], map[string]any{"explanation": "test it", "terminal": id, "text": "while :; do echo busy; sleep 0.1; done", "keys": []string{"ENTER"}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw, _ := json.Marshal(map[string]any{"terminal": id, "wait_ms": 60000})
	if _, err := byName[TerminalRead].Call(ctx, raw); !errors.Is(err, context.Canceled) {
		t.Fatalf("a read in a cancelled run = %v, want %v", err, context.Canceled)
	}
}

// A wait is the whole of what was asked for, quiet or not: a program that
// prints, sleeps, and prints again is read after its second line, which a wait
// ending at the first silence would miss.
func TestTerminalWaitsTheWholeTime(t *testing.T) {
	_, byName := terminalTools(t, 1)
	id := open(t, byName)
	started := time.Now()
	out := mustCall(t, byName[TerminalSend], map[string]any{"explanation": "test it",
		"terminal": id, "text": "echo first; sleep 1; echo second", "keys": []string{"ENTER"}, "wait_after_ms": 2000,
	})
	if elapsed := time.Since(started); elapsed < 2*time.Second {
		t.Errorf("the call returned after %s, want the whole 2s waited", elapsed)
	}
	if !strings.Contains(out, "\nsecond") {
		t.Fatalf("the wait ended before the output that came after a pause:\n%s", out)
	}
}

// wait_before_ms is waited before anything is typed, which is what it is for: a
// program still starting would lose keys sent to it too soon.
func TestTerminalSendWaitsBeforeTyping(t *testing.T) {
	_, byName := terminalTools(t, 1)
	id := open(t, byName)
	started := time.Now()
	out := mustCall(t, byName[TerminalSend], map[string]any{"explanation": "test it",
		"terminal": id, "text": "echo typed-at-$SECONDS", "keys": []string{"ENTER"},
		"wait_before_ms": 1000, "wait_after_ms": 500,
	})
	if elapsed := time.Since(started); elapsed < 1500*time.Millisecond {
		t.Errorf("the call returned after %s, want both waits waited", elapsed)
	}
	if !strings.Contains(out, "\ntyped-at-") {
		t.Fatalf("the text was not typed after the wait:\n%s", out)
	}
}

// Closing an agent's terminals ends what runs in them, and a nil set — an
// agent given none — closes without anything to do.
func TestTerminalsCloseEndsEverything(t *testing.T) {
	var none *Terminals
	none.Close()

	ts, byName := terminalTools(t, 2)
	open(t, byName)
	open(t, byName)
	ts.Close()
	if _, err := call(t, byName[TerminalRead], map[string]any{"terminal": 1}); err == nil {
		t.Fatal("a terminal can still be read after its agent's terminals were closed")
	}
}

// The announcement says which terminal and why, then everything about to be
// typed, keys included, since that is what is about to happen to the machine.
func TestTerminalSendAnnouncesWhyAndWhatIsTyped(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"terminal": 2, "explanation": "clear out the old build", "text": "rm -rf build", "keys": []string{"ENTER"},
	})
	got := NewTerminals(1).Tools()[1].(tools.Announcer).Announce(raw)
	first, rest, _ := strings.Cut(got, "\n")
	if first != "terminal 2: clear out the old build (wait 0s before typing, 0s after)" {
		t.Errorf("the first line = %q, want the terminal, the reason and both waits", first)
	}
	if !strings.Contains(rest, "rm -rf build [ENTER]") {
		t.Errorf("the rest = %q, want everything typed", rest)
	}
}

// Both waits are shown on every send, each named for its side of the typing,
// and a read shows its own, since how long to wait is the one judgement a call
// asks of the model.
func TestTerminalAnnouncementsShowTheWaits(t *testing.T) {
	ts := NewTerminals(1).Tools()
	send, _ := json.Marshal(map[string]any{
		"terminal": 1, "explanation": "connect", "text": "ssh host", "wait_before_ms": 1500, "wait_after_ms": 250,
	})
	first, _, _ := strings.Cut(ts[1].(tools.Announcer).Announce(send), "\n")
	if !strings.HasSuffix(first, "(wait 1.5s before typing, 250ms after)") {
		t.Errorf("the send's first line = %q, want both waits, before and after", first)
	}
	read, _ := json.Marshal(map[string]any{"terminal": 1, "wait_ms": 2000})
	if got := ts[2].(tools.Announcer).Announce(read); got != "terminal 1, the screen (wait 2s before reading)" {
		t.Errorf("the read's announcement = %q, want its wait", got)
	}
	read, _ = json.Marshal(map[string]any{"terminal": 1})
	if got := ts[2].(tools.Announcer).Announce(read); got != "terminal 1, the screen (wait 0s before reading)" {
		t.Errorf("a read with no wait = %q, want the wait shown anyway", got)
	}
}

// Opening reports the number it handed out, since every later call is shown by
// that number.
func TestTerminalOpenReportsItsNumber(t *testing.T) {
	r, ok := NewTerminals(1).Tools()[0].(tools.Reporter)
	if !ok {
		t.Fatal("terminal_open does not report what it opened")
	}
	if got := r.Report("Opened terminal 3.\n[terminal 3; running]"); !strings.Contains(got, "terminal 3") {
		t.Errorf("Report = %q, want the terminal's number", got)
	}
	if got := r.Report("something else"); got != "" {
		t.Errorf("Report of a result it does not recognize = %q, want nothing", got)
	}
}

// A send without a reason is refused, the way a bash command without one is.
func TestTerminalSendNeedsAnExplanation(t *testing.T) {
	_, byName := terminalTools(t, 1)
	_, err := call(t, byName[TerminalSend], map[string]any{"terminal": 1, "text": "ls", "explanation": "  "})
	if err == nil || !strings.Contains(err.Error(), "explanation") {
		t.Fatalf("a send with no explanation = %v, want it refused, naming the parameter", err)
	}
	if def := byName[TerminalSend].Definition(); !slices.Contains(def.Parameters["required"].([]string), "explanation") {
		t.Errorf("explanation is not among the required parameters: %v", def.Parameters["required"])
	}
}

// The mistakes models make with a call's arguments are refused in words that
// say how to correct them, rather than in the decoder's Go type names — and a
// parameter the tool does not have is refused rather than ignored, since "id"
// in place of "terminal" would otherwise be read as terminal 0.
func TestTerminalExplainsMalformedArguments(t *testing.T) {
	_, byName := terminalTools(t, 1)
	id := open(t, byName)
	for _, c := range []struct {
		tool string
		args string
		want []string
	}{
		{TerminalSend, `{"terminal": "1", "explanation": "x", "text": "ls"}`,
			[]string{`"terminal" parameter must be a plain integer`, `1 rather than "1"`, "given a JSON string"}},
		{TerminalSend, `{"terminal": 1, "explanation": "x", "keys": "ENTER"}`,
			[]string{`"keys" parameter must be an array`}},
		{TerminalRead, `{"id": 1}`,
			[]string{`no parameter "id"`, "terminal, wait_ms, lines"}},
		{TerminalClose, `{"terminal": 1.5}`,
			[]string{`"terminal" parameter must be a plain integer`}},
		{TerminalOpen, `{oops`, []string{"not valid JSON"}},
	} {
		_, err := byName[c.tool].Call(context.Background(), json.RawMessage(c.args))
		for _, want := range c.want {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s(%s) = %v, want an error saying %q", c.tool, c.args, err, want)
			}
		}
		if err != nil && strings.Contains(err.Error(), "Go struct") {
			t.Errorf("%s(%s) = %v, which names Go types the model never saw", c.tool, c.args, err)
		}
	}
	// None of those calls touched the terminal that was open.
	mustCall(t, byName[TerminalRead], map[string]any{"terminal": id})
}

// A terminal number below 1 is never one that was handed out, and is refused
// saying where the real number comes from — which is also what a call that
// left the parameter out is told, since it decodes as 0.
func TestTerminalRefusesNumbersBelowOne(t *testing.T) {
	_, byName := terminalTools(t, 1)
	open(t, byName)
	for _, args := range []map[string]any{{"terminal": 0}, {}} {
		_, err := call(t, byName[TerminalRead], args)
		if err == nil || !strings.Contains(err.Error(), "numbered from 1") || !strings.Contains(err.Error(), "Open ones: 1") {
			t.Errorf("terminal_read(%v) = %v, want it to say terminals start at 1 and list the open ones", args, err)
		}
	}
}

// The descriptions say how many terminals may be open, rather than "a few".
func TestTerminalDescriptionsNameTheCap(t *testing.T) {
	for _, tool := range NewTerminals(3).Tools() {
		def := tool.Definition()
		if def.Name != TerminalOpen && def.Name != TerminalClose {
			continue
		}
		if !strings.Contains(def.Description, "at most 3 may be open") {
			t.Errorf("%s's description does not name the cap:\n%s", def.Name, def.Description)
		}
	}
}
