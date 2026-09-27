package terminal

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// errPTYClosed is what a write reports once the PTY is closed. It is unexported
// because nothing outside the package reaches a closed PTY: input is refused
// with ErrNotRunning before it gets here.
var errPTYClosed = errors.New("terminal: the PTY is closed")

// ptySession is the master side of a PTY whose slave side is a program.
//
// It knows about the kernel's PTY and nothing else: it starts the program,
// reaps it, and reads, writes and resizes. What the bytes mean is the screen's
// business, and keeping the two apart is what lets the screen be tested by
// feeding it bytes with no program anywhere.
type ptySession struct {
	argv []string
	env  []string
	dir  string
	cols int
	rows int

	// master is set once by spawn, before anything else can use it, and
	// never reassigned. It is an os.File, which is what makes closing it safe
	// while a read or a write is in flight on it.
	master    *os.File
	closeOnce sync.Once
}

// openMaster opens a PTY pair and hands back a master that honors read
// deadlines.
//
// creack/pty returns the master in blocking mode, and a blocking file ignores
// a deadline without saying so: the reader would wait for output for as long
// as the program stayed quiet, and Close would wait with it. So the descriptor
// is duplicated, made non-blocking, and wrapped again with os.NewFile, which
// puts its reads through Go's poller where deadlines work.
//
// The master's Fd must never be called after this, because calling it puts
// the file back in blocking mode and undoes all of it. Anything that needs the
// raw descriptor goes through control.
func openMaster() (master, slave *os.File, err error) {
	m, s, err := pty.Open()
	if err != nil {
		return nil, nil, err
	}
	fd, err := unix.Dup(int(m.Fd()))
	m.Close()
	if err != nil {
		s.Close()
		return nil, nil, err
	}
	unix.CloseOnExec(fd)
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		s.Close()
		return nil, nil, err
	}
	return os.NewFile(uintptr(fd), "/dev/ptmx"), s, nil
}

// spawn starts the program on a new PTY and returns its pid. onExit is called
// once, with how the program ended, after it has been reaped.
//
// The program is made a session leader with the slave as its controlling
// terminal, so its pid is also its session's id and its process group's. That
// is what lets Close find everything it started: they are all in its session.
//
// The program is asked to be sent SIGHUP if the host dies, and that request is
// the reason for the goroutine below. The kernel delivers it when the thread
// that forked the program exits — the thread, not the process — and the Go
// runtime retires threads whenever it likes, which would hang the program up
// at some arbitrary moment in the middle of a run. So the fork happens on a
// thread locked to one goroutine, and that same goroutine waits for the
// program and only then returns, which is what lets the thread go. Two ways of
// changing this break it: moving the wait somewhere else lets the thread go
// while the program runs, and parking the goroutine forever keeps one thread
// per terminal ever started, which TestNoThreadLeakAcrossManyTerminals is
// there to catch.
func (p *ptySession) spawn(onExit func(*os.ProcessState)) (int, error) {
	master, slave, err := openMaster()
	if err != nil {
		return 0, err
	}
	defer slave.Close()

	// The size is set before the program starts, so a program that reads it
	// once as it starts up reads the right one.
	if err := pty.Setsize(slave, &pty.Winsize{Cols: uint16(p.cols), Rows: uint16(p.rows)}); err != nil {
		master.Close()
		return 0, err
	}

	cmd := exec.Command(p.argv[0], p.argv[1:]...)
	cmd.Env = p.env
	cmd.Dir = p.dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:    true,
		Setctty:   true,
		Ctty:      0,
		Pdeathsig: syscall.SIGHUP,
	}

	started := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		if err := cmd.Start(); err != nil {
			started <- err
			return
		}
		started <- nil
		// The standard streams are files, so exec starts no goroutines to
		// copy them and the wait is for the program alone.
		_ = cmd.Wait()
		onExit(cmd.ProcessState)
	}()
	if err := <-started; err != nil {
		master.Close()
		return 0, err
	}
	p.master = master
	return cmd.Process.Pid, nil
}

// read reads whatever output is available, waiting at most timeout for some.
//
// It returns how much arrived, which may be nothing, and eof once no process
// holds the slave side any more and nothing ever will arrive again.
func (p *ptySession) read(buf []byte, timeout time.Duration) (n int, eof bool) {
	if p.master == nil {
		return 0, true
	}
	if err := p.master.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return 0, true
	}
	n, err := p.master.Read(buf)
	if n > 0 {
		return n, false
	}
	if err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		return 0, false
	}
	// Linux reports EIO on the master once every descriptor of the slave is
	// closed, which is a PTY's end of file.
	return 0, true
}

// write writes all of data, since os.File.Write loops over a partial write.
func (p *ptySession) write(data []byte) error {
	if p.master == nil {
		return errPTYClosed
	}
	_, err := p.master.Write(data)
	if errors.Is(err, os.ErrClosed) {
		return errPTYClosed
	}
	return err
}

// resize changes the size the kernel holds for the PTY, and the kernel sends
// SIGWINCH to the foreground process group.
func (p *ptySession) resize(cols, rows int) error {
	if p.master == nil {
		return nil
	}
	return p.control(func(fd int) error {
		return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Col: uint16(cols), Row: uint16(rows)})
	})
}

// control runs f on the master's raw descriptor without calling Fd, which
// would put the file back in blocking mode; see openMaster.
func (p *ptySession) control(f func(fd int) error) error {
	raw, err := p.master.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := raw.Control(func(fd uintptr) { ferr = f(int(fd)) }); err != nil {
		return err
	}
	return ferr
}

func (p *ptySession) close() {
	p.closeOnce.Do(func() {
		if p.master != nil {
			p.master.Close()
		}
	})
}
