//go:build !windows

package spawn

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

// tracker is empty on Unix: peak RSS comes from wait4 rusage and the
// tree kill uses the process group.
type tracker struct{}

// configureCmd puts the child in its own process group so killTree
// reaches the whole tree.
func configureCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func trackStart(_ *exec.Cmd) *tracker { return &tracker{} }

// killTree signals the whole process group.
func killTree(cmd *exec.Cmd, _ *tracker) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// finishTrack extracts the kernel-tracked peak from rusage. Linux
// reports Maxrss in KiB, Darwin in bytes.
func finishTrack(_ *exec.Cmd, _ *tracker, state *os.ProcessState, res *Result) {
	if state == nil {
		return
	}
	ru, ok := state.SysUsage().(*syscall.Rusage)
	if !ok {
		return
	}
	switch runtime.GOOS {
	case "linux":
		res.PeakRSSBytes = int64(ru.Maxrss) * 1024
		res.PeakSemantics = "linux:maxrss"
	case "darwin":
		res.PeakRSSBytes = int64(ru.Maxrss)
		res.PeakSemantics = "darwin:maxrss"
	default:
		res.PeakRSSBytes = int64(ru.Maxrss)
		res.PeakSemantics = runtime.GOOS + ":maxrss"
	}
}
