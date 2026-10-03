package tools

// What may and may not be asserted about a recording, which every test here
// follows:
//
//   - A stream's own content and its own order are exact, always, and may be
//     asserted freely. That is the strongest claim the capture makes and most
//     of the file leans on it.
//   - The order of the two streams against each other may only be asserted
//     where the program forced a round trip. Two goroutines reading two pipes
//     have no obligation to each other, and a test that asserted an alternation
//     would fail on a loaded machine for being right about nothing.
//   - Chunk counts and chunk boundaries are decided by io.Copy's buffer, the
//     kernel's pipe and the scheduler, so nothing is asserted about them but
//     the length invariant.
//   - Every test that waits on a time waits at least ten times as long as the
//     thing it is waiting for, and says the ratio.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// sh runs a shell command line and returns what it did. The shell is the one
// the bash tool uses, for the same reason: one interpreter the tests can count
// on.
func sh(ctx context.Context, line string) *Execution {
	return Execute(ctx, exec.CommandContext(ctx, "/bin/sh", "-c", line))
}

// quick runs a command line that is expected to finish on its own.
func quick(t *testing.T, line string) *Execution {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ran := sh(ctx, line)
	if ran.Ending == TimedOut {
		t.Fatalf("%q did not finish within 30s, which is a hundred times longer "+
			"than it needs; something is not being drained", line)
	}
	return ran
}

// lines builds the text a loop printing prefix0..prefixN-1 produces.
func lines(prefix string, n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "%s%d\n", prefix, i)
	}
	return b.String()
}

// Standard output alone lands on standard output, and nothing invents a second
// stream for a program that only used one.
func TestStdoutAloneIsAllOnStdout(t *testing.T) {
	ran := quick(t, `printf 'hello\n'`)
	if ran.Stdout() != "hello\n" {
		t.Errorf("Stdout() = %q, want %q", ran.Stdout(), "hello\n")
	}
	if ran.Stderr() != "" {
		t.Errorf("Stderr() = %q, want empty", ran.Stderr())
	}
	if ran.Interleaved() != "hello\n" {
		t.Errorf("Interleaved() = %q, want %q", ran.Interleaved(), "hello\n")
	}
}

// The test this whole file exists for. It is also the one that catches the
// silent trap: hand exec one writer value for both fields and it gives the
// child a single file descriptor, everything lands on standard output, and
// nothing anywhere reports a problem.
func TestStderrAloneIsAllOnStderr(t *testing.T) {
	ran := quick(t, `printf 'warning\n' >&2`)
	if ran.Stderr() != "warning\n" {
		t.Errorf("Stderr() = %q, want %q — the two streams are sharing one "+
			"descriptor", ran.Stderr(), "warning\n")
	}
	if ran.Stdout() != "" {
		t.Errorf("Stdout() = %q, want empty", ran.Stdout())
	}
}

// Both streams at once are kept apart, which is the whole correction: the
// textual appearance of a line says nothing about which stream produced it, and
// only the file descriptor does.
func TestTheTwoStreamsAreKeptApart(t *testing.T) {
	ran := quick(t, `printf 'ERROR: on stdout\n'; printf 'all is well\n' >&2`)
	if ran.Stdout() != "ERROR: on stdout\n" {
		t.Errorf("Stdout() = %q", ran.Stdout())
	}
	if ran.Stderr() != "all is well\n" {
		t.Errorf("Stderr() = %q", ran.Stderr())
	}
}

// Each stream's own order is exact however much the two compete, which is the
// strongest claim the capture makes and the one the rest of the file leans on.
func TestEachStreamKeepsItsOwnOrderExactly(t *testing.T) {
	const n = 200
	ran := quick(t, fmt.Sprintf(
		`i=0; while [ $i -lt %d ]; do printf 'o%%d\n' $i; i=$((i+1)); done &
		 i=0; while [ $i -lt %d ]; do printf 'e%%d\n' $i >&2; i=$((i+1)); done
		 wait`, n, n))

	if ran.Stdout() != lines("o", n) {
		t.Error("standard output is not in the order it was written")
	}
	if ran.Stderr() != lines("e", n) {
		t.Error("standard error is not in the order it was written")
	}
}

// A command can say something useful on standard error and still have
// succeeded. The exit status decides nothing about which stream was captured,
// and a protocol that reads one of them on success depends on this.
func TestExitZeroStillCarriesWhatWasPrintedOnStderr(t *testing.T) {
	ran := quick(t, `printf 'result\n'; printf 'warning: deprecated\n' >&2; exit 0`)
	if ran.Ending != Exited || ran.Status != 0 {
		t.Fatalf("Ending = %s, Status = %d; want exited 0", ran.Ending, ran.Status)
	}
	if ran.Stdout() != "result\n" || ran.Stderr() != "warning: deprecated\n" {
		t.Errorf("Stdout() = %q, Stderr() = %q", ran.Stdout(), ran.Stderr())
	}
}

