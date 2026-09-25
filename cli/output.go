package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/ChrisVilches/agi/tools"
	"github.com/mattn/go-isatty"
)

// output decides where the two kinds of thing agi prints should go.
//
// At a terminal, stdout is a display for a person: tool calls, the reasons the
// agent gave, and the answer all belong there. When stdout is piped or
// redirected it stops being a display and becomes a channel to another
// program, so it must carry the final answer and nothing else. Progress would
// be noise at best and corrupt the other program's input at worst.
type output struct {
	// answer receives the final answer, always.
	answer io.Writer
	// steps receives human-facing progress. It is io.Discard when there is no
	// one to read it.
	steps io.Writer
	// verbose includes tool output in the progress stream.
	verbose bool
}

// newOutput routes the two streams.
//
// Verbose output while piping is a deliberate request for diagnostics, so it
// goes to stderr: the user gets what they asked for without putting anything
// on the channel the other program is reading.
func newOutput(stdout, stderr io.Writer, verbose bool) output {
	return route(isTerminal(stdout), stdout, stderr, verbose)
}

// route decides the streams from whether a person is watching, kept separate
// from newOutput so every branch can be tested without a terminal.
func route(interactive bool, stdout, stderr io.Writer, verbose bool) output {
	switch {
	case interactive:
		return output{answer: stdout, steps: stdout, verbose: verbose}
	case verbose:
		return output{answer: stdout, steps: stderr, verbose: true}
	default:
		return output{answer: stdout, steps: io.Discard}
	}
}

// quiet returns an output nobody reads. It is for a run whose progress would
// only get in the way: subagents work several at a time, and their steps
// interleaved with each other and with the parent's would be unreadable.
func quiet() output {
	return output{answer: io.Discard, steps: io.Discard}
}

// nested returns an output for an agent running inside this one, with its
// progress marked so the user can see whose it is.
//
// It is quiet's opposite number and exists because a fork can be watched where
// a subagent cannot. Subagents are silent because several run at once and their
// steps would interleave into a page belonging to no agent; only one fork runs
// at a time, and the agent that asked for it is blocked while it works, so
// there is nothing for its steps to interleave with.
//
// The answer is discarded. A fork's answer is not the run's: it is the result
// of a tool call, which the agent loop already prints and logs, and printing it
// here as well would say it twice.
func (o output) nested() output {
	return output{answer: io.Discard, steps: mark(o.steps), verbose: o.verbose}
}

// nestedMarker heads every line an agent inside the run prints.
//
// It is not indent's "  | ", which already marks the output of a tool. A fork's
// own tool output is marked by indent before it reaches here and is marked
// again on the way out, so the two have to be told apart or the first line of a
// fork would read as the run's own tool output.
const nestedMarker = "  : "

// marker writes to w, putting nestedMarker at the start of every line.
//
// It marks lazily — when the first byte of a line arrives rather than when the
// newline ending the previous one does — because a run prints a bare newline
// after every tool call, and marking eagerly would draw a marker on nothing.
// It holds one bit of state rather than splitting a finished block, because
// progress arrives a line at a time and a line often arrives across several
// writes.
type marker struct {
	w io.Writer
	// midLine says a line is already under way, so the next byte continues it
	// rather than starting a new one.
	midLine bool
}

// mark wraps w so that every line written through it is marked.
func mark(w io.Writer) io.Writer { return &marker{w: w} }

func (m *marker) Write(p []byte) (int, error) {
	for i, b := range p {
		// A newline arriving at the start of a line is a blank line, and a
		// marker on it would be a marker on nothing. The agent loop prints one
		// after every tool call, so this is the common case rather than a
		// corner of one.
		if !m.midLine && b != '\n' {
			if _, err := io.WriteString(m.w, tools.Detail(nestedMarker)); err != nil {
				return i, err
			}
			m.midLine = true
		}
		if _, err := m.w.Write(p[i : i+1]); err != nil {
			return i, err
		}
		if b == '\n' {
			m.midLine = false
		}
	}
	return len(p), nil
}

// step prints human-facing progress, which is dropped when nobody is watching.
func (o output) step(format string, args ...any) {
	fmt.Fprintf(o.steps, format, args...)
}

// final prints the answer the run produced.
func (o output) final(answer string) {
	fmt.Fprintln(o.answer, answer)
}

// isTerminal reports whether a stream is one end of a terminal. It takes
// either end — a reader or a writer — because the same question is asked of
// standard input, where it decides whether a prompt can be typed, and of
// standard output, where it decides whether anyone is watching.
func isTerminal(stream any) bool {
	f, ok := stream.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}
