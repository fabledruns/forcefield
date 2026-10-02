//go:build windows

package pty

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Child is one ConPTY child: input pipe (parent writes), output pipe
// (parent reads), a separate marker pipe for the child's stderr, and
// retained process + job handles.
//
// stderr gets its own pipe rather than sharing the console: ConPTY
// relays it through the console host, which reorders and interleaves
// writes that arrive milliseconds apart ("stage-agents" arriving
// before "stage-skills", two markers fused into one name). A dedicated
// pipe preserves marker order and line integrity, matching Unix.
type Child struct {
	pid    int
	inW    *os.File // parent -> child stdin
	outR   *os.File // child stdout -> parent
	errR   *os.File // child stderr (markers) -> parent
	hPC    windows.Handle
	job    windows.Handle
	proc   windows.Handle
	hasJob bool

	waitOnce sync.Once
	exitCode int
	waitErr  error
}

// Stderr returns the marker pipe (separate from the console stream, so
// marker lines parse without terminal escape processing).
func (c *Child) Stderr() *os.File { return c.errR }

// Output returns the pty output stream for draining.
func (c *Child) Output() *os.File { return c.outR }

// Pid returns the child PID.
func (c *Child) Pid() int { return c.pid }

// WriteInput writes terminal input (keys) to the child.
func (c *Child) WriteInput(p []byte) (int, error) { return c.inW.Write(p) }

// hpcValue reinterprets an HPCON as the lpValue that
// PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE expects. The attribute wants
// the handle's own value, not a pointer to it, and the handle is a
// kernel object rather than a Go pointer, so reading its bits out of
// a live variable (rather than converting a uintptr, which vet
// rejects) is both correct and stable.
func hpcValue(h windows.Handle) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&h))
}