// And the mirror: a command that failed may have printed the useful half on
// standard output, so failing is no reason to throw a stream away.
func TestANonZeroExitStillCarriesWhatWasPrintedOnStdout(t *testing.T) {
	ran := quick(t, `printf 'some useful information\n'; printf 'fatal\n' >&2; exit 3`)
	if ran.Ending != Exited || ran.Status != 3 {
		t.Fatalf("Ending = %s, Status = %d; want exited 3", ran.Ending, ran.Status)
	}
	if ran.Stdout() != "some useful information\n" {
		t.Errorf("Stdout() = %q", ran.Stdout())
	}
}

// A signalled program has no exit status of its own, and reporting one would
// mean printing -1 as though a shell had ever shown such a number.
func TestASignalIsNotAnExitStatus(t *testing.T) {
	ran := quick(t, `kill -TERM $$`)
	if ran.Ending != Signalled {
		t.Fatalf("Ending = %s, want signalled (Status = %d)", ran.Ending, ran.Status)
	}
	if ran.Signal != syscall.SIGTERM {
		t.Errorf("Signal = %v, want SIGTERM", ran.Signal)
	}
}

// Output that stops mid-line is not given an ending it never had, and no byte
// is invented on the way through the woven view. Asserting the length rather
// than the text is what lets this say so without claiming an order.
func TestOutputWithNoTrailingNewlineIsNotGivenOne(t *testing.T) {
	ran := quick(t, `printf 'no newline'; printf 'nor here' >&2`)
	if ran.Stdout() != "no newline" {
		t.Errorf("Stdout() = %q", ran.Stdout())
	}
	if ran.Stderr() != "nor here" {
		t.Errorf("Stderr() = %q", ran.Stderr())
	}
	if got, want := len(ran.Interleaved()), len(ran.Stdout())+len(ran.Stderr()); got != want {
		t.Errorf("the woven view is %d bytes and the two streams are %d; something "+
			"was added or dropped", got, want)
	}
}

// A program that never ran is not a program that failed, and the two must not
// be reported as one thing: there is nothing to read a status out of.
func TestAProgramThatCannotBeStartedIsNotAProgramThatFailed(t *testing.T) {
	ctx := context.Background()
	ran := Execute(ctx, exec.CommandContext(ctx, filepath.Join(t.TempDir(), "nothing-here")))
	if ran.Ending != Unstarted {
		t.Fatalf("Ending = %s, want unstarted", ran.Ending)
	}
	if ran.Stdout() != "" || ran.Stderr() != "" {
		t.Errorf("a program that never ran wrote something: %q / %q", ran.Stdout(), ran.Stderr())
	}
	if ran.Err == nil {
		t.Error("Err is nil, so nothing can say why it would not start")
	}
}

// The missing shebang, which is the commonest way a user's own script will not
// run. Each caller words its own sentence about it, so what is shared is the
// test for it and not the words.
func TestAFileWithoutAShebangIsReportedAsTheKernelRefusingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(path, []byte("this is prose, not a program\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ran := Execute(ctx, exec.CommandContext(ctx, path))
	if ran.Ending != Unstarted {
		t.Fatalf("Ending = %s, want unstarted", ran.Ending)
	}
	if !errors.Is(ran.Err, syscall.ENOEXEC) {
		t.Errorf("Err = %v, want one wrapping ENOEXEC", ran.Err)
	}
}

// A command that ran perfectly well but left something behind holding the pipe
// is a command that ran, not one that could not be started. Switching on the
// error type reports it as the latter, because ErrWaitDelay is not an
// ExitError; switching on the wait status says the truth.
//
// The straggler outlives the delay twenty-five times over, so there is no race
// to lose here.
func TestAWaitDelayOverrunIsNotACommandThatCouldNotBeStarted(t *testing.T) {
	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `(sleep 5 &) ; printf 'done'`)
	cmd.WaitDelay = 200 * time.Millisecond
	ran := Execute(ctx, cmd)

	if ran.Ending != Exited || ran.Status != 0 {
		t.Fatalf("Ending = %s, Status = %d; want exited 0", ran.Ending, ran.Status)
	}
	if ran.Stdout() != "done" {
		t.Errorf("Stdout() = %q, want %q", ran.Stdout(), "done")
	}
}

