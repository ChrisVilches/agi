package tools

// This file is deliberately not part of process.go. That file does not build on
// Windows and says so on purpose, because the two syscalls in it are the whole
// of what it means. Nothing here is unix-specific but the one WaitStatus below,
// so keeping the two apart means a Windows port moves two syscalls rather than
// first having to split a file.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// stream names which of a program's two output streams some bytes came from.
//
// It is unexported, and so is the chunk list it labels, because nothing outside
// this package has a use for either yet: a caller wants one stream, the other,
// or the two woven together, and all three are methods here. The record is what
// would let a later change dim standard error on screen, and that change can
// export it when it is made rather than this file exporting a name on its
// behalf.
type stream uint8

const (
	outStream stream = iota
	errStream
)

// chunk is one stretch of bytes read from one stream, recorded as a length
// rather than as the bytes themselves. The bytes are already in that stream's
// own buffer, in order, so a copy here would be the same bytes stored twice;
// and because the walk that rebuilds the woven view is sequential, a length is
// as good as an offset.
//
// Two chunks of the same stream in a row are indistinguishable from one in
// every view derived from the record, so the recorder extends the last chunk
// instead of appending a new one. A program printing a megabyte on standard
// output alone records one chunk rather than thirty-two, and the list grows
// only when the two streams really do alternate — which is the only case it
// exists for.
type chunk struct {
	stream stream
	n      int
}

// Ending says how a program stopped, in the ways a caller answers differently.
//
// It is a type rather than an error, a boolean and an integer because every
// caller here has to tell a program that failed from one that never ran from
// one agi killed, and a distinction three callers draw is a distinction worth a
// name.
type Ending uint8

const (
	// Exited is the program having run to completion and chosen its own
	// status, which Status carries. Zero is not separated out: whether a
	// status means failure is the caller's protocol, and a hook reads the same
	// number differently from a tool.
	Exited Ending = iota
	// Signalled is a signal having ended it, which Signal names. It is kept
	// apart from Exited because a signalled program has no exit status at all
	// — ExitCode reports -1 for one — and a caller printing "exit status -1"
	// would be printing a number no shell ever shows.
	Signalled
	// TimedOut is the program having outrun the deadline on the context it was
	// given, which is each caller's own bound and never the run's.
	TimedOut
	// Cancelled is the run having been stopped underneath the program. It did
	// not fail, and there is usually no next turn to report anything in.
	Cancelled
	// Unstarted is the program never having run: nothing at that path, no
	// permission, no shebang for the kernel to follow. Err carries what the
	// operating system said, and both streams are empty because nothing was
	// ever there to write to them.
	Unstarted
)

// String names an ending, for a test failure or a log line that would otherwise
// print a number.
func (e Ending) String() string {
	switch e {
	case Exited:
		return "exited"
	case Signalled:
		return "signalled"
	case TimedOut:
		return "timed out"
	case Cancelled:
		return "cancelled"
	case Unstarted:
		return "unstarted"
	}
	return "unknown"
}

// Execution is what one program did: what it wrote on each of its two output
// streams, the order agi observed the two arriving in, and how it stopped.
//
// It exists because every tool that runs a program has the same problem and no
// reason to answer it differently, which is the argument Truncate makes one
// file over and KillGroupOnCancel makes citing it. What a program wrote, in
// what order, and how it ended is the third member of that set.
//
// The record is two buffers and a list of chunk lengths, and the woven view is
// derived from it by Interleaved. That is the way round it has to be. Once the
// two streams have been flattened into one string their identity is gone for
// good, and no amount of reading the string afterwards brings it back; the
// reverse is a walk over a list. It is also the rule the rest of agi already
// follows — a subagent's answers are kept apart and assembled in Render, never
// stored assembled — and the invariant that keeps the record honest is that the
// chunk lengths for a stream sum to the length of that stream's buffer.
//
// What the order means is worth being exact about, because it is weaker than it
// looks. The chunks are in the order agi finished reading them, which is not
// the order the program wrote them. Two pipes are buffered separately by the
// kernel and drained by two goroutines scheduled independently, so a program
// that writes to standard error and then immediately to standard output may be
// observed either way round. What is guaranteed is that each stream's own bytes
// are in exactly the order they were written, and that a chunk boundary is a
// real moment: when the program forces a round trip, or simply pauses longer
// than the scheduler's grain, the recorded order is the true one. That is the
// whole of what interleaving can mean once the two streams are two file
// descriptors, and it is strictly weaker than giving the program one descriptor
// for both, where the kernel orders the writes exactly. Telling the streams
// apart costs that, and this is the trade being made.
type Execution struct {
	// Ending is how the program stopped, and the fields under it are what that
	// ending has to say. Status means something for Exited, Signal for
	// Signalled, and Err for Unstarted.
	Ending Ending
	Status int
	Signal syscall.Signal
	// Err is the raw error cmd.Run returned, kept so that a caller wanting a
	// particular cause has errors.Is rather than a helper here. The two that
	// want one want syscall.ENOEXEC, and each words its own sentence about it:
	// a user-defined tool tells the author of a tool what is missing, a hook
	// tells the author of a hook, and a sentence shared between them would be
	// addressed to neither.
	Err error

	// TODO: capture is unbounded, as cmd.CombinedOutput and a bytes.Buffer
	// both already were, so `cat /dev/urandom` ends the run by exhausting
	// memory. The fix belongs here rather than in each caller, and its shape
	// is decided even though it is not written: a cap on the total across both
	// streams, since the budget being protected is one context window and the
	// two share it; head and tail preserved, so that it composes with Truncate
	// instead of handing it an already-cut string and making its "[N bytes
	// omitted]" a lie; and the gap recorded as a chunk of its own carrying a
	// byte count, so the woven view stays derivable across it and can say
	// where it is. That last clause is why this is a change to the record and
	// not a buffer size, and why it waits for its own unit of work.
	mu     sync.Mutex
	out    []byte
	err    []byte
	chunks []chunk
}

