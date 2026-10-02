//go:build windows

package spawn

import (
	"os"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processMemoryCounters mirrors PROCESS_MEMORY_COUNTERS_EX for the
// psapi GetProcessMemoryInfo call x/sys does not wrap. Layout matches
// the Windows ABI on 64-bit (two DWORDs then SIZE_T fields).
type processMemoryCounters struct {
	CB                      uint32
	PageFaultCount          uint32
	PeakWorkingSetSize      uintptr
	WorkingSetSize          uintptr
	QuotaPeakPagedPoolUsage uintptr
	QuotaPagedPoolUsage     uintptr
	QuotaPeakNonPagedPool   uintptr
	QuotaNonPagedPoolUsage  uintptr
	PagefileUsage           uintptr
	PeakPagefileUsage       uintptr
	PrivateUsage            uintptr
}

var (
	modPsapi                 = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessMemoryInfo = modPsapi.NewProc("GetProcessMemoryInfo")
)

// tracker retains a query handle (valid after process exit until
// closed) and a KILL_ON_JOB_CLOSE job for tree kills.
type tracker struct {
	query  windows.Handle
	job    windows.Handle
	hasJob bool
}

func configureCmd(_ *exec.Cmd) {}

// trackStart opens a query handle by PID while the process object is
// alive (os/exec holds its own handle until Wait) and assigns a job
// object for whole-tree kills. Best-effort: failures leave zero
// handles and the synchronous kill path.
func trackStart(cmd *exec.Cmd) *tracker {
	tr := &tracker{}
	if cmd.Process == nil {
		return tr
	}
	pid := uint32(cmd.Process.Pid)
	q, err := windows.OpenProcess(
		windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ|windows.PROCESS_TERMINATE,
		false, pid)
	if err == nil {
		tr.query = q
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return tr
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
		_ = windows.CloseHandle(job)
		return tr
	}
	// Open our own handle for assignment (mirrors internal/process:
	// explicit rights, and an exited PID fails instead of
	// mis-assigning).
	proc, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false, pid)
	if err != nil {
		_ = windows.CloseHandle(job)
		return tr
	}
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		// Already in a job (nested supervision) or just exited:
		// keep the query handle, drop the job.
		_ = windows.CloseHandle(proc)
		_ = windows.CloseHandle(job)
		return tr
	}
	_ = windows.CloseHandle(proc)
	tr.job = job
	tr.hasJob = true
	return tr
}

// killTree terminates the whole job; without a job it terminates the
// root process directly.
func killTree(cmd *exec.Cmd, tr *tracker) {
	if tr != nil && tr.hasJob {
		_ = windows.TerminateJobObject(tr.job, 1)
		return
	}
	if tr != nil && tr.query != 0 {
		_ = windows.TerminateProcess(tr.query, 1)
		return
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// finishTrack reads PeakWorkingSetSize through the retained handle
// (valid after exit) and releases handles. Closing the job also kills
// any stragglers via KILL_ON_JOB_CLOSE.
func finishTrack(_ *exec.Cmd, tr *tracker, _ *os.ProcessState, res *Result) {
	if tr == nil {
		return
	}
	defer func() {
		if tr.query != 0 {
			_ = windows.CloseHandle(tr.query)
			tr.query = 0
		}
		if tr.hasJob {
			_ = windows.CloseHandle(tr.job)
			tr.hasJob = false
		}
	}()
	if tr.query == 0 {
		return
	}
	var mc processMemoryCounters
	mc.CB = uint32(unsafe.Sizeof(mc))
	r1, _, _ := procGetProcessMemoryInfo.Call(
		uintptr(tr.query),
		uintptr(unsafe.Pointer(&mc)),
		uintptr(mc.CB),
	)
	if r1 == 0 {
		return
	}
	res.PeakRSSBytes = int64(mc.PeakWorkingSetSize)
	res.PeakSemantics = "windows:peak_working_set"
}