// The invariant that says the record and the bytes cannot have drifted apart.
// It holds for any program at all, so it is run against the one that stresses
// the record hardest.
func TestTheRecordedLengthsAccountForEveryByte(t *testing.T) {
	ran := quick(t, `i=0; while [ $i -lt 100 ]; do printf 'o%d\n' $i; printf 'e%d\n' $i >&2; i=$((i+1)); done`)

	var sum [2]int
	for _, c := range ran.chunks {
		sum[c.stream] += c.n
	}
	if sum[outStream] != len(ran.out) {
		t.Errorf("chunks account for %d bytes of standard output, which holds %d",
			sum[outStream], len(ran.out))
	}
	if sum[errStream] != len(ran.err) {
		t.Errorf("chunks account for %d bytes of standard error, which holds %d",
			sum[errStream], len(ran.err))
	}
}

// Two reads of the same stream in a row say nothing a single read would not, so
// the record keeps one chunk rather than two. Without this the list grows with
// the output of a program that only ever used one stream.
func TestAdjacentChunksOfOneStreamAreOneChunk(t *testing.T) {
	e := &Execution{}
	e.record(outStream, []byte("one "))
	e.record(outStream, []byte("two "))
	e.record(errStream, []byte("!"))
	e.record(outStream, []byte("three"))

	if len(e.chunks) != 3 {
		t.Fatalf("recorded %d chunks, want 3", len(e.chunks))
	}
	if e.chunks[0].n != 8 {
		t.Errorf("the first two writes were not joined: chunk is %d bytes, want 8", e.chunks[0].n)
	}
}

// The reason the woven view is by line and not by chunk. A read is not a write,
// so a chunk boundary can fall inside a line the program wrote whole; emitting
// chunks as they came would splice the other stream into it, and this is the
// record that does it.
//
// It is built by hand rather than run, because no real program can be relied on
// to produce exactly this record, and the rule still has to be checked.
func TestTheWovenViewNeverSplicesTheMiddleOfALine(t *testing.T) {
	e := &Execution{}
	e.record(outStream, []byte("he"))
	e.record(errStream, []byte("ERR\n"))
	e.record(outStream, []byte("llo\n"))

	if got, want := e.Interleaved(), "ERR\nhello\n"; got != want {
		t.Errorf("Interleaved() = %q, want %q", got, want)
	}
}

// A line that never ended is still handed back whole, and nothing is put after
// it that the program did not print.
func TestATailWithNoNewlineIsFlushedAtTheEndAndNothingIsAddedToIt(t *testing.T) {
	e := &Execution{}
	e.record(outStream, []byte("done\n"))
	e.record(outStream, []byte("and then"))

	if got, want := e.Interleaved(), "done\nand then"; got != want {
		t.Errorf("Interleaved() = %q, want %q", got, want)
	}
}

// When both streams end mid-line there is nothing left to order them by but
// when each was last seen, so that is what is used — and still nothing is
// inserted between them.
func TestTwoUnterminatedTailsAreFlushedInTheOrderTheyWereLastSeen(t *testing.T) {
	e := &Execution{}
	e.record(errStream, []byte("err"))
	e.record(outStream, []byte("out"))

	if got, want := e.Interleaved(), "errout"; got != want {
		t.Errorf("Interleaved() = %q, want %q", got, want)
	}
}

// Both pipes are drained while the program runs. Reading one stream to the end
// before starting on the other deadlocks here: a megabyte is sixteen times the
// kernel's pipe buffer, so the program blocks writing and never exits.
func TestBothStreamsAtOnceDoNotDeadlockHoweverMuchTheyWrite(t *testing.T) {
	const size = 1 << 20

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ran := sh(ctx, fmt.Sprintf(
		`head -c %d /dev/zero & head -c %d /dev/zero >&2; wait`, size, size))

	if ran.Ending == TimedOut {
		t.Fatal("neither stream finished within 30s: the two pipes are not being " +
			"drained at the same time, which is the deadlock this guards")
	}
	if len(ran.Stdout()) != size || len(ran.Stderr()) != size {
		t.Errorf("captured %d bytes of standard output and %d of standard error, want %d each",
			len(ran.Stdout()), len(ran.Stderr()), size)
	}
}

// What a program printed before it was stopped is still worth having, so the
// record keeps it and the caller decides what to do with it. The sleep is
// thirty times the wait, so there is no race to lose.
func TestInterruptionKeepsWhatWasAlreadyPrinted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *Execution, 1)
	go func() {
		done <- sh(ctx, `printf 'out\n'; printf 'err\n' >&2; sleep 10`)
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()

	ran := <-done
	if ran.Ending != Cancelled {
		t.Fatalf("Ending = %s, want cancelled", ran.Ending)
	}
	if ran.Stdout() != "out\n" || ran.Stderr() != "err\n" {
		t.Errorf("Stdout() = %q, Stderr() = %q; what was printed before the stop was lost",
			ran.Stdout(), ran.Stderr())
	}
}

