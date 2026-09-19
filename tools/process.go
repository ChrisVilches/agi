package tools

// TODO: this is unix and nothing here works on Windows. There is no
// syscall.SysProcAttr.Setpgid and no syscall.Kill there, so this file does
// not compile rather than quietly doing nothing, which is the right way
// round for something whose job is making sure nothing is left running. agi
// has other unix assumptions — session's flock, and the bash tool itself —
// so this is not the only thing in the way.
//
// The shape is already right when it comes. Windows groups processes with a
// job object: create one, assign the child to it, and terminating the job
// ends every process in it, including anything the child started. So the
// function below stays exactly as it is and only its two syscalls move into
// files chosen by build tag.
//
// Do not answer it with a no-op on Windows. A cleanup that silently does
// nothing is worse than one that does not build: it would leave a stopped
// run's processes behind on the platform nobody here can test, which is the
// failure this file exists to prevent.

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// KillGroupOnCancel arranges for a cancelled command to take everything it
// started with it.
//
// Every tool that runs a program has the same problem with what that program
// leaves behind, and no reason to answer it differently, so this lives here
// rather than in each of them — the argument Truncate already makes one
// field over.
//
// Killing a process does not kill what that process started. A shell handed a
// command line with more than one command in it stays alive as a parent, so
// killing the shell orphans its children rather than ending them, and the
// orphans go on doing whatever they were doing: a find over the whole disk, a
// connection to a server that will not answer. Those are exactly the cases
// Ctrl-C is pressed for, and a Ctrl-C that leaves them running has not done
// what the person meant by it.
//
// So the command is made the leader of a process group of its own, and
// cancelling the context kills the group rather than the one process. A
// process group is inherited the way a pipe is, so everything the command
// starts is in it, however deep, and one signal reaches all of them.
//
// Three things about this are worth knowing before changing it.
//
// It fires on cancellation only, which includes a timeout, and never on a
// command that finished by itself. A model that deliberately leaves something
// running in the background keeps it when the call succeeds; it loses it when
// the run is stopped, which is the trade being made and the whole point.
//
// It is SIGKILL and not a SIGTERM first. The run is being abandoned, so there
// is nothing for a graceful shutdown to be graceful for, and the default this
// replaces already killed the direct child outright — a ladder of two signals
// would add a second timer and a state to hold for no gain anybody could
// name.
//
// It does not replace the WaitDelay each caller sets, and both are wanted. A
// descendant that put itself in a group of its own — anything that calls
// setsid, which is what a daemon does — escapes the signal and can still hold
// the output pipe open, and WaitDelay is what stops that holding the run.
//
// One consequence is easy to miss: the command is no longer in agi's own
// process group, so a Ctrl-C typed at a terminal no longer reaches it
// directly, and this function is the only thing that ends it. That is more
// deterministic than what it replaces, where a command died of the terminal's
// signal at a terminal and of nothing at all when agi was piped — but it does
// mean a command is killed rather than interrupted, and one that cleaned up
// after itself on SIGINT no longer gets to.
//
// It must be called before the command is started, and it takes a command
// built with exec.CommandContext: without a context there is nothing to
// cancel and Cancel is never called.
func KillGroupOnCancel(cmd *exec.Cmd) {
	// Setpgid with no Pgid given makes the child the leader of a new group
	// whose id is the child's own pid, which is what lets the kill below name
	// the group without asking the kernel for anything.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	cmd.Cancel = func() error {
		// A negative pid is the process group of that id, which is why the
		// group had to be the child's own: -pid reaches the child and
		// everything under it, and nothing else.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			// The whole group is already gone, which is not a failure to end
			// it. os.ErrProcessDone is what the default Cancel reports for
			// the same case, and exec reads it as nothing having gone wrong.
			return os.ErrProcessDone
		}
		return err
	}
}
