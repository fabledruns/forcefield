//go:build windows

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Configure starts cmd suspended so Track can assign the Job Object
// before a single instruction of child code runs. Without this, a
// fast-spawning grandchild born between Start and Track would miss job
// membership entirely (AssignProcessToJobObject covers only the assigned
// process; pre-existing children are not pulled in). Track resumes the
// child after assignment, so no descendant can predate membership.
// Every Configure must be paired with Track: an untracked child stays
// suspended. All current callers pair them (shell, jobs, git,
// search_code, MCP host, Run).
func Configure(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
}

// Track assigns cmd's process to a Job Object limited with
// KILL_ON_JOB_CLOSE and returns its release func. Closing the job kills
// the whole Windows-side tree rooted at cmd — including processes
// spawned after Track ran — even if Forcefield itself is killed
// outright (the OS closes our handle for us).
//
// The child was started suspended by Configure and is resumed here only
// after assignment, so no descendant can be born outside the job: the
// Start→Track race is closed by construction rather than by winning it.
// A resume failure kills the child instead of leaving a suspended
// zombie, and the release stays a no-op path.
//
// Best-effort by design: assignment fails when the process already
// belongs to a job (e.g. nested under a supervising Forcefield's job —
// which already covers it through inheritance) or has just exited. Those
// paths still resume the child and fall back to the synchronous Kill
// path. Track must be called after Start and released after Wait.
func Track(cmd *exec.Cmd) (release ReleaseFunc) {
	noop := func() {}
	if cmd == nil || cmd.Process == nil {
		return noop
	}
	pid := uint32(cmd.Process.Pid)
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		// No job: resume so the child can run; synchronous Kill remains.
		_ = resumeProcess(pid)
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
		_ = resumeProcess(pid)
		release()
		return noop
	}
	// Open our own handle rather than reaching into os internals: the
	// rights AssignProcessToJobObject needs are explicit this way, and a
	// PID that already exited fails here instead of mis-assigning.
	proc, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		pid,
	)
	if err != nil {
		_ = resumeProcess(pid)
		release()
		return noop
	}
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		// Already in a job (nested supervision) or otherwise
		// unassignable: resume regardless so no suspended zombie
		// remains, and rely on the synchronous Kill path.
		_ = windows.CloseHandle(proc)
		_ = resumeProcess(pid)
		release()
		return noop
	}
	_ = windows.CloseHandle(proc)
	if err := resumeProcess(pid); err != nil {
		// Cannot resume what we just caged: kill it rather than leak a
		// suspended process, then release the (now empty) job.
		_ = Kill(cmd)
		release()
		return noop
	}
	return release
}

// resumeWaitTimeout bounds thread enumeration for resuming a suspended
// child. The primary thread exists from CreateProcess, so this resolves
// on the first snapshot in practice; the bound keeps a wedged lookup
// from stalling process startup.
const resumeWaitTimeout = 2 * time.Second

// resumeProcess resumes every thread of pid (in practice exactly the
// suspended primary thread: no child code has run yet, so no other
// thread can exist). It is the second half of the suspended-start
// protocol opened by Configure.
func resumeProcess(pid uint32) error {
	deadline := time.Now().Add(resumeWaitTimeout)
	for {
		tids, err := processThreadIDs(pid)
		if err == nil && len(tids) > 0 {
			for _, tid := range tids {
				if rerr := resumeThread(tid); rerr != nil {
					return rerr
				}
			}
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return err
			}
			return fmt.Errorf("resume process %d: no threads found", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// processThreadIDs lists thread IDs owned by pid via a system snapshot.
func processThreadIDs(pid uint32) ([]uint32, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Thread32First(snap, &entry); err != nil {
		return nil, err
	}
	var out []uint32
	for {
		if entry.OwnerProcessID == pid {
			out = append(out, entry.ThreadID)
		}
		if err := windows.Thread32Next(snap, &entry); err != nil {
			break
		}
	}
	return out, nil
}

// resumeThread decrements one thread's suspend count once (created
// suspended, so exactly one resume releases it).
func resumeThread(tid uint32) error {
	h, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, tid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	_, err = windows.ResumeThread(h)
	return err
}

// taskkillPath is a seam over launcher resolution so tests can simulate
// absence without touching the real system directory.
var taskkillPath = defaultTaskkillPath

// defaultTaskkillPath prefers the well-known System32 location so a
// tampered PATH cannot substitute a different executable (verified
// Phase 0: bare "taskkill" resolved an attacker first when a hostile
// directory led PATH). Falls back to PATH lookup when System32 is
// unavailable, mirroring the wsl.exe launcher resolution.
func defaultTaskkillPath() string {
	if root := os.Getenv("SystemRoot"); root != "" {
		if p := filepath.Join(root, "System32", "taskkill.exe"); statOk(p) {
			return p
		}
	}
	return "taskkill"
}

func statOk(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// taskkillTimeout bounds the helper invocation inside Kill so a wedged
// taskkill cannot hang cancellation, timeout, or shutdown paths.
const taskkillTimeout = 10 * time.Second

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
	func() {
		ctx, cancel := context.WithTimeout(context.Background(), taskkillTimeout)
		defer cancel()
		_ = exec.CommandContext(ctx, taskkillPath(), "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}()

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