// A command that outran its own deadline is not a run that was stopped
// underneath it, and the two have to be told apart: one is the command's doing
// and has a next turn to be reported in, the other is the run ending.
func TestATimeoutIsNotACancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	ran := sh(ctx, `printf 'started\n'; sleep 10`)

	if ran.Ending != TimedOut {
		t.Fatalf("Ending = %s, want timed out", ran.Ending)
	}
	if ran.Stdout() != "started\n" {
		t.Errorf("Stdout() = %q; what was printed before the deadline was lost", ran.Stdout())
	}
}

// Rapid alternation, asserting only what the program forced. Each stream's own
// order is exact; no byte is added or lost; and every line of the woven view is
// one of the lines that was printed, which is the splice property and is true
// whatever the scheduler did.
//
// What is deliberately not asserted is that the woven view alternates. It
// usually does, and a test that required it would fail on a loaded machine for
// being right about nothing.
func TestRapidAlternationKeepsEachStreamsOwnOrder(t *testing.T) {
	const n = 500
	ran := quick(t, fmt.Sprintf(
		`i=0; while [ $i -lt %d ]; do printf 'o%%d\n' $i; printf 'e%%d\n' $i >&2; i=$((i+1)); done`, n))

	if ran.Stdout() != lines("o", n) {
		t.Error("standard output is not in the order it was written")
	}
	if ran.Stderr() != lines("e", n) {
		t.Error("standard error is not in the order it was written")
	}

	woven := ran.Interleaved()
	if len(woven) != len(ran.Stdout())+len(ran.Stderr()) {
		t.Fatalf("the woven view is %d bytes and the two streams are %d",
			len(woven), len(ran.Stdout())+len(ran.Stderr()))
	}
	printed := make(map[string]bool, 2*n)
	for i := range n {
		printed[fmt.Sprintf("o%d", i)] = true
		printed[fmt.Sprintf("e%d", i)] = true
	}
	for _, line := range strings.Split(strings.TrimRight(woven, "\n"), "\n") {
		if !printed[line] {
			t.Fatalf("the woven view holds %q, which no program ever printed: a read "+
				"landed in the middle of a line", line)
		}
	}
}

// The one place cross-stream order may be asserted: the program waited between
// the two writes, so the order really is knowable. Both directions are checked,
// so neither stream is quietly privileged.
//
// Two hundred milliseconds is thousands of times the scheduler's grain. A
// shorter wait would make this fail for a reason that is not a bug, which is
// the rule the top of this file states.
func TestCrossStreamOrderIsKeptWhenTheProgramForcesARoundTrip(t *testing.T) {
	for _, c := range []struct{ line, want string }{
		{`printf 'first\n' >&2; sleep 0.2; printf 'second\n'`, "first\nsecond\n"},
		{`printf 'first\n'; sleep 0.2; printf 'second\n' >&2`, "first\nsecond\n"},
	} {
		ran := quick(t, c.line)
		if got := ran.Interleaved(); got != c.want {
			t.Errorf("Interleaved() = %q, want %q, for %s", got, c.want, c.line)
		}
	}
}

// A hook replaces what the model reads and changes nothing about what the
// program did, so the rewritten record keeps the ending and drops the streams.
func TestRewritingAResultKeepsHowTheProgramEnded(t *testing.T) {
	ran := quick(t, `printf 'out\n'; printf 'err\n' >&2; exit 2`)
	rewritten := ran.Rewritten("a hook said this instead")

	if rewritten.Ending != Exited || rewritten.Status != 2 {
		t.Errorf("Ending = %s, Status = %d; want exited 2", rewritten.Ending, rewritten.Status)
	}
	if rewritten.Stdout() != "a hook said this instead" {
		t.Errorf("Stdout() = %q", rewritten.Stdout())
	}
	if rewritten.Stderr() != "" {
		t.Errorf("Stderr() = %q, want empty: the hook's text is one stream", rewritten.Stderr())
	}
	if ran.Stdout() != "out\n" {
		t.Errorf("the original was changed: Stdout() = %q", ran.Stdout())
	}
}

// A tool that ran no program has nothing to rewrite, and must not grow a record
// on the way through the hooks.
func TestRewritingNothingIsStillNothing(t *testing.T) {
	var none *Execution
	if none.Rewritten("text") != nil {
		t.Error("a tool that ran no program was given one")
	}
}

// Bytes are kept as bytes. Nothing decodes, validates or normalizes them, so a
// program printing a carriage return or an invalid encoding is handed back what
// it printed.
func TestBytesAreKeptAsTheyWerePrinted(t *testing.T) {
	ran := quick(t, `printf 'a\r\nb\xff\n'`)
	if want := "a\r\nb\xff\n"; ran.Stdout() != want {
		t.Errorf("Stdout() = %q, want %q", ran.Stdout(), want)
	}
	if bytes.Contains([]byte(ran.Stdout()), []byte("�")) {
		t.Error("an invalid byte was replaced rather than kept")
	}
}
