package cli

import (
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/ChrisVilches/agi/tools"
)

// liveLine is the one line at the bottom of a terminal that says what a run is
// waiting on, and the writer every line of progress goes through on its way to
// the screen.
//
// agi prints a stream of lines and does not paint a screen, and this is the
// one exception: a request to the model is not streamed, so without it a slow
// model and a hung run look the same. It keeps to the rule everywhere it can.
// The line is redrawn in place and erased when the wait ends, so nothing that
// stays on the screen was ever painted; and it exists only where stdout is a
// terminal, never in a pipe, on standard error, in the log or in a snapshot.
//
// It is the writer as well as the spinner because things are printed while a
// wait is going on — a fork's progress during the fork call, a subagent's
// introduction during the subagents call — and a line printed on top of the
// spinner would share the row with it. Owning the writer means every write
// clears the spinner first, under the same lock the spinner draws under, with
// no caller having to know a spinner exists.
type liveLine struct {
	w  io.Writer
	mu sync.Mutex
	// waits are the waits under way, innermost last. Only the innermost is
	// drawn: a fork waiting on the model inside the parent's tool call says
	// more than the call it is inside.
	waits []*wait
	// drawn says the spinner is on the screen and has to be cleared before
	// anything else is written.
	drawn bool
	// midLine says the last write did not end its line. The spinner is not
	// drawn then, because returning to the start of the line would wipe what
	// is already on it.
	midLine bool
}

// wait is one thing being waited on.
type wait struct {
	what  string
	began time.Time
	frame int
}

// spinInterval is how often the spinner is redrawn. A wait shorter than this
// draws nothing, so a quick call does not flicker. A variable so a test can
// make it short.
var spinInterval = 100 * time.Millisecond

// clearLine returns to the start of the line and erases it.
const clearLine = "\r\x1b[K"

// spinFrames are the frames the spinner turns through.
var spinFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

func newLiveLine(w io.Writer) *liveLine { return &liveLine{w: w} }

// Write clears the spinner, if it is drawn, and writes p.
func (l *liveLine) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.clear()
	if len(p) > 0 {
		l.midLine = p[len(p)-1] != '\n'
	}
	return l.w.Write(p)
}

// clear erases the spinner. The lock must be held.
func (l *liveLine) clear() {
	if l.drawn {
		io.WriteString(l.w, clearLine)
		l.drawn = false
	}
}

// start begins a wait and returns the function that ends it.
//
// The returned function does not return until the spinner has stopped and the
// line is clear, so whatever the caller prints next starts on a clean line.
func (l *liveLine) start(what string) (done func()) {
	w := &wait{what: what, began: time.Now()}
	l.mu.Lock()
	l.waits = append(l.waits, w)
	l.mu.Unlock()

	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		tick := time.NewTicker(spinInterval)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				l.draw(w)
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			<-stopped
			l.mu.Lock()
			defer l.mu.Unlock()
			l.waits = slices.DeleteFunc(l.waits, func(x *wait) bool { return x == w })
			l.clear()
		})
	}
}

// draw redraws the spinner for w, if w is the innermost wait and the line is
// free.
func (l *liveLine) draw(w *wait) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.waits) == 0 || l.waits[len(l.waits)-1] != w || l.midLine {
		return
	}
	frame := spinFrames[w.frame%len(spinFrames)]
	w.frame++
	seconds := int(time.Since(w.began).Seconds())
	io.WriteString(l.w, clearLine+tools.Detail(fmt.Sprintf("%c %s · %ds", frame, w.what, seconds)))
	l.drawn = true
}