// writer records what one stream wrote. Two separately allocated values are
// handed to one command, and that is load-bearing: exec gives the child a
// single file descriptor for both streams when Stdout and Stderr are the same
// interface value — that is exactly how CombinedOutput merges them — so one
// writer shared between the two fields would put everything on standard output,
// leave Stderr empty forever, and report no error at all.
type writer struct {
	to     *Execution
	stream stream
}

// Write implements io.Writer. It never fails: there is nowhere for the bytes to
// fail to go, and an error here would stop exec's copier and lose the rest of
// the program's output over a problem that cannot happen.
func (w *writer) Write(p []byte) (int, error) {
	w.to.record(w.stream, p)
	return len(p), nil
}

// record appends bytes to their stream and notes that they arrived.
//
// Both writers take this one lock, and under it do both halves of the record.
// That is what makes the observed order a total order: "this chunk precedes
// that one" means "agi finished reading this before it began reading that", for
// every pair. A lock per stream, or a sequence number taken separately from the
// append, would let the two orderings disagree and there would be no single
// moment the record is about.
func (e *Execution) record(s stream, p []byte) {
	if len(p) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	if s == outStream {
		e.out = append(e.out, p...)
	} else {
		e.err = append(e.err, p...)
	}
	if last := len(e.chunks) - 1; last >= 0 && e.chunks[last].stream == s {
		e.chunks[last].n += len(p)
		return
	}
	e.chunks = append(e.chunks, chunk{stream: s, n: len(p)})
}

// Execute runs a command that is already built and configured, and reports what
// it did.
//
// The command belongs to the caller and this only runs it. Each caller builds a
// different one — a shell command line, an executable handed a JSON argument, a
// hook reading a payload on standard input — and sets its own Dir, its own
// WaitDelay, its own timeout. A primitive that built the command would need
// every one of those as a parameter and would be a worse exec.Cmd. What is
// actually shared is the two things no caller has a reason to answer
// differently: how the output is captured, and what the outcome was. So this
// sets Stdout and Stderr, calls Run, and classifies. It sets nothing else and
// reads nothing else, and a command that already had either field set has it
// replaced, since capturing is the whole point of calling this.
//
// It does not call KillGroupOnCancel either, though all three callers do. That
// has to be called before the command is started and would mean this silently
// replacing a SysProcAttr a caller had set. Two lines next to each other in
// three places is the right amount of repetition for two decisions that merely
// happen to agree today.
//
// The context is the one the command was built with, and is read only to tell a
// run that was stopped from a program that outran its own deadline. It is read
// first, because a killed program's wait status describes the signal agi sent
// and says nothing about the program.
//
// Nothing is returned but the execution. Everything that can go wrong is an
// Ending and the raw error is in Err, so there is no second return for a caller
// to write `if err != nil { return err }` against — which is precisely the shape
// that throws away what a timed-out or signalled program had already printed,
// the thing this exists to keep.
func Execute(ctx context.Context, cmd *exec.Cmd) *Execution {
	e := &Execution{}
	// Two distinct values, for the reason written on writer.
	cmd.Stdout = &writer{to: e, stream: outStream}
	cmd.Stderr = &writer{to: e, stream: errStream}

	// Both pipes are drained while the program runs, and that is exec's doing
	// rather than something arranged here: a writer that is not an *os.File
	// makes exec create a pipe and copy from it on a goroutine of its own, so
	// two such writers are two pipes and two copiers, started before the child
	// and running until end of file. The deadlock this file would otherwise
	// have — the child blocked filling the standard error pipe while the
	// parent waits for standard output to finish — is only reachable by
	// reading one stream after the other, which is what StdoutPipe and
	// StderrPipe invite. Wait also waits for both copiers before it returns,
	// and closes the pipes itself when WaitDelay elapses first, so by the time
	// Run returns nothing can still be writing into the record.
	runErr := cmd.Run()
	e.Err = runErr

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		e.Ending = TimedOut
	case errors.Is(ctx.Err(), context.Canceled):
		e.Ending = Cancelled
	case cmd.ProcessState == nil:
		// There is no wait status, so the program never ran. This is the only
		// branch that reads runErr for anything but keeping it.
		e.Ending = Unstarted
	default:
		// The remaining two branches ask the wait status rather than the error
		// type, and that is not a stylistic preference. Wait returns
		// exec.ErrWaitDelay when the program exited cleanly but something it
		// left behind held the output pipe past the delay — the exact case
		// WaitDelay exists for. That is not an *exec.ExitError, so a caller
		// switching on the error type falls through to its last arm and
		// reports a command that ran fine as one that could not be run. The
		// wait status says the truth: it ran, here is its status, and here is
		// what was captured before the pipes were closed.
		if sig := signalled(cmd.ProcessState); sig != 0 {
			e.Ending = Signalled
			e.Signal = sig
			break
		}
		e.Ending = Exited
		e.Status = cmd.ProcessState.ExitCode()
	}
	return e
}

