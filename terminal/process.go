package terminal

import (
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// sessionGroups returns the id of every process group in session sid, read
// out of /proc.
//
// The session's leader's own group is always in it, which is all a /proc that
// cannot be read leaves: the program itself is still signalled, and only what
// it put in groups of its own is missed.
func sessionGroups(sid int) map[int]struct{} {
	groups := map[int]struct{}{sid: {}}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return groups
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if s, err := unix.Getsid(pid); err == nil && s == sid {
			if pgid, err := unix.Getpgid(pid); err == nil {
				groups[pgid] = struct{}{}
			}
		}
	}
	return groups
}

// child is the program a terminal started: how it ended, and how to end it.
//
// It is reaped by the goroutine that started it, which calls setExited; see
// ptySession.spawn for why nothing else may wait for it.
type child struct {
	pid int
	// exited is closed once the program has been reaped.
	exited chan struct{}

	mu         sync.Mutex
	exitCode   *int
	exitSignal *int
}

func newChild() *child {
	return &child{exited: make(chan struct{})}
}

// setExited records how the program ended. A program that exited has a code,
// one a signal ended has the signal, and the two are kept apart because "was
// killed" and "failed" are different answers to why a program is gone.
func (c *child) setExited(state *os.ProcessState) {
	c.mu.Lock()
	if ws, ok := state.Sys().(syscall.WaitStatus); ok {
		if ws.Exited() {
			code := ws.ExitStatus()
			c.exitCode = &code
		} else if ws.Signaled() {
			signal := int(ws.Signal())
			c.exitSignal = &signal
		}
	}
	c.mu.Unlock()
	close(c.exited)
}

func (c *child) gone() bool {
	select {
	case <-c.exited:
		return true
	default:
		return false
	}
}

// wait reports whether the program was reaped within timeout.
func (c *child) wait(timeout time.Duration) bool {
	select {
	case <-c.exited:
		return true
	case <-time.After(timeout):
		return false
	}
}

// signalSession sends signal to every process group in the program's session.
func (c *child) signalSession(signal syscall.Signal) {
	if c.pid <= 0 {
		return
	}
	for pgid := range sessionGroups(c.pid) {
		_ = syscall.Kill(-pgid, signal)
	}
}

// terminate ends the program's whole session, asking twice before it insists:
// SIGHUP, which is what a terminal closing has always sent, then SIGTERM, then
// SIGKILL, each followed by a wait for the program to be reaped. It reports
// whether it was.
//
// Each signal is sent with SIGCONT beside it, because a stopped job — one
// suspended with Ctrl-Z — does not act on a signal until it is continued, and
// would otherwise sit through all three.
//
// The sweep at the end runs whatever happened before it. A job that ignored
// SIGHUP, the way anything run under nohup does, is still alive after the
// shell has gone, and the shell going is not a reason for it to go too.
func (c *child) terminate(grace time.Duration) bool {
	steps := []struct {
		signal syscall.Signal
		wait   time.Duration
	}{
		{syscall.SIGHUP, grace / 2},
		{syscall.SIGTERM, grace / 2},
		{syscall.SIGKILL, time.Second},
	}
	for _, step := range steps {
		if c.gone() {
			break
		}
		c.signalSession(step.signal)
		c.signalSession(syscall.SIGCONT)
		if c.wait(step.wait) {
			break
		}
	}
	c.signalSession(syscall.SIGKILL)
	return c.gone()
}

func (c *child) status() (running bool, exitCode, exitSignal *int) {
	running = !c.gone()
	c.mu.Lock()
	defer c.mu.Unlock()
	return running, c.exitCode, c.exitSignal
}