// Start creates the ConPTY pair, the pseudoconsole, and the child
// process attached to it.
//
// Two details are load-bearing (Windows console semantics, verified
// against the platform docs and a working reference):
//
//   - STARTF_USESTDHANDLES must be set. Without it the kernel
//     duplicates the parent's own std handles into console children;
//     under any runner with redirected stdio (go test, CI, hpov
//     itself) the child silently bypasses the pseudoconsole.
//   - The child inherits exactly one handle, its stderr marker pipe,
//     named in a PROC_THREAD_ATTRIBUTE_HANDLE_LIST. The console
//     supplies stdin/stdout; inheriting the harness's own std handles
//     instead would let the child bypass the pseudoconsole.
//
// The child starts suspended so job assignment closes the
// Start→Track race (mirrors internal/process); it is resumed before
// Start returns.
func Start(opts Options) (*Child, error) {
	cols, rows := dims(opts)
	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		InheritHandle:      1,
		SecurityDescriptor: nil,
	}
	// inR/outW feed the pseudoconsole; inW/outR are the parent's ends.
	// errR/errW are the child's stderr marker pipe and never touch the
	// console.
	var inR, inW, outR, outW, errR, errW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, sa, 0); err != nil {
		return nil, fmt.Errorf("pty: input pipe: %w", err)
	}
	if err := windows.CreatePipe(&outR, &outW, sa, 0); err != nil {
		windows.CloseHandle(inR)
		windows.CloseHandle(inW)
		return nil, fmt.Errorf("pty: output pipe: %w", err)
	}
	if err := windows.CreatePipe(&errR, &errW, sa, 0); err != nil {
		windows.CloseHandle(inR)
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		windows.CloseHandle(outW)
		return nil, fmt.Errorf("pty: stderr pipe: %w", err)
	}
	child := &Child{pid: -1}
	closeOnErr := func() {
		windows.CloseHandle(inR)
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		windows.CloseHandle(outW)
		windows.CloseHandle(errR)
		windows.CloseHandle(errW)
		if child.hPC != 0 {
			windows.ClosePseudoConsole(child.hPC)
			child.hPC = 0
		}
	}
	var hPC windows.Handle
	if err := windows.CreatePseudoConsole(
		windows.Coord{X: int16(cols), Y: int16(rows)}, inR, outW, 0, &hPC); err != nil {
		closeOnErr()
		return nil, fmt.Errorf("pty: create pseudo console %dx%d: %w", cols, rows, err)
	}
	child.hPC = hPC

	// Two attributes: the pseudoconsole, and an explicit handle list so
	// the child inherits only its stderr pipe. Inheritance must be on
	// (the child needs errW); the handle list is what keeps it from
	// inheriting the harness's own std handles.
	attr, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		closeOnErr()
		return nil, fmt.Errorf("pty: attribute list: %w", err)
	}
	defer attr.Delete()
	// PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE takes the HPCON value
	// itself as lpValue (unlike attributes that take a pointer to
	// their value): &hPC silently leaves the child on the parent's
	// handles instead.
	if err := attr.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		hpcValue(child.hPC), unsafe.Sizeof(child.hPC)); err != nil {
		closeOnErr()
		return nil, fmt.Errorf("pty: attach console: %w", err)
	}
	handles := []windows.Handle{errW}
	if err := attr.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
		unsafe.Pointer(&handles[0]), unsafe.Sizeof(handles[0])); err != nil {
		closeOnErr()
		return nil, fmt.Errorf("pty: handle list: %w", err)
	}

	cmdLine, err := commandLine(opts.Path, opts.Args)
	if err != nil {
		closeOnErr()
		return nil, err
	}
	envBlock, err := envBlock(opts.Env)
	if err != nil {
		closeOnErr()
		return nil, err
	}
	var cwd *uint16
	if opts.Dir != "" {
		cwd, err = windows.UTF16PtrFromString(opts.Dir)
		if err != nil {
			closeOnErr()
			return nil, fmt.Errorf("pty: cwd: %w", err)
		}
	}
	var si windows.StartupInfoEx
	si.StartupInfo.Cb = uint32(unsafe.Sizeof(si))
	si.StartupInfo.Flags = windows.STARTF_USESTDHANDLES
	// stdin/stdout stay NULL: the pseudoconsole supplies them. stderr
	// points at the dedicated marker pipe.
	si.StartupInfo.StdInput = 0
	si.StartupInfo.StdOutput = 0
	si.StartupInfo.StdErr = errW
	si.ProcThreadAttributeList = attr.List()
	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT |
		windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_SUSPENDED)
	if err := windows.CreateProcess(nil, &cmdLine[0], nil, nil, true,
		flags, envBlock, cwd, &si.StartupInfo, &pi); err != nil {
		closeOnErr()
		return nil, fmt.Errorf("pty: create process: %w", err)
	}
	// Pin raw-pointer syscall inputs across the call.
	runtime.KeepAlive(cmdLine)
	runtime.KeepAlive(envBlock)
	runtime.KeepAlive(attr)
	runtime.KeepAlive(&si)

	// Job assignment while suspended: no descendant can predate it.
	if job, err := windows.CreateJobObject(nil, nil); err == nil {
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
			},
		}
		_, serr := windows.SetInformationJobObject(job,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
		if serr == nil && windows.AssignProcessToJobObject(job, pi.Process) == nil {
			child.job, child.hasJob = job, true
		} else {
			_ = windows.CloseHandle(job)
		}
	}
	// The pseudoconsole dup'd its pipe ends into conhost: closing
	// ours lets readers observe EOF when the session ends. errW went to
	// the child, not the console, so the parent keeps only errR.
	_ = windows.CloseHandle(inR)
	_ = windows.CloseHandle(outW)
	_ = windows.CloseHandle(errW)
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		_ = windows.CloseHandle(pi.Process)
		_ = windows.CloseHandle(pi.Thread)
		closeOnErr()
		return nil, fmt.Errorf("pty: resume: %w", err)
	}
	_ = windows.CloseHandle(pi.Thread)

	child.proc = pi.Process
	child.pid = int(pi.ProcessId)
	child.inW = os.NewFile(uintptr(inW), "pty-in")
	child.outR = os.NewFile(uintptr(outR), "pty-out")
	child.errR = os.NewFile(uintptr(errR), "pty-err")
	return child, nil
}

// Wait blocks for process exit and returns its code.
func (c *Child) Wait() (int, error) {
	c.waitOnce.Do(func() {
		if c.proc == 0 {
			c.waitErr = fmt.Errorf("pty: no process")
			return
		}
		ev, err := windows.WaitForSingleObject(c.proc, windows.INFINITE)
		if err != nil || ev != windows.WAIT_OBJECT_0 {
			c.waitErr = fmt.Errorf("pty: wait: %w", err)
			return
		}
		var code uint32
		if err := windows.GetExitCodeProcess(c.proc, &code); err != nil {
			c.waitErr = fmt.Errorf("pty: exit code: %w", err)
			return
		}
		c.exitCode = int(code)
	})
	return c.exitCode, c.waitErr
}

