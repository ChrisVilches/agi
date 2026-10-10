package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

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

	// stepsShown and answerShown say whether each of the two streams reaches
	// anybody at all.
	//
	// They are what decides whether the MessageDisplay hooks run over a piece
	// of text: agi will not spawn a renderer for a message it is about to
	// throw away. That is not a rule about terminals — a display hook is run
	// into a pipe like any other, because one that redacts or translates is
	// worth running there — it is what the event means, which is that agi is
	// displaying something.
	//
	// They are written down rather than worked out by comparing a writer
	// against io.Discard. The comparison is true today by accident of how
	// discarding happens to be spelled, and a stream that was one day
	// discarded some other way would go on reading as though somebody were
	// watching it.
	stepsShown  bool
	answerShown bool
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
		return output{answer: stdout, steps: stdout, verbose: verbose,
			stepsShown: true, answerShown: true}
	case verbose:
		return output{answer: stdout, steps: stderr, verbose: true,
			stepsShown: true, answerShown: true}
	default:
		return output{answer: stdout, steps: io.Discard, answerShown: true}
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
	return output{answer: io.Discard, steps: mark(o.steps), verbose: o.verbose,
		stepsShown: o.stepsShown}
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

// reason prints the deliberation behind an assistant turn, above the turn
// itself. It prints nothing for a turn that came back with no reasoning, which
// is most of them.
//
// It is progress and goes exactly where the rest of the progress goes: onto
// the screen at a terminal, onto standard error in a piped run that asked for
// --verbose, and nowhere at all in a piped run that did not. It is never on
// standard output away from a terminal, because progress never is — that
// stream is the channel another program is reading, and the answer is the only
// thing on it.
//
// That is one rule and not a second one about reasoning. Deliberation is
// longer than the rest of what a turn prints and it was tempting to hold it
// back from a piped --verbose run on that ground, but --verbose is a request
// for everything the run can say about itself, and a diagnostic stream missing
// the one part of a turn that explains the rest is not the quieter thing, only
// the less useful one. Whoever asked for it can stop asking.
//
// It is the shape a tool call has — a bracketed label with an indented block
// under it — because that is what it is sitting among, and in another color
// because it is not one: nothing here is about to happen to the machine.
//
// It does not go through the MessageDisplay hooks, and it is the one thing
// the model wrote that does not. A hook over this would fire on text no pipe
// ever carries, twice a turn, over the one piece of a turn that is the model
// talking to itself rather than to the reader — and a renderer armed to read
// the answer would be spawned to lay out deliberation nobody asked to see
// formatted. So it is printed exactly as it came back, and the three hooked
// sites stay the three things agi says to the user.
func (o output) reason(content string) {
	if !o.stepsShown {
		return
	}
	if content = strings.TrimSpace(content); content == "" {
		return
	}
	o.step("%s\n%s\n\n", tools.Thinking(), indent(content))
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
