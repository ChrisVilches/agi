package cli

import (
	"fmt"
	"io"
	"os"

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

// step prints human-facing progress, which is dropped when nobody is watching.
func (o output) step(format string, args ...any) {
	fmt.Fprintf(o.steps, format, args...)
}

// final prints the answer the run produced.
func (o output) final(answer string) {
	fmt.Fprintln(o.answer, answer)
}

// isTerminal reports whether w is a terminal a person is looking at.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}
