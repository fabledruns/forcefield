//go:build windows

package process

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Configure is a no-op on Windows: POSIX process groups don't exist
// here. Tree membership is established after Start via Track instead.
func Configure(cmd *exec.Cmd) {}

// Track assigns cmd's process to a Job Object limited with
// KILL_ON_JOB_CLOSE and returns its release func. Closing the job kills
// the whole Windows-side tree rooted at cmd — including processes
// spawned before Track ran or after Kill ran — even if Forcefield
// itself is killed outright (the OS closes our handle for us).
//
// Best-effort by design: assignment fails when the process already
// belongs to a job (e.g. nested under a supervising Forcefield's job —
// which already covers it through inheritance) or has just exited. The
// release is then a no-op and the synchronous Kill path remains. Track
// must be called after Start and released after Wait.
func Track(cmd *exec.Cmd) (release ReleaseFunc) {
	noop := func() {}
	if cmd == nil || cmd.Process == nil {
		return noop
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return noop
	}
	// From here on the handle must be closed exactly once.
	var once sync.Once
	release = func() {
		once.Do(func() { _ = windows.CloseHandle(job) })
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		release()
		return noop
	}
	// Open our own handle rather than reaching into os internals: the
	// rights AssignProcessToJobObject needs are explicit this way, and a
	// PID that already exited fails here instead of mis-assigning.
	proc, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		release()
		return noop
	}
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		_ = windows.CloseHandle(proc)
		release()
		return noop
	}
	_ = windows.CloseHandle(proc)
	return release
}

// Kill terminates the whole Windows-side tree rooted at cmd's process:
// taskkill enumerates and force-kills descendants first (/T /F), then
// the direct child is killed as fallback. A Job Object from Track (if
// any) finishes stragglers when its handle is released. Diagnostics
// from the kill itself are swallowed — callers report the cancellation
// or timeout, not the mechanics — except when the tree is provably
// still alive afterwards.
func Kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// taskkill /T terminates descendants before the root, which is what
	// prevents grandchildren from holding stdio pipes open and delaying
	// timeout/cancellation completion.
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()

	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		// taskkill may have already reaped the process: TerminateProcess
		// on a corpse reports access-denied instead of done. Confirm
		// with a short bounded aliveness check rather than trusting the
		// error (which would cry wolf) or swallowing it (which would
		// hide a genuinely surviving tree).
		if gone := waitGone(cmd.Process.Pid); gone {
			return nil
		}
		return err
	}
	return nil
}

// killConfirmTimeout bounds the death confirmation after a failed
// fallback kill. Corpses resolve on the first poll; a truly surviving
// tree fails fast here instead of hanging cancellation.
const killConfirmTimeout = 2 * time.Second

// stillActive is the STILL_ACTIVE pseudo exit code GetExitCodeProcess
// reports for a live process (x/sys/windows does not export the
// constant; the value comes from the Windows SDK).
const stillActive = 259

// waitGone polls whether pid still names a live process. An
// unresolvable PID means nothing is left to kill. (Like taskkill /PID
// itself, this races PID reuse in principle; in practice the window is
// the two seconds after our own kill of a known PID.)
func waitGone(pid int) bool {
	deadline := time.Now().Add(killConfirmTimeout)
	for {
		stillAlive := true
		handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if err != nil {
			stillAlive = false
		} else {
			var code uint32
			if qerr := windows.GetExitCodeProcess(handle, &code); qerr == nil && code != stillActive {
				stillAlive = false
			}
			_ = windows.CloseHandle(handle)
		}
		if !stillAlive {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Terminate requests termination on Windows. There is no graceful
// console primitive that preserves stdio behavior (a shared console
// Ctrl event would hit Forcefield too), so Terminate is Kill: the
// Run lifecycle still honors the grace wait, which simply observes an
// already-dead tree.
func Terminate(cmd *exec.Cmd) error {
	return Kill(cmd)
}