// Kill terminates the whole tree (job) or the root process.
func (c *Child) Kill() error {
	if c.hasJob {
		return windows.TerminateJobObject(c.job, 1)
	}
	if c.proc != 0 {
		return windows.TerminateProcess(c.proc, 1)
	}
	return fmt.Errorf("pty: nothing to kill")
}

// stillActive is STATUS_PENDING for a live process object.
const stillActive = 259

// Alive reports whether the process object still runs.
func (c *Child) Alive() bool {
	if c.proc == 0 {
		return false
	}
	var code uint32
	if err := windows.GetExitCodeProcess(c.proc, &code); err != nil {
		return false
	}
	return code == stillActive
}

// Close releases the pseudoconsole, job, process, and pipes. Closing
// the job kills any stragglers via KILL_ON_JOB_CLOSE.
//
// Ordering matters: pending synchronous reads on another thread
// block CloseHandle on the same pipe, so readers are cancelled
// (CancelIoEx) and the console is closed first — both unblock drain
// readers — before any pipe handle closes.
func (c *Child) Close() error {
	if c.outR != nil {
		cancelIo(c.outR)
	}
	if c.errR != nil {
		cancelIo(c.errR)
	}
	if c.inW != nil {
		cancelIo(c.inW)
	}
	if c.hPC != 0 {
		windows.ClosePseudoConsole(c.hPC)
		c.hPC = 0
	}
	if c.hasJob {
		_ = windows.CloseHandle(c.job)
		c.hasJob = false
	}
	if c.proc != 0 {
		_ = windows.CloseHandle(c.proc)
		c.proc = 0
	}
	if c.inW != nil {
		_ = c.inW.Close()
		c.inW = nil
	}
	if c.outR != nil {
		_ = c.outR.Close()
		c.outR = nil
	}
	if c.errR != nil {
		_ = c.errR.Close()
		c.errR = nil
	}
	return nil
}

// cancelIo unblocks pending synchronous I/O issued from other
// threads so handle closes cannot deadlock behind them.
func cancelIo(f *os.File) {
	_ = windows.CancelIoEx(windows.Handle(f.Fd()), nil)
}

// commandLine builds a mutable, MSVCRT-quoted command line.
// CreateProcess may modify the buffer, so it must be owned here.
func commandLine(path string, args []string) ([]uint16, error) {
	if path == "" {
		return nil, fmt.Errorf("pty: empty command")
	}
	parts := append([]string{path}, args...)
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteByte(' ')
		}
		quoteArg(&b, p)
	}
	return syscall.StringToUTF16(b.String()), nil
}

// quoteArg applies CommandLineToArgvW quoting: 2n backslashes +
// literal quote for n backslashes before a quote, 1:1 elsewhere,
// doubled trailing backslashes before the closing quote.
func quoteArg(b *strings.Builder, s string) {
	if s == "" {
		b.WriteString(`""`)
		return
	}
	if !strings.ContainsAny(s, " \t\n\v\"") {
		b.WriteString(s)
		return
	}
	b.WriteByte('"')
	slashes := func(n int) {
		for i := 0; i < n; i++ {
			b.WriteByte('\\')
		}
	}
	n := 0
	for _, r := range s {
		switch r {
		case '\\':
			n++
		case '"':
			slashes(2 * n)
			n = 0
			b.WriteString(`\"`)
		default:
			slashes(n)
			n = 0
			b.WriteRune(r)
		}
	}
	slashes(2 * n)
	b.WriteByte('"')
}

// envBlock builds a NUL-separated, double-NUL-terminated UTF-16 block.
func envBlock(env []string) (*uint16, error) {
	if len(env) == 0 {
		return nil, fmt.Errorf("pty: empty environment")
	}
	var block []uint16
	for _, kv := range env {
		block = append(block, syscall.StringToUTF16(kv)...)
	}
	block = append(block, 0)
	return &block[0], nil
}
