package session

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// lockChildDir names the session a child process is to try to lock. It is how
// the test below tells the helper it is the child rather than an ordinary run
// of the test binary.
const lockChildDir = "AGI_TEST_LOCK_DIR"

// The lock is what makes a session one conversation: the second run is
// refused, and told which session it is that somebody else has open.
func TestLockKeepsASecondRunOut(t *testing.T) {
	dir := sessionDir(t, "chat")
	first, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	lock, err := first.Lock()
	if err != nil {
		t.Fatalf("Lock() = %v", err)
	}

	second, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	held, err := second.Lock()
	if err == nil {
		held.Release()
		t.Fatal("Lock() of a session already open = nil, want it refused")
	}
	if !errors.Is(err, ErrLocked) {
		t.Errorf("Lock() = %v, want ErrLocked", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("Lock() = %q, want the session it names to be %s", err, dir)
	}

	// And the session is free again once the first run has ended.
	if err := lock.Release(); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	next, err := second.Lock()
	if err != nil {
		t.Fatalf("Lock() after Release() = %v, want the session to be free", err)
	}
	if err := next.Release(); err != nil {
		t.Fatalf("Release() = %v", err)
	}
}

// The lock is the operating system's, so what it keeps out is another agi
// process and not merely another goroutine. Nothing short of a second process
// says that, so this test runs the test binary again as a child and has it try
// the same session.
func TestLockKeepsAnotherProcessOut(t *testing.T) {
	dir := sessionDir(t, "chat")
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	lock, err := s.Lock()
	if err != nil {
		t.Fatalf("Lock() = %v", err)
	}

	if got := lockInChildProcess(t, dir); got != "refused" {
		t.Errorf("another process %s the session this one holds, want it refused", got)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	if got := lockInChildProcess(t, dir); got != "took" {
		t.Errorf("another process %s the released session, want it taken", got)
	}
}

// lockInChildProcess runs the helper below in a process of its own and reports
// what became of its attempt to lock dir.
func lockInChildProcess(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestLockInChildProcess", "-test.v")
	cmd.Env = append(os.Environ(), lockChildDir+"="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child process = %v:\n%s", err, out)
	}
	switch {
	case strings.Contains(string(out), "lock-refused"):
		return "refused"
	case strings.Contains(string(out), "lock-taken"):
		return "took"
	default:
		t.Fatalf("child process said nothing about the lock:\n%s", out)
		return ""
	}
}

// TestLockInChildProcess is the child half of the test above and does nothing
// when run on its own, which is how it behaves in an ordinary test run.
func TestLockInChildProcess(t *testing.T) {
	dir := os.Getenv(lockChildDir)
	if dir == "" {
		t.Skip("the child half of TestLockKeepsAnotherProcessOut")
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	lock, err := s.Lock()
	if err != nil {
		t.Log("lock-refused")
		return
	}
	t.Log("lock-taken")
	if err := lock.Release(); err != nil {
		t.Fatalf("Release() = %v", err)
	}
}

// A session with nowhere to write has nothing for a second run to collide
// with, so it locks nothing and says so by succeeding.
func TestLockingADiscardedSessionDoesNothing(t *testing.T) {
	lock, err := Discarded().Lock()
	if err != nil {
		t.Fatalf("Lock() of a discarded session = %v, want no error", err)
	}
	if err := lock.Release(); err != nil {
		t.Errorf("Release() = %v", err)
	}
}

// Releasing is about the session being free afterwards, so a lock given back
// twice is not a failure.
func TestReleaseIsIdempotent(t *testing.T) {
	s, err := Open(sessionDir(t, "chat"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	lock, err := s.Lock()
	if err != nil {
		t.Fatalf("Lock() = %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Errorf("second Release() = %v, want no error", err)
	}
}

// The lock file is the session's own and is left where it is: removing it on
// release is what would let two runs lock two different files at one path.
func TestLockFileStaysAndIsPrivate(t *testing.T) {
	dir := sessionDir(t, "chat")
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	lock, err := s.Lock()
	if err != nil {
		t.Fatalf("Lock() = %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release() = %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, lockFileName))
	if err != nil {
		t.Fatalf("Stat() = %v, want the lock file to still be there", err)
	}
	if perm := info.Mode().Perm(); perm != filePerm {
		t.Errorf("lock file has mode %v, want %v", perm, filePerm)
	}
	if info.Size() != 0 {
		t.Errorf("lock file holds %d bytes, want it empty: the lock is the kernel's, not the file's contents", info.Size())
	}
}
