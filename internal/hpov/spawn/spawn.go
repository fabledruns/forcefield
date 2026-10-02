// Package spawn launches measured processes with exact spawn→exit
// wall timing, kernel CPU times, and platform peak-memory tracking.
//
// Time boundaries: T_spawn is taken immediately before Start (process
// creation is included: it is user-perceived latency); T_exit is
// taken immediately after Wait returns. Clocks use Go's time.Now
// monotonic reading; resolution is calibrated by envinfo.
package spawn

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// MaxCapture bounds captured stdout/stderr (1 MiB each).
const MaxCapture = 1 << 20

// DefaultTimeout bounds one iteration when the suite sets none.
const DefaultTimeout = 60 * time.Second

// Options controls one spawn.
type Options struct {
	Path string
	Args []string
	Env  []string // complete environment block; nil inherits (avoid: use fixture.ScrubEnv)
	Dir  string
	// Stdio: nil means the null device (wall pass). Callers needing
	// streaming (marker pass) pass their own *os.File pipes.
	Stdin  *os.File
	Stdout *os.File
	Stderr *os.File
	// CaptureStdout/Stderr collect small outputs (version/help
	// validity, probes) instead of discarding them.
	CaptureStdout bool
	CaptureStderr bool
	Timeout       time.Duration // 0 = DefaultTimeout
}

// Result is one spawn's measurement.
type Result struct {
	WallMS float64
	UserMS float64 // kernel user CPU time
	SysMS  float64 // kernel system CPU time
	// ExitCode from ProcessState. -1 when killed by signal/exception.
	ExitCode int
	Stdout   string
	Stderr   string
	TimedOut bool
	// PeakRSSBytes is the kernel-tracked root-process peak, -1 when
	// unavailable. Absolute values are NOT comparable across OSes
	// (working set vs RSS); see PeakSemantics.
	PeakRSSBytes  int64
	PeakSemantics string
	State         *os.ProcessState
}

// Run spawns, waits, and measures. On timeout (or ctx cancel) the
// whole process tree is killed. Wall timing always covers spawn→exit.
func Run(ctx context.Context, opts Options) (Result, error) {
	var res Result
	res.PeakRSSBytes = -1
	res.PeakSemantics = "unavailable"
	if opts.Path == "" {
		return res, fmt.Errorf("spawn: empty path")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return res, fmt.Errorf("spawn: open null device: %w", err)
	}
	defer func() { _ = null.Close() }()

	stdin := opts.Stdin
	if stdin == nil {
		stdin = null
	}
	stdout := opts.Stdout
	var outBuf *bytes.Buffer
	var outR, outW *os.File
	if opts.CaptureStdout && stdout == nil {
		outBuf = &bytes.Buffer{}
		r, w, err := os.Pipe()
		if err != nil {
			return res, fmt.Errorf("spawn: stdout pipe: %w", err)
		}
		defer func() { _ = r.Close() }()
		outR, outW = r, w
		stdout = w // child inherits the write end
	}
	if stdout == nil {
		stdout = null
	}
	stderr := opts.Stderr
	var errBuf *bytes.Buffer
	var errR, errW *os.File
	if opts.CaptureStderr && stderr == nil {
		errBuf = &bytes.Buffer{}
		r, w, err := os.Pipe()
		if err != nil {
			return res, fmt.Errorf("spawn: stderr pipe: %w", err)
		}
		defer func() { _ = r.Close() }()
		errR, errW = r, w
		stderr = w // child inherits the write end
	}
	if stderr == nil {
		stderr = null
	}

	cmd := exec.Command(opts.Path, opts.Args...)
	cmd.Env = opts.Env
	cmd.Dir = opts.Dir
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second // reap quietly if the tree kill races Wait
	configureCmd(cmd)

	var wg sync.WaitGroup
	if outW != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = io.CopyN(outBuf, outR, MaxCapture+1)
		}()
	}
	if errW != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = io.CopyN(errBuf, errR, MaxCapture+1)
		}()
	}

	tSpawn := time.Now()
	if err := cmd.Start(); err != nil {
		if outW != nil {
			_ = outW.Close()
		}
		if errW != nil {
			_ = errW.Close()
		}
		wg.Wait()
		return res, fmt.Errorf("spawn: start %s: %w", opts.Path, err)
	}
	// The child inherited its copies; the parent closes the write
	// ends so the copy goroutines see EOF at child exit.
	if outW != nil {
		_ = outW.Close()
	}
	if errW != nil {
		_ = errW.Close()
	}
	tr := trackStart(cmd)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case err = <-waitCh:
	case <-ctx.Done():
		killTree(cmd, tr)
		err = <-waitCh
		if ctx.Err() == context.DeadlineExceeded {
			res.TimedOut = true
		} else {
			finishTrack(cmd, tr, cmd.ProcessState, &res)
			wg.Wait()
			fillCaptured(&res, outBuf, errBuf)
			return res, fmt.Errorf("spawn: aborted: %w", ctx.Err())
		}
	}
	tExit := time.Now()

	finishTrack(cmd, tr, cmd.ProcessState, &res)
	wg.Wait()
	fillCaptured(&res, outBuf, errBuf)

	res.WallMS = float64(tExit.Sub(tSpawn).Nanoseconds()) / 1e6
	if ps := cmd.ProcessState; ps != nil {
		res.State = ps
		res.UserMS = float64(ps.UserTime().Nanoseconds()) / 1e6
		res.SysMS = float64(ps.SystemTime().Nanoseconds()) / 1e6
		res.ExitCode = ps.ExitCode()
	}
	_ = err // exit codes are data, not errors; callers check ExitCode
	return res, nil
}

func fillCaptured(res *Result, outBuf, errBuf *bytes.Buffer) {
	if outBuf != nil {
		s := outBuf.String()
		if len(s) > MaxCapture {
			s = s[:MaxCapture]
		}
		res.Stdout = s
	}
	if errBuf != nil {
		s := errBuf.String()
		if len(s) > MaxCapture {
			s = s[:MaxCapture]
		}
		res.Stderr = s
	}
}
