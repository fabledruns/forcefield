//go:build !windows

package pty

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Child is one openpty child: the pty master (parent side), a
// separate stderr pipe for markers, and the exec'd process in its own
// session with the slave as controlling terminal.
type Child struct {
	cmd    *exec.Cmd
	master *os.File
	errR   *os.File
	pid    int
}

// Stderr returns the marker pipe (separate from the pty, so marker
// lines parse cleanly without terminal escape processing).
func (c *Child) Stderr() *os.File { return c.errR }

// Output returns the pty master for draining.
func (c *Child) Output() *os.File { return c.master }

// Pid returns the child PID.
func (c *Child) Pid() int { return c.pid }

// WriteInput writes terminal input (keys) to the child.
func (c *Child) WriteInput(p []byte) (int, error) { return c.master.Write(p) }

// Start opens a pty pair, sizes the slave, and spawns the child with
// the slave as its controlling terminal.
func Start(opts Options) (*Child, error) {
	if opts.Path == "" {
		return nil, fmt.Errorf("pty: empty command")
	}
	cols, rows := dims(opts)
	master, slave, err := openPty(cols, rows)
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		_ = master.Close()
		_ = slave.Close()
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("pty: stderr pipe: %w", err)
	}
	cmd := exec.Command(opts.Path, opts.Args...)
	cmd.Env = opts.Env
	cmd.Dir = opts.Dir
	slaveFd := int(slave.Fd())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true, // new session + process group (pgid = pid)
		Setctty: true,
		Ctty:    slaveFd,
	}
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = errW
	if err := cmd.Start(); err != nil {
		_ = errR.Close()
		_ = errW.Close()
		cleanup()
		return nil, fmt.Errorf("pty: start: %w", err)
	}
	// The child owns its copies now; the parent keeps master + errR.
	_ = slave.Close()
	_ = errW.Close()
	return &Child{cmd: cmd, master: master, errR: errR, pid: cmd.Process.Pid}, nil
}

// Wait blocks for process exit and returns its code.
func (c *Child) Wait() (int, error) {
	if err := c.cmd.Wait(); err != nil {
		if c.cmd.ProcessState != nil {
			return c.cmd.ProcessState.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}

// Kill signals the whole process group.
func (c *Child) Kill() error {
	if c.cmd.Process == nil {
		return fmt.Errorf("pty: nothing to kill")
	}
	// Negative pid addresses the child's process group (setsid).
	if err := syscall.Kill(-c.pid, syscall.SIGKILL); err != nil {
		return c.cmd.Process.Kill()
	}
	return nil
}

// Alive reports whether signalling the child succeeds.
func (c *Child) Alive() bool {
	if c.pid <= 0 {
		return false
	}
	return syscall.Kill(c.pid, 0) == nil
}

// Close releases the master and marker pipe. Closing the master
// delivers EOF/SIGHUP to the session side.
func (c *Child) Close() error {
	if c.master != nil {
		_ = c.master.Close()
		c.master = nil
	}
	if c.errR != nil {
		_ = c.errR.Close()
		c.errR = nil
	}
	return nil
}

// openPty opens /dev/ptmx, unlocks it, resolves the slave name, and
// installs the fixed window size before any child output exists.
func openPty(cols, rows int) (master, slave *os.File, err error) {
	masterFd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("pty: open ptmx: %w", err)
	}
	master = os.NewFile(uintptr(masterFd), "ptmx")
	cleanup := func() { _ = master.Close() }
	slaveName, err := unlockPty(masterFd)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	slaveFd, err := unix.Open(slaveName, unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("pty: open slave %s: %w", slaveName, err)
	}
	slave = os.NewFile(uintptr(slaveFd), "pts")
	if err := unix.IoctlSetWinsize(slaveFd, unix.TIOCSWINSZ,
		&unix.Winsize{Row: uint16(rows), Col: uint16(cols)}); err != nil {
		_ = slave.Close()
		cleanup()
		return nil, nil, fmt.Errorf("pty: winsize %dx%d: %w", cols, rows, err)
	}
	return master, slave, nil
}

func ioctl(fd int, req uint, arg unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(req), uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}
