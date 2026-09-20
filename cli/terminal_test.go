package cli

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/ChrisVilches/agi/hooks"
	"github.com/ChrisVilches/agi/model"
	"github.com/ChrisVilches/agi/tools/native"
	"github.com/ChrisVilches/agi/tools/workspace"
	"github.com/ChrisVilches/agi/transport/litellm"
)

// terminalsOf runs the app far enough to read the two terminal flags, and
// returns what terminalFlags made of them.
func terminalsOf(t *testing.T, args ...string) (int, error) {
	t.Helper()
	var most int
	var readErr error
	app := NewApp("test")
	app.Action = func(_ context.Context, cmd *cli.Command) error {
		most, readErr = terminalFlags(cmd)
		return nil
	}
	if err := app.Run(context.Background(), append([]string{"agi"}, args...)); err != nil {
		return 0, err
	}
	return most, readErr
}

// Terminals are off unless asked for, and asked for they come with the
// built-in cap unless --max-terminals says otherwise.
func TestTerminalsAreOffByDefault(t *testing.T) {
	for _, c := range []struct {
		args []string
		want int
	}{
		{[]string{"prompt"}, 0},
		{[]string{"--enable-terminal", "prompt"}, workspace.DefaultMaxTerminals},
		{[]string{"--enable-terminal", "--max-terminals", "3", "prompt"}, 3},
	} {
		got, err := terminalsOf(t, c.args...)
		if err != nil || got != c.want {
			t.Errorf("agi %v gives %d terminals, %v, want %d", c.args, got, err, c.want)
		}
	}
}

// A cap on terminals in a run that has none is a user who believes the run
// has them, and is told it does not rather than quietly ignored.
func TestMaxTerminalsWithoutEnablingThemIsRefused(t *testing.T) {
	_, err := terminalsOf(t, "--max-terminals", "3", "prompt")
	if err == nil || !strings.Contains(err.Error(), "--enable-terminal") {
		t.Fatalf("--max-terminals alone = %v, want it refused, naming --enable-terminal", err)
	}
}

// Fewer than one is a tool that could never open anything, refused while the
// command line is read, before a gateway is chosen.
func TestTooFewTerminalsIsRefusedBeforeTheBackend(t *testing.T) {
	t.Setenv(EnvBackend, "")
	_, err := run(t, "agi", "--enable-terminal", "--max-terminals", "0", "prompt")
	if err == nil || !strings.Contains(err.Error(), "max-terminals") {
		t.Fatalf("--max-terminals 0 = %v, want the cap to be what failed", err)
	}
}

// Asked for properly, the flags get the run as far as needing a gateway.
func TestEnabledTerminalsReachTheBackend(t *testing.T) {
	t.Setenv(EnvBackend, litellm.Name)
	t.Setenv(litellm.EnvURL, "")
	_, err := run(t, "agi", "--enable-terminal", "--max-terminals", "2", "prompt")
	if err == nil || !strings.Contains(err.Error(), litellm.EnvURL) {
		t.Fatalf("agi --enable-terminal = %v, want the flags accepted", err)
	}
}

var terminalTools = []string{workspace.TerminalOpen, workspace.TerminalSend, workspace.TerminalRead, workspace.TerminalClose}

// A run without terminals has none of the four tools, and a run with them has
// all four, and nothing about either reaches the system prompt: they are work
// tools, described by their definitions alone.
func TestTerminalToolsAreRegisteredOnlyWhenEnabled(t *testing.T) {
	without, err := defaultRegistry(wiring{out: quiet(), client: &scriptedClient{}, log: nolog(), maxTurns: defaultMaxTurns, maxSubagents: defaultMaxSubagents})
	if err != nil {
		t.Fatal(err)
	}
	with, err := defaultRegistry(wiring{out: quiet(), client: &scriptedClient{}, log: nolog(), maxTurns: defaultMaxTurns, maxSubagents: defaultMaxSubagents, terminals: workspace.NewTerminals(1)})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range terminalTools {
		if _, ok := without.Lookup(name); ok {
			t.Errorf("%s is registered in a run that did not enable terminals", name)
		}
		if _, ok := with.Lookup(name); !ok {
			t.Errorf("%s is missing from a run that enabled terminals", name)
		}
	}
	if with.SystemPrompt() != without.SystemPrompt() {
		t.Error("enabling terminals changed what the tools put in the system prompt")
	}
}

// Every agent built from a wiring that has terminals gets a set of its own.
func TestEachAgentGetsTerminalsOfItsOwn(t *testing.T) {
	w := wiring{maxTerminals: 2}
	if a, b := w.newTerminals(), w.newTerminals(); a == nil || a == b {
		t.Fatal("two agents were handed the same terminals, or none")
	}
	if (wiring{}).newTerminals() != nil {
		t.Fatal("a run without terminals handed an agent some")
	}
}

