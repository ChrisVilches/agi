package session

// TODO: this is a unix lock and nothing here works on Windows. syscall.Flock
// does not exist there, so the package does not compile rather than
// misbehaving at run time, which is the right way round for something whose
// whole job is keeping two processes apart. agi has other unix assumptions —
// the bash tool for one — so this is not the only thing in the way, and none
// of it is worth doing until somebody can run it.
//
// The shape is already right when it comes. Windows has the same primitive
// under another name: LockFileEx with LOCKFILE_EXCLUSIVE_LOCK and
// LOCKFILE_FAIL_IMMEDIATELY takes an exclusive lock on an open handle without
// waiting, closing the handle releases it, and the kernel drops it when the
// process dies. So Lock, Release and ErrLocked stay exactly as they are and
// only the two syscalls move into files chosen by build tag.
//
// Do not answer it with a no-op on Windows. A lock that silently succeeds is
// worse than one that does not build: it would let two runs share a session
// and produce a snapshot belonging to neither, which is the failure this file
// exists to prevent, and it would do it on the platform nobody here can test.
//
// Both rules the unix version turns on hold there too, and more firmly.
// Windows will not let an open file be unlinked at all, so the lock file could
// not be removed on release even by mistake; and Release already runs before
// the directory is removed, which --rm needs on a system that refuses to
// delete a directory holding an open file.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockFileName is the file whose lock says a run has the session open. It is
// created empty and stays empty: nothing is written into it, because anything
// written there would be a fourth account of the run, and a stale one — a pid
// read out of it says who held the lock at some moment that has passed, which
// is exactly the question the lock already answers correctly.
//
// It is hidden because it is not part of what the session holds. A session
// directory is a conversation and the record of how it was produced, and
// whoever lists one is looking for those; this is agi's own bookkeeping —
// empty, never read, and never removed — so it stays out of the way of the
// files that are worth seeing.
const lockFileName = ".lock"

// ErrLocked reports that another agi process has the session open. It is a
// sentinel so that a caller can tell a session in use from a session that
// could not be opened at all; the message names the directory either way.
var ErrLocked = errors.New("session: another agi run has this session open")

// Lock is a session held for the length of one run. Release gives it back.
//
// The zero value holds nothing and releasing it does nothing, which is what a
// session with nowhere to write returns: there is no directory, so there is
// nothing for a second run to collide with.
type Lock struct {
	// file is the open lock file. The lock belongs to this open file
	// description rather than to the path, so keeping the file open is what
	// keeps the lock held, and closing it is what releases it.
	file *os.File
}

// lockPath returns where the lock file lives.
func (s *Session) lockPath() string { return filepath.Join(s.dir, lockFileName) }

// Lock takes the session for this run, refusing with ErrLocked when another
// agi process already has it. A session with nowhere to write is not locked at
// all and reports no error.
//
// Both files in a session assume one writer and neither says so. Two runs
// finishing at once leave whichever snapshot was written last, and the other
// run's turns are simply gone. The log fares differently but no better: it is
// appended to, so nothing is lost, but the two runs' lines are shuffled
// together and each numbers its turns from the beginning, so the file reads as
// one incoherent run. This is what makes "a session is a conversation" true
// rather than merely usual.
//
// The lock is the operating system's, taken with flock on a file in the
// session's directory, because the things to be kept apart are separate
// invocations of agi that share nothing but the filesystem. A mutex would only
// order the goroutines inside one of them, which was never the problem.
//
// It is flock rather than a file created exclusively, and the difference is
// what happens to a run that is killed outright. The kernel drops an flock
// when the process holding it dies, however it dies, so there is no such thing
// as a stale lock to be cleaned up or overridden. A lock file whose existence
// is the lock would outlive a kill -9 and leave the user's own conversation
// permanently refused, with nothing to do about it but delete a file agi told
// them not to worry about.
//
// It is also why nothing removes the lock file. Unlinking it on release is the
// one way to reintroduce the bug the lock exists to prevent: a second run can
// have the file open and be waiting on it at the moment it is unlinked, after
// which a third run creates a fresh file at the same path and locks that, and
// two runs hold locks nobody would say conflict. The file is empty and stays
// where it is, and the lock it carries is the kernel's business.
func (s *Session) Lock() (*Lock, error) {
	if !s.Stored() {
		return &Lock{}, nil
	}

	file, err := os.OpenFile(s.lockPath(), os.O_CREATE|os.O_RDWR, filePerm)
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s; wait for that run to finish, or use another session", ErrLocked, s.dir)
		}
		return nil, fmt.Errorf("session: locking %s: %w", s.lockPath(), err)
	}
	return &Lock{file: file}, nil
}

// Release gives the session back, so that the next run can have it. It happens
// however the run ended — an answer, a failure, the user stopping it, the
// model ending it — and a lock released twice is not an error, since what
// matters is that it is not held afterwards.
//
// Closing the file is the release: the lock belongs to the open file
// description, so there is nothing to unlock first. A run that never gets this
// far is released by the kernel when the process ends, which is the whole
// reason the lock is taken this way.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	if err := file.Close(); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	return nil
}
