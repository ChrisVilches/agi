package terminal_test

// The tests in this package drive real programs on real PTYs — bash, python3,
// vim, top — through nothing but the public API, so they check what a caller
// actually gets rather than what the internals believe they produce. A test
// needing a program that is not installed skips rather than fails.
//
// Each waits for something it can name before it reads the screen, usually the
// next prompt, and none sleeps unless the program redraws on a timer of its
// own, which top does. None loads the machine to provoke a race either: a test
// suite that makes the computer it runs on worse to use is one people stop
// running.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ChrisVilches/agi/terminal"
)

// hostEnv turns the test binary into a host: a process that owns a terminal
// and can be killed by a test, which is the only way to test what happens to
// a terminal whose host dies without calling Close.
const hostEnv = "AGI_TERMINAL_TEST_HOST"

func TestMain(m *testing.M) {
	if mode := os.Getenv(hostEnv); mode != "" {
		runHost(mode)
		return
	}
	os.Exit(m.Run())
}

// runHost starts a shell with a job in the background and prints
// "PIDS <shell> <job>". Then it blocks until it is killed, for "block", or
// exits without calling Close, for "exit".
func runHost(mode string) {
	t, err := terminal.Start(terminal.Options{Command: shellCommand, Env: shellEnv})
	if err != nil {
		fmt.Println("ERR", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t.Wait(ctx, terminal.WaitFor{Text: "$ "})
	t.FeedKeys("sleep 1000 & echo BG=$!\n")
	pattern := regexp.MustCompile(`BG=(\d+)`)
	t.Wait(ctx, terminal.WaitFor{Regex: pattern})
	m := pattern.FindStringSubmatch(t.Text())
	if m == nil {
		fmt.Println("ERR the background job printed no pid")
		os.Exit(1)
	}
	fmt.Printf("PIDS %d %s\n", t.Status().Pid, m[1])
	if mode == "exit" {
		// No Close: the parent-death signal is all that is left to clean up.
		os.Exit(0)
	}
	select {}
}

// startHost runs the test binary as a host and returns it with the pids of its
// shell and of the shell's background job.
func startHost(t *testing.T, mode string) (host *exec.Cmd, shell, job int) {
	t.Helper()
	host = exec.Command(os.Args[0])
	host.Env = append(os.Environ(), hostEnv+"="+mode)
	host.Stderr = os.Stderr
	out, err := host.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "PIDS ") {
		host.Process.Kill()
		t.Fatalf("the host said %q (%v), want its pids", line, err)
	}
	fields := strings.Fields(line)
	return host, atoi(t, fields[1]), atoi(t, fields[2])
}

// shellCommand and shellEnv make a shell that behaves the same on every
// machine: no rc files, a fixed prompt, and no history file to read or write.
var (
	shellCommand = []string{"bash", "--norc", "--noprofile"}
	shellEnv     = []string{"PS1=$ ", "HISTFILE=/dev/null"}
)

// start starts a terminal and closes it when the test ends.
func start(t *testing.T, opts terminal.Options) *terminal.Terminal {
	t.Helper()
	term, err := terminal.Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { term.Close() })
	return term
}

// startShell starts the predictable shell and waits for its first prompt.
func startShell(t *testing.T, opts terminal.Options) *terminal.Terminal {
	t.Helper()
	opts.Command = shellCommand
	opts.Env = append(slices.Clone(shellEnv), opts.Env...)
	term := start(t, opts)
	mustWait(t, term, 5, terminal.WaitFor{Text: "$ "})
	return term
}

func wait(term *terminal.Terminal, seconds float64, w terminal.WaitFor) error {
	ctx, cancel := context.WithTimeout(context.Background(), duration(seconds))
	defer cancel()
	return term.Wait(ctx, w)
}

// mustWait fails the test with the screen as it stood, which is nearly always
// what explains why the wait ran out.
func mustWait(t *testing.T, term *terminal.Terminal, seconds float64, w terminal.WaitFor) {
	t.Helper()
	if err := wait(term, seconds, w); err != nil {
		t.Fatalf("waiting for %+v = %v; the screen:\n%s", w, err, term.Text())
	}
}

// re compiles a pattern in multi-line mode, so that ^ and $ match at each line.
func re(pattern string) *regexp.Regexp { return regexp.MustCompile("(?m)" + pattern) }

func duration(seconds float64) time.Duration { return time.Duration(seconds * float64(time.Second)) }

func feed(t *testing.T, term *terminal.Terminal, keys string) {
	t.Helper()
	if err := term.FeedKeys(keys); err != nil {
		t.Fatal(err)
	}
}

func key(t *testing.T, term *terminal.Terminal, names ...string) {
	t.Helper()
	if err := term.Key(names...); err != nil {
		t.Fatal(err)
	}
}

func screen(term *terminal.Terminal, lines, maxChars int) terminal.Snapshot {
	return term.Screen(terminal.ScreenOptions{Lines: lines, MaxChars: maxChars})
}

// history is the scrollback part of a snapshot.
func history(s terminal.Snapshot) []string { return s.Rows[:max(0, -s.FirstRow)] }

// pidAlive reports whether pid is a process that has not finished. A zombie is
// one that has: it exists only until its parent reaps it, and is counted dead.
func pidAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
		return false
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
	return len(fields) == 0 || fields[0] != "Z"
}

func waitDead(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// need skips the test unless every program it names is installed.
func need(t *testing.T, programs ...string) {
	t.Helper()
	for _, p := range programs {
		if _, err := exec.LookPath(p); err != nil {
			t.Skipf("%s is not installed", p)
		}
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("%q is not a number", s)
	}
	return n
}

// firstMatch returns the first group of the first match of pattern in text.
func firstMatch(t *testing.T, pattern, text string) string {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("nothing matches %s in:\n%s", pattern, text)
	}
	return m[1]
}