// The terminal tools' names are agi's whether or not this run enabled them, so
// a tools.json that works today does not stop working the day somebody passes
// --enable-terminal.
func TestTerminalToolNamesAreReserved(t *testing.T) {
	provided, err := providedNames(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range terminalTools {
		if !provided[name] {
			t.Errorf("%s is not reserved against a user-defined tool", name)
		}
	}
}

// A hook watching the terminal tools could never run in a run without them, so
// it is refused the way any hook naming a tool the run does not have is — and
// accepted once the run has them.
func TestAHookOnTerminalsNeedsThemEnabled(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, hooks.FileName, `{"PreToolUse": [
	  {"matcher": "^terminal_send$", "command": "./guard.sh", "args": []}
	]}`, 0o644)
	write(t, dir, "guard.sh", "#!/bin/sh\nexit 0\n", 0o755)

	if _, err := resourcesOf(t, "--resources", dir, "prompt"); err == nil || !strings.Contains(err.Error(), "could never run") {
		t.Errorf("a terminal hook without --enable-terminal = %v, want it refused as one that could never run", err)
	}
	if _, err := resourcesOf(t, "--enable-terminal", "--resources", dir, "prompt"); err != nil {
		t.Errorf("a terminal hook with --enable-terminal = %v, want it accepted", err)
	}
}

// alive reports whether pid is a process that has not finished, counting a
// zombie — finished and not yet reaped — as finished.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
	return len(fields) > 0 && fields[0] != "Z"
}

func calling(id, tool string, args any) model.AssistantMessage {
	raw, _ := json.Marshal(args)
	return model.AssistantMessage{ToolRequests: []model.ToolRequest{{ID: id, Name: tool, Arguments: raw}}}
}

// lastResult is the text of the last turn a request carried, which is the
// answer to the call the agent made just before it.
func lastResult(req model.Message) string {
	if answer, ok := req.(model.ToolAnswer); ok {
		return answer.Render()
	}
	return ""
}

// A fork is a new agent as far as terminals go: its first terminal is number
// 1 though its parent already had one, it cannot reach its parent's, and
// whatever it opened is closed when it ends — while the parent's terminal is
// still there afterwards.
func TestAForksTerminalsAreItsOwnAndEndWithIt(t *testing.T) {
	client := &scriptedClient{replies: []model.AssistantMessage{
		calling("1", workspace.TerminalOpen, map[string]any{}),
		calling("2", native.ForkTool, map[string]any{"task": "use a terminal"}),
		// The fork:
		calling("3", workspace.TerminalOpen, map[string]any{}),
		calling("4", workspace.TerminalSend, map[string]any{"explanation": "test it",
			"terminal": 1, "text": "sleep 1000 & echo BG=$!", "keys": []string{"ENTER"}, "wait_before_ms": 1000, "wait_after_ms": 1000,
		}),
		{Content: "the fork is done"},
		// The run again:
		calling("5", workspace.TerminalSend, map[string]any{"explanation": "test it",
			"terminal": 1, "text": "echo parent-still-here", "keys": []string{"ENTER"}, "wait_after_ms": 1500,
		}),
		{Content: "all done"},
	}}

	w := wiring{
		out: quiet(), client: client, log: nolog(),
		maxTurns: defaultMaxTurns, maxSubagents: defaultMaxSubagents,
		agents: &native.Counter{}, maxTerminals: 2,
	}
	w.terminals = w.newTerminals()
	defer w.terminals.Close()
	registry, err := defaultRegistry(w)
	if err != nil {
		t.Fatal(err)
	}
	if err := runAgent(context.Background(), quiet(), nolog(), client, registry, asked("q"), nil, defaultMaxTurns, nil); err != nil {
		t.Fatalf("runAgent() = %v", err)
	}

	sent := client.sent
	if len(sent) != 7 {
		t.Fatalf("%d requests were sent, want 7", len(sent))
	}
	forkOpened := lastResult(sent[3].Conversation.Messages[len(sent[3].Conversation.Messages)-1])
	if !strings.HasPrefix(forkOpened, "Opened terminal 1.") {
		t.Errorf("the fork's first terminal said %q, want it numbered 1", forkOpened)
	}
	// The fork started a job in the background of its terminal. Closing the
	// terminal ends the job too, so the job being gone is how the test knows
	// the terminal was closed when the fork ended rather than left open.
	started := lastResult(sent[4].Conversation.Messages[len(sent[4].Conversation.Messages)-1])
	m := regexp.MustCompile(`BG=(\d+)`).FindStringSubmatch(started)
	if m == nil {
		t.Fatalf("the fork's job printed no pid:\n%s", started)
	}
	pid, _ := strconv.Atoi(m[1])
	if alive(pid) {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("the job the fork started in its terminal is still running, so the terminal was not closed when the fork ended")
	}
	afterFork := lastResult(sent[6].Conversation.Messages[len(sent[6].Conversation.Messages)-1])
	if !strings.Contains(afterFork, "\nparent-still-here") {
		t.Errorf("the run's own terminal did not survive the fork:\n%s", afterFork)
	}
}
