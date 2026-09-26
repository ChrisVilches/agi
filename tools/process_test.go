package tools

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// leaves builds a command that starts a grandchild which writes marker after a
// delay, and then waits for it. The delay is what makes the marker mean
// something: it appears only if the grandchild outlived whatever was supposed
// to end it.
func leaves(ctx context.Context, marker string, wait bool) *exec.Cmd {
	line := "(sleep 1; touch " + marker + ") & echo started"
	if wait {
		line += "; wait"
	}
	return exec.CommandContext(ctx, "/bin/sh", "-c", line)
}

// The whole point: cancelling reaches past the process that was started into
// everything it started, which killing the one process does not.
func TestKillGroupOnCancelEndsTheWholeTree(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survived")

	ctx, cancel := context.WithCancel(context.Background())
	cmd := leaves(ctx, marker, true)
	KillGroupOnCancel(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	go func() { _ = cmd.Wait() }()

	// Long enough for the shell to have started its child.
	time.Sleep(200 * time.Millisecond)
	cancel()
	// Well past the grandchild's delay, so it has had every chance.
	time.Sleep(3 * time.Second)

	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the grandchild left its mark (%v), so the kill reached the process "+
			"and not the group under it", err)
	}
}

// Without it, the same tree survives. This is here so the test above is known
// to be testing the helper rather than something the shell was going to do
// anyway.
func TestWithoutItTheTreeSurvives(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survived")

	ctx, cancel := context.WithCancel(context.Background())
	cmd := leaves(ctx, marker, true)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	go func() { _ = cmd.Wait() }()

	time.Sleep(200 * time.Millisecond)
	cancel()
	time.Sleep(3 * time.Second)

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the grandchild left no trace (%v); it was supposed to survive a kill "+
			"aimed at its parent alone, and if it no longer does then the test above "+
			"proves nothing", err)
	}
}

// A command nobody cancelled keeps what it deliberately left running. The
// group is killed when the context is done and at no other time, which is
// what separates stopping a run from tidying up after every call.
func TestKillGroupOnCancelSparesACommandThatFinished(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survived")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := leaves(ctx, marker, false)
	KillGroupOnCancel(cmd)
	// Run returns once the shell exits, which is at once: it does not wait
	// for the background job.
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait() = %v", err)
	}
	time.Sleep(3 * time.Second)

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the background job left no trace (%v), want a command that finished "+
			"to keep what it deliberately left running", err)
	}
}