// signalled returns the signal that ended a program, or zero for one that
// chose its own exit status. Zero is safe as the answer for "none", since no
// program is ever killed by signal zero: that is the signal that tests whether
// a process exists.
func signalled(st *os.ProcessState) syscall.Signal {
	ws, ok := st.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return 0
	}
	return ws.Signal()
}

// Stdout returns everything the program wrote on standard output, in the order
// it wrote it.
func (e *Execution) Stdout() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return string(e.out)
}

// Stderr returns everything the program wrote on standard error, in the order
// it wrote it.
func (e *Execution) Stderr() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return string(e.err)
}

// Interleaved returns the two streams woven back together in the order agi
// observed them, which is what a reader of command output usually wants: a
// warning between the two steps it came between is worth more than knowing
// which file descriptor carried it.
//
// It is derived from the record every time it is asked for, and never computed
// while the program runs. The record is the record, and a woven string stored
// at capture time would commit to one rendering and make every other one
// unrecoverable.
//
// The weaving is by line and not by chunk, which is the one place this departs
// from what was literally observed. A read is not a write: the operating system
// may split one of the program's writes across two reads or join two into one,
// so a chunk boundary can fall in the middle of a line that the program wrote
// whole. Emitting chunks as they came would then let the other stream land
// inside that line, and `error: cannot op` + `hello` + `en file` would render as
// `error: cannot ophelloen file` — a corruption that giving the program one
// descriptor for both streams could never produce, and the one real cost of
// telling them apart. So each stream's bytes wait here until its line is
// complete, and while they wait the other stream's complete lines go out.
//
// The price is a slight reordering: a line is placed by when it finished, not by
// when its first byte arrived, so a line of standard output that was only
// completed after three lines of standard error had been read is emitted after
// those three. The guarantee bought is that every write before the final flush
// ends in a newline, so nothing emitted can land in the middle of a line,
// whatever the two goroutines did.
//
// Two edges follow from that and are deliberate. Nothing is ever inserted: when
// the first tail flushed at the end does not end in a newline, the second is
// butted straight onto it, because a separator would be agi putting bytes on
// the screen that no program printed. And a progress bar that redraws itself
// with a carriage return never completes a line, so it is held to the end and
// flushed there rather than woven in — which is right, since it is one logical
// line, but is surprising enough to be worth saying.
func (e *Execution) Interleaved() string {
	e.mu.Lock()
	defer e.mu.Unlock()

	buf := [2][]byte{e.out, e.err}
	var (
		woven   []byte
		pending [2][]byte
		taken   [2]int
		last    = [2]int{-1, -1}
	)
	for i, c := range e.chunks {
		s := c.stream
		last[s] = i
		pending[s] = append(pending[s], buf[s][taken[s]:taken[s]+c.n]...)
		taken[s] += c.n
		if k := bytes.LastIndexByte(pending[s], '\n'); k >= 0 {
			woven = append(woven, pending[s][:k+1]...)
			pending[s] = pending[s][k+1:]
		}
	}

	// The tails are flushed in the order their streams were last seen, which is
	// the only thing left that says anything about when they were written.
	first, second := outStream, errStream
	if last[errStream] < last[outStream] {
		first, second = errStream, outStream
	}
	woven = append(woven, pending[first]...)
	woven = append(woven, pending[second]...)
	return string(woven)
}

// Rewritten is the record as it stands once a hook has replaced the result: the
// text is the hook's, so it stands in for standard output and standard error is
// empty, while how the program ended is untouched, because nothing a hook does
// afterwards changes what the program did.
//
// It is a method here rather than something each tool works out for itself,
// because every tool that ran a program answers it the same way, and the caller
// is the hooks package, which must not know which tool it is holding. A nil
// execution — a tool that ran no program — rewrites to nil.
func (e *Execution) Rewritten(text string) *Execution {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	r := &Execution{Ending: e.Ending, Status: e.Status, Signal: e.Signal, Err: e.Err}
	if text != "" {
		r.out = []byte(text)
		r.chunks = []chunk{{stream: outStream, n: len(r.out)}}
	}
	return r
}
