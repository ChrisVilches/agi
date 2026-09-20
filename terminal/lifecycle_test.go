package terminal_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ChrisVilches/agi/terminal"
)

// backgroundSleep starts a sleep in the background of the shell and returns
// its pid.
func backgroundSleep(t *testing.T, term *terminal.Terminal) int {
	t.Helper()
	feed(t, term, "sleep 1000 & echo BG=$!\n")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`BG=\d+`)})
	return atoi(t, firstMatch(t, `BG=(\d+)`, term.Text()))
}

func TestStatusReportsTheRunningProgram(t *testing.T) {
	term := startShell(t, terminal.Options{})
	st := term.Status()
	if !st.Running || !pidAlive(st.Pid) || !slices.Equal(st.Command, shellCommand) || st.ExitCode != nil {
		t.Fatalf("status = %+v, want the shell running with no exit code", st)
	}
}

func TestTheDefaultCommandIsTheUsersShell(t *testing.T) {
	sh := os.Getenv("SHELL")
	if sh == "" {
		t.Skip("$SHELL is not set")
	}
	term := start(t, terminal.Options{})
	if got := term.Status().Command; !slices.Equal(got, []string{sh}) {
		t.Fatalf("the default command = %q, want [%s]", got, sh)
	}
}

// Env reaches the program laid over the host's environment, TERM is set, and
// Dir is where the program starts.
func TestEnvAndDirReachTheProgram(t *testing.T) {
	dir := t.TempDir()
	term := startShell(t, terminal.Options{Dir: dir, Env: []string{"GREETING=hi there"}})
	feed(t, term, "echo \"[$GREETING] [$TERM]\"; pwd\n")
	mustWait(t, term, 5, terminal.WaitFor{Text: "[hi there] [xterm-256color]"})
	mustWait(t, term, 5, terminal.WaitFor{Regex: re("^" + strings.ReplaceAll(dir, ".", `\.`) + "$")})
}

// Close ends the shell and every job it started, in the foreground and in the
// background alike, and leaves the screen readable and input refused.
func TestCloseEndsTheShellAndItsJobs(t *testing.T) {
	term := startShell(t, terminal.Options{})
	shell := term.Status().Pid
	background := backgroundSleep(t, term)
	// A foreground job is put in a process group of its own, which is the
	// reason Close signals every group in the session.
	feed(t, term, "sleep 1000\n")
	wait(term, 5, terminal.WaitFor{Idle: duration(0.3)})
	if st := term.Close(); st.Running {
		t.Fatalf("status after Close = %+v, want it not running", st)
	}
	if !waitDead(shell, 5*time.Second) || !waitDead(background, 5*time.Second) {
		t.Fatal("the shell or its background job survived Close")
	}
	if err := term.FeedKeys("x"); !errors.Is(err, terminal.ErrNotRunning) {
		t.Fatalf("FeedKeys after Close = %v, want %v", err, terminal.ErrNotRunning)
	}
	if term.Text() == "" {
		t.Fatal("the screen is empty after Close, want it still readable")
	}
}

// bash passes SIGHUP on to its jobs, but a job that ignores it and SIGTERM —
// the way anything run under nohup does — is only ended because Close goes on
// to SIGKILL every group in the session. Making signalSession signal the shell
// alone is what this test is there to catch.
func TestCloseEndsAJobThatIgnoresHangup(t *testing.T) {
	term := startShell(t, terminal.Options{})
	feed(t, term, "(trap '' HUP TERM; exec sleep 1000) & echo BG=$!\n")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`BG=\d+`)})
	pid := atoi(t, firstMatch(t, `BG=(\d+)`, term.Text()))
	term.Close()
	if !waitDead(pid, 5*time.Second) {
		t.Fatal("a job ignoring SIGHUP and SIGTERM survived Close")
	}
}

