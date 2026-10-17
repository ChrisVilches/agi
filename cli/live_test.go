package cli

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fastSpin makes the spinner repaint quickly for one test, so a test can see it
// drawn without sleeping for long.
func fastSpin(t *testing.T) {
	t.Helper()
	previous := spinInterval
	spinInterval = time.Millisecond
	t.Cleanup(func() { spinInterval = previous })
}

// syncBuffer is a bytes.Buffer safe to read while the spinner writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor polls until the buffer holds want, or fails the test.
func waitFor(t *testing.T, b *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(b.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("output never contained %q; got %q", want, b.String())
		}
		time.Sleep(time.Millisecond)
	}
}

// Nothing is drawn anywhere but on a terminal's screen: not into a pipe, not
// onto standard error in a piped --verbose run, and not for an agent whose
// progress nobody sees.
func TestSpinnerIsOnlyAtATerminal(t *testing.T) {
	fastSpin(t)
	var stdout, stderr bytes.Buffer
	for name, o := range map[string]output{
		"piped":             route(false, &stdout, &stderr, false),
		"piped and verbose": route(false, &stdout, &stderr, true),
		"quiet":             quiet(),
	} {
		done := o.waiting("inference")
		time.Sleep(10 * time.Millisecond)
		done()
		if stdout.Len() != 0 || stderr.Len() != 0 {
			t.Errorf("%s: wrote %q and %q, want nothing", name, stdout.String(), stderr.String())
		}
	}
}

// At a terminal the line is drawn while the wait lasts and is gone once it
// ends, so what is printed next starts at the beginning of a clean line.
func TestSpinnerIsDrawnAndErased(t *testing.T) {
	fastSpin(t)
	var screen syncBuffer
	o := route(true, &screen, io.Discard, false)

	done := o.waiting("inference")
	waitFor(t, &screen, "inference")
	done()
	o.step("next\n")

	got := screen.String()
	if !strings.HasSuffix(got, clearLine+"next\n") {
		t.Errorf("screen = %q, want the spinner cleared before the next line", got)
	}
	// Once done has returned, nothing is drawn again.
	time.Sleep(10 * time.Millisecond)
	if screen.String() != got {
		t.Errorf("the spinner drew after done returned: %q", screen.String()[len(got):])
	}
}

// A wait that ends before the first repaint draws nothing, so a quick call
// does not flicker.
func TestQuickWaitDrawsNothing(t *testing.T) {
	var screen syncBuffer
	o := route(true, &screen, io.Discard, false)
	o.waiting("inference")()
	if got := screen.String(); got != "" {
		t.Errorf("screen = %q, want nothing for a wait shorter than a repaint", got)
	}
}

// Progress printed while a wait is going on — a fork's lines, a subagent's
// introduction — clears the spinner first, so the two never share a line.
func TestProgressDuringAWaitClearsTheSpinner(t *testing.T) {
	fastSpin(t)
	var screen syncBuffer
	o := route(true, &screen, io.Discard, false)

	done := o.waiting("running")
	waitFor(t, &screen, "running")
	o.step("a line from inside\n")
	done()

	got := screen.String()
	at := strings.Index(got, "a line from inside")
	if at < len(clearLine) || got[at-len(clearLine):at] != clearLine {
		t.Errorf("screen = %q, want the spinner cleared right before the line", got)
	}
}

// The spinner never draws while a line is half written, because returning to
// the start of it would wipe what is there.
func TestSpinnerWaitsForTheEndOfALine(t *testing.T) {
	fastSpin(t)
	var screen syncBuffer
	o := route(true, &screen, io.Discard, false)

	o.step("half a line")
	done := o.waiting("running")
	time.Sleep(20 * time.Millisecond)
	done()

	if got := screen.String(); got != "half a line" {
		t.Errorf("screen = %q, want the half line left alone", got)
	}
}

// A fork's own wait inside the parent's tool call shows in place of the
// parent's, and the parent's comes back once the fork's is over.
func TestNestedWaitsShowTheInnermost(t *testing.T) {
	fastSpin(t)
	var screen syncBuffer
	o := route(true, &screen, io.Discard, false)

	outer := o.waiting("running")
	waitFor(t, &screen, "running")
	inner := o.nested().waiting("inference")
	waitFor(t, &screen, "inference")
	inner()

	mark := len(screen.String())
	time.Sleep(20 * time.Millisecond)
	if after := screen.String()[mark:]; !strings.Contains(after, "running") || strings.Contains(after, "inference") {
		t.Errorf("after the inner wait ended the screen showed %q, want the outer one back", after)
	}
	outer()
}
