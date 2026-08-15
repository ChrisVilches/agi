// Package eventlog records the stream of things that happen during a run.
//
// agi reports itself in three ways, and they are not variations on one
// mechanism. Terminal output is for a person watching a run and exists to be
// read at a glance, so it is styled, partial, and thrown away when nobody is
// at a terminal. A session snapshot is the conversation as it stood when the
// run finished, and exists so the next run can continue it. This package is
// the third: one line per event, in the order the events happened, written to
// a file and never to the terminal, so that a run can be traced and
// reproduced after the fact.
//
// Two consequences follow from that purpose and are worth stating plainly.
//
// The log is append-only and is never rewritten. When a run takes something
// back out of the conversation — the scaffolding of a schema phase, say — the
// removal is itself an event, recorded like any other. What the snapshot
// drops, the log keeps, which is why the log and not the snapshot is the thing
// to read when asking what actually happened.
//
// The log never reaches stdout or stderr. Progress printed to a pipe would
// corrupt whatever is reading it, and that is the whole reason agi is careful
// about which stream carries what; a debugging log that landed there would
// undo it. A log goes to a file or nowhere.
package eventlog

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// buffer is how many events may be waiting to be written before the run that
// is recording them has to wait. It is generous, because an event is small and
// a run that blocks on its own log is a run slowed down by being observed.
const buffer = 256

// filePerm keeps the log to its owner. It holds the prompt, the answers, and
// everything a tool printed, on a machine whose temporary directory everyone
// can read.
const filePerm fs.FileMode = 0o600

// mainAgent names the run the user started, as opposed to a subagent.
const mainAgent = "main"

// Log records events. It is safe to use from several goroutines at once, which
// is not incidental: subagents run alongside each other and alongside the run
// that spawned them, and all of them write to the one file.
//
// A Log is a handle onto a shared sink plus the name of the agent using it.
// Agent returns another handle onto the same sink under another name, so the
// lines of concurrent agents can be told apart in a single stream.
//
// The zero value, and the value Discarded returns, records nothing: every
// method works and does nothing at all. That is what --no-log gives a run, and
// it is why no caller has to ask whether there is a log before recording.
type Log struct {
	// sink is where events are written, nil when there is nowhere.
	sink *sink
	// agent names the agent recording, stamped on every event it records.
	agent string
}

// sink is the writing end, shared by every Log handle on one file.
type sink struct {
	// events carries records to the writer goroutine. A channel is what makes
	// a Log safe to share: the writer is the only thing that touches the file,
	// and everything else only ever sends.
	events chan Event
	// closed is closed by Close to stop accepting events. An agent still
	// trying to record after that is not blocked forever.
	closed chan struct{}
	// done is closed by the writer once it has drained and stopped.
	done chan struct{}
	// once makes Close idempotent, since the run that opened a log is not
	// always the only one that can reach it.
	once sync.Once
	// agents numbers nested agents, so two subagents given the same name are
	// still two names in the file.
	agents atomic.Int64
	// err is the first write that failed, reported by Close. A log that cannot
	// be written does not stop the run: the answer the user asked for is worth
	// more than the record of how it was reached.
	err   error
	errMu sync.Mutex
}

// Open returns a log appending to the file at path, creating it if it is not
// there. An empty path returns a discarded log, so a run with nowhere to store
// anything needs no special case.
//
// The file is appended to rather than replaced because a session is resumed by
// running agi again against the same directory: the second run continues the
// conversation, and its events continue the log of it.
func Open(path string) (*Log, error) {
	if path == "" {
		return Discarded(), nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, filePerm)
	if err != nil {
		return nil, fmt.Errorf("eventlog: %w", err)
	}
	return newLog(f), nil
}

// Discarded returns a log that records nothing, which is what --no-log asks
// for: the stream becomes a pass-through and the run is unobserved.
func Discarded() *Log { return &Log{} }

// newLog starts the writer goroutine on w and returns the log feeding it.
func newLog(w io.WriteCloser) *Log {
	s := &sink{
		events: make(chan Event, buffer),
		closed: make(chan struct{}),
		done:   make(chan struct{}),
	}
	go s.write(w)
	return &Log{sink: s, agent: mainAgent}
}

// Recording reports whether the log writes anything, which is worth asking
// only to tell the user where the log is.
func (l *Log) Recording() bool { return l != nil && l.sink != nil }

// Agent returns a log for a nested run, writing to the same file under a name
// of its own. The name is numbered, so several subagents with the same title
// are still distinguishable.
//
// A subagent is an ordinary run and records the same events an ordinary run
// does; the agent field is the only thing that says whose they are.
func (l *Log) Agent(name string) *Log {
	if !l.Recording() {
		return Discarded()
	}
	return &Log{
		sink:  l.sink,
		agent: fmt.Sprintf("%s-%d", name, l.sink.agents.Add(1)),
	}
}

// Name returns the name of the agent this log records under.
func (l *Log) Name() string {
	if l == nil {
		return ""
	}
	return l.agent
}

// Record writes one event. The caller fills in what the event is about; the
// time, the sequence number, and which agent recorded it are stamped here,
// because no caller should be able to get them wrong.
//
// It never fails and never blocks for long: an event that cannot be written is
// dropped rather than propagated, since a run must not fail because the record
// of it could not be kept.
func (l *Log) Record(e Event) {
	if !l.Recording() {
		return
	}
	e.Agent = l.agent
	e.Time = time.Now()

	select {
	case l.sink.events <- e:
	case <-l.sink.closed:
		// The log is shutting down and this event missed it. Better a missing
		// line than a goroutine that never returns.
	}
}

// Close stops recording and waits for what has been recorded to reach the
// file, returning the first write that failed if any did. It is idempotent,
// and closing a discarded log does nothing.
//
// Events recorded after Close are dropped. Nothing should be recording by
// then: a run closes its log when the run is over.
func (l *Log) Close() error {
	if !l.Recording() {
		return nil
	}
	l.sink.once.Do(func() {
		close(l.sink.closed)
		<-l.sink.done
	})
	return l.sink.failure()
}

// write is the writer goroutine: the only thing that touches the file, which
// is what makes the file safe to share and its lines whole.
//
// Sequence numbers are assigned here rather than where the event was recorded,
// so they run in the order the lines do and not in the order several agents
// happened to reach the channel. They count this run's events: a file appended
// to by a second run starts again at one, which is what RunStart marks. What
// the event says about itself — its time above all — is the caller's, taken
// when the thing happened rather than when it was written down.
func (s *sink) write(w io.WriteCloser) {
	defer close(s.done)
	defer func() {
		if err := w.Close(); err != nil {
			s.fail(err)
		}
	}()

	enc := json.NewEncoder(w)
	var seq int64

	record := func(e Event) {
		seq++
		e.Seq = seq
		// Encode writes the value and a newline, which is the format: one
		// event per line, readable a line at a time by anything.
		if err := enc.Encode(e); err != nil {
			s.fail(err)
		}
	}

	for {
		select {
		case e := <-s.events:
			record(e)
		case <-s.closed:
			// Drain what was recorded before the close and stop. Anything
			// recorded after it is not owed a line.
			for {
				select {
				case e := <-s.events:
					record(e)
				default:
					return
				}
			}
		}
	}
}

// fail keeps the first write failure. The rest are almost certainly the same
// failure repeated, and a log that reported every one of them would be a worse
// log than one that reported none.
func (s *sink) fail(err error) {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if s.err == nil {
		s.err = fmt.Errorf("eventlog: %w", err)
	}
}

// failure returns the first write failure, if there was one.
func (s *sink) failure() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}