// A job suspended with Ctrl-Z acts on no signal until it is continued, which
// is why Close sends SIGCONT beside each one.
func TestCloseEndsAStoppedJob(t *testing.T) {
	term := startShell(t, terminal.Options{})
	feed(t, term, "sleep 1000\n")
	wait(term, 5, terminal.WaitFor{Idle: duration(0.3)})
	key(t, term, "CTRL_Z")
	mustWait(t, term, 5, terminal.WaitFor{Text: "Stopped"})
	feed(t, term, "jobs -p\n")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^\d+$`)})
	pid := atoi(t, firstMatch(t, `(?m)^(\d+)$`, term.Text()))
	term.Close()
	if !waitDead(pid, 5*time.Second) {
		t.Fatal("a stopped job survived Close")
	}
}

func TestCloseIsIdempotentAndSafeConcurrently(t *testing.T) {
	term := startShell(t, terminal.Options{})
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			term.Close()
		}()
	}
	wg.Wait()
	if st := term.Close(); st.Running {
		t.Fatalf("status = %+v, want it not running", st)
	}
}

// A program that exits by itself reports how, leaves its last screen readable,
// and refuses input from then on.
func TestAProgramThatExitsReportsItsStatus(t *testing.T) {
	term := startShell(t, terminal.Options{})
	feed(t, term, "exit 3\n")
	mustWait(t, term, 5, terminal.WaitFor{Exit: true})
	select {
	case <-term.Done():
	default:
		t.Fatal("Done is not closed after the program exited")
	}
	st := term.Status()
	if st.Running || st.ExitCode == nil || *st.ExitCode != 3 || st.ExitSignal != nil {
		t.Fatalf("status = %+v, want exit code 3", st)
	}
	if !strings.Contains(term.Text(), "exit 3") {
		t.Fatalf("text = %q, want the last screen", term.Text())
	}
	if err := term.FeedKeys("x"); !errors.Is(err, terminal.ErrNotRunning) {
		t.Fatalf("FeedKeys after the exit = %v, want %v", err, terminal.ErrNotRunning)
	}
}

// Done means the output is on the screen, not merely that the program exited:
// a program that writes and exits at once has to be read whole.
func TestDoneMeansTheOutputIsRendered(t *testing.T) {
	term := start(t, terminal.Options{Command: []string{"sh", "-c", "printf 'one shot'; exit 7"}})
	select {
	case <-term.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done was never closed")
	}
	if term.Text() != "one shot" || *term.Status().ExitCode != 7 {
		t.Fatalf("text = %q, status = %+v, want %q and exit code 7", term.Text(), term.Status(), "one shot")
	}
}

// A program ended by a signal reports the signal and no exit code, since being
// killed and failing are different answers to why it is gone.
func TestAProgramKilledByASignalReportsIt(t *testing.T) {
	term := start(t, terminal.Options{Command: []string{"sleep", "1000"}})
	syscall.Kill(term.Status().Pid, syscall.SIGTERM)
	mustWait(t, term, 5, terminal.WaitFor{Exit: true})
	st := term.Status()
	if st.ExitSignal == nil || *st.ExitSignal != int(syscall.SIGTERM) || st.ExitCode != nil {
		t.Fatalf("status = %+v, want SIGTERM and no exit code", st)
	}
}

func TestAProgramThatCannotStartIsAnError(t *testing.T) {
	_, err := terminal.Start(terminal.Options{Command: []string{"/nonexistent/program"}})
	if err == nil || !strings.Contains(err.Error(), "/nonexistent/program") {
		t.Fatalf("Start = %v, want an error naming the program", err)
	}
}

func TestStartRefusesInvalidOptions(t *testing.T) {
	for _, opts := range []terminal.Options{
		{Cols: -1},
		{Rows: terminal.MaxDim + 1},
		{Scrollback: -5},
		{Scrollback: terminal.MaxScrollback + 1},
		{Env: []string{"NOEQUALS"}},
	} {
		if term, err := terminal.Start(opts); err == nil {
			term.Close()
			t.Errorf("Start(%+v) succeeded, want it refused", opts)
		}
	}
}

// A call that is refused, or a wait that runs out, leaves the terminal as
// usable as it was.
func TestInvalidCallsAreRefusedAndHarmless(t *testing.T) {
	term := startShell(t, terminal.Options{})
	if err := term.Resize(0, 10); err == nil {
		t.Error("Resize(0, 10) succeeded, want it refused")
	}
	if err := term.Key("NOT_A_KEY"); err == nil {
		t.Error("Key(NOT_A_KEY) succeeded, want it refused")
	}
	if err := term.Key(); err == nil {
		t.Error("Key() succeeded, want it refused")
	}
	if err := term.Wait(context.Background(), terminal.WaitFor{}); !errors.Is(err, terminal.ErrNoCondition) {
		t.Errorf("an empty WaitFor = %v, want %v", err, terminal.ErrNoCondition)
	}
	if err := wait(term, 0.2, terminal.WaitFor{Text: "never appears"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a wait that runs out = %v, want %v", err, context.DeadlineExceeded)
	}
	feed(t, term, "echo still-alive\n")
	mustWait(t, term, 5, terminal.WaitFor{Regex: re(`^still-alive$`)})
}

// A host that dies without calling Close still takes its shell down, whether
// it was killed or simply exited. Nothing runs in the host to make that
// happen: it is the parent-death signal and the PTY hanging up.
func TestTheHostDyingEndsTheShell(t *testing.T) {
	for _, c := range []struct {
		name string
		mode string
		// kill is the signal the host is sent, and zero for a host that exits
		// by itself.
		kill syscall.Signal
	}{
		{"SIGKILL", "block", syscall.SIGKILL},
		{"SIGTERM", "block", syscall.SIGTERM},
		{"exit without Close", "exit", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			host, shell, job := startHost(t, c.mode)
			if c.kill != 0 {
				host.Process.Signal(c.kill)
			}
			host.Wait()
			if !waitDead(shell, 5*time.Second) {
				stat, _ := os.ReadFile("/proc/" + strconv.Itoa(shell) + "/stat")
				t.Fatalf("the shell %d survived its host: %s", shell, stat)
			}
			// The background job is ended only if bash passed SIGHUP on before
			// it noticed the hang-up, so this is reported and not failed:
			// Close is the guaranteed path, and the package says so.
			if !waitDead(job, 2*time.Second) {
				t.Logf("the background job %d outlived its host, which is a known limitation", job)
				syscall.Kill(job, syscall.SIGKILL)
			}
		})
	}
}

func threadCount(t *testing.T) int {
	t.Helper()
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Skip("there is no /proc to count threads in")
	}
	for _, line := range strings.Split(string(status), "\n") {
		if v, ok := strings.CutPrefix(line, "Threads:"); ok {
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		}
	}
	t.Skip("/proc/self/status has no thread count")
	return 0
}

// Each running terminal holds one OS thread for the parent-death signal, and
// has to let it go once the program is gone; one kept per terminal ever
// started is a leak that grows for as long as the host runs.
func TestNoThreadIsKeptOnceAProgramIsGone(t *testing.T) {
	warm, _ := terminal.Start(terminal.Options{Command: []string{"true"}})
	<-warm.Done()
	warm.Close()
	before := threadCount(t)
	for i := 0; i < 30; i++ {
		term, err := terminal.Start(terminal.Options{Command: []string{"sh", "-c", "echo hi"}})
		if err != nil {
			t.Fatal(err)
		}
		<-term.Done()
		term.Close()
	}
	time.Sleep(200 * time.Millisecond)
	if after := threadCount(t); after > before+5 {
		t.Fatalf("threads grew from %d to %d over 30 terminals", before, after)
	}
}

// Readers, writers and waiters on one terminal at once. It proves the most
// under -race.
func TestConcurrentUse(t *testing.T) {
	term := startShell(t, terminal.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				term.Screen(terminal.ScreenOptions{Lines: 100, MaxChars: 500})
				term.Text()
				term.Cells()
				term.Status()
			}
		}()
	}
	feed(t, term, "seq 1 3000\n")
	if err := term.Wait(ctx, terminal.WaitFor{Regex: re(`^3000\n\$`)}); err != nil {
		t.Fatal(err)
	}
	if err := term.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()
	if s := screen(term, 0, 0); s.Size != (terminal.Size{Cols: 100, Rows: 30}) {
		t.Fatalf("size = %+v, want 100x30", s.Size)
	}
}

// A snapshot is meant to be handed to a model as JSON, so every field has to
// be there under the name it is documented with.
func TestSnapshotJSONHasEveryField(t *testing.T) {
	term := startShell(t, terminal.Options{})
	data, err := json.Marshal(screen(term, 2, 0))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"rows", "first_row", "total", "truncated", "cursor", "size", "alt_screen", "title"} {
		if _, ok := fields[k]; !ok {
			t.Errorf("the JSON has no %q: %s", k, data)
		}
	}
}

// Idle counted from After ignores the quiet before it. Without After, a wait
// begun right after typing returns before the program has answered, because
// the shell was already quiet when the keys were sent.
func TestIdleCountsFromAfter(t *testing.T) {
	term := startShell(t, terminal.Options{})
	wait(term, 5, terminal.WaitFor{Idle: duration(0.5)})
	feed(t, term, "sleep 0.3; echo late\n")
	mustWait(t, term, 5, terminal.WaitFor{Idle: duration(0.5), After: time.Now()})
	if !strings.Contains(term.Text(), "\nlate") {
		t.Fatalf("the wait returned before the output it was waiting out:\n%s", term.Text())
	}
}
