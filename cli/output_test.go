package cli

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// At a terminal, everything is for the person watching.
func TestRouteInteractive(t *testing.T) {
	var stdout, stderr bytes.Buffer
	o := route(true, &stdout, &stderr, false)

	o.step("progress\n")
	o.final("the answer")

	if stdout.String() != "progress\nthe answer\n" {
		t.Errorf("stdout = %q, want progress and answer together", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", stderr.String())
	}
}

// Piped, stdout carries the answer and nothing else: another program is
// reading it.
func TestRoutePipedDropsProgress(t *testing.T) {
	var stdout, stderr bytes.Buffer
	o := route(false, &stdout, &stderr, false)

	o.step("progress\n")
	o.step("[bash] doing something\n")
	o.final("the answer")

	if stdout.String() != "the answer\n" {
		t.Errorf("stdout = %q, want only the answer", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing without --verbose", stderr.String())
	}
}

// Asking for diagnostics while piping must not pollute the data channel.
func TestRoutePipedVerboseGoesToStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	o := route(false, &stdout, &stderr, true)

	o.step("progress\n")
	o.final("the answer")

	if stdout.String() != "the answer\n" {
		t.Errorf("stdout = %q, want only the answer", stdout.String())
	}
	if stderr.String() != "progress\n" {
		t.Errorf("stderr = %q, want the progress", stderr.String())
	}
	if !o.verbose {
		t.Error("verbose = false, want it carried through")
	}
}

// Verbose only controls tool output; it never moves the answer.
func TestRouteAnswerAlwaysGoesToStdout(t *testing.T) {
	for _, tt := range []struct {
		name        string
		interactive bool
		verbose     bool
	}{
		{"interactive", true, false},
		{"interactive verbose", true, true},
		{"piped", false, false},
		{"piped verbose", false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			route(tt.interactive, &stdout, &stderr, tt.verbose).final("the answer")
			if stdout.String() != "the answer\n" {
				t.Errorf("stdout = %q, want the answer", stdout.String())
			}
		})
	}
}

func TestIsTerminal(t *testing.T) {
	if isTerminal(&bytes.Buffer{}) {
		t.Error("isTerminal(buffer) = true, want false")
	}
	if isTerminal(io.Discard) {
		t.Error("isTerminal(io.Discard) = true, want false")
	}

	// A pipe is a file but not a terminal, which is exactly the case that
	// matters here.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() = %v", err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	if isTerminal(w) {
		t.Error("isTerminal(pipe) = true, want false")
	}
}

// newOutput must reach the piped branch under `go test`, whose stdout is not a
// terminal.
func TestNewOutputDetectsNonTerminal(t *testing.T) {
	var stderr bytes.Buffer
	o := newOutput(os.Stdout, &stderr, false)
	if o.steps != io.Discard {
		t.Error("newOutput() kept a progress stream although stdout is not a terminal")
	}
}

// Each output says whether its two streams reach anybody, which is what
// decides whether the MessageDisplay hooks are run over a message: agi will
// not spawn a renderer for text it is about to throw away.
//
// It is a table because the four constructors are the whole of the answer,
// and one of them getting it wrong is silent — a subagent quietly rendering
// everything it prints into io.Discard, or a piped answer quietly going out
// unrendered.
func TestOutputSaysWhichStreamsReachSomebody(t *testing.T) {
	var buf bytes.Buffer
	cases := []struct {
		what         string
		out          output
		steps, final bool
	}{
		{"at a terminal", route(true, &buf, &buf, false), true, true},
		{"piped", route(false, &buf, io.Discard, false), false, true},
		{"piped and verbose", route(false, &buf, io.Discard, true), true, true},
		{"a subagent", quiet(), false, false},
		{"a fork of a watched run", route(true, &buf, &buf, false).nested(), true, false},
		{"a fork of a piped run", route(false, &buf, io.Discard, false).nested(), false, false},
	}

	for _, c := range cases {
		if c.out.stepsShown != c.steps {
			t.Errorf("%s: stepsShown = %v, want %v", c.what, c.out.stepsShown, c.steps)
		}
		if c.out.answerShown != c.final {
			t.Errorf("%s: answerShown = %v, want %v", c.what, c.out.answerShown, c.final)
		}
	}
}
