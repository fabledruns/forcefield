//go:build windows

package collect

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processMemoryCounters mirrors PROCESS_MEMORY_COUNTERS, the argument
// to psapi GetProcessMemoryInfo. x/sys/windows wraps Job Object
// accounting but not this call, so it is declared here and resolved
// lazily through psapi.dll (already present in every Windows system;
// no extra dependency).
//
// Layout: two DWORDs, then eight SIZE_T fields.
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

// ReadProcessMemoryInfo queries one process through psapi.
//
// It returns PeakSemantics "windows:peak_working_set" and
// RSSSemantics "windows:working_set". Windows working set is not the
// same quantity as Unix RSS (it includes shared and mapped pages and
// excludes some private pages), which is exactly why every reading
// carries its semantics tag: values are comparable within one OS
// only.
func ReadProcessMemoryInfo(pid int) (uint64, uint64, error) {
	const (
		queryInfo = windows.PROCESS_QUERY_INFORMATION
		queryVM   = windows.PROCESS_VM_READ
	)
	h, err := windows.OpenProcess(queryInfo|queryVM, false, uint32(pid))
	if err != nil {
		return 0, 0, fmt.Errorf("open process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()

	var mc processMemoryCounters
	mc.CB = uint32(unsafe.Sizeof(mc))
	r1, _, _ := procGetProcessMemoryInfo.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&mc)),
		uintptr(mc.CB),
	)
	if r1 == 0 {
		return 0, 0, fmt.Errorf("GetProcessMemoryInfo(%d) failed", pid)
	}
	return uint64(mc.WorkingSetSize), uint64(mc.PeakWorkingSetSize), nil
}

func readProcess(pid int) (Reading, error) {
	ws, peak, err := ReadProcessMemoryInfo(pid)
	if err != nil {
		// No semantics tag: nothing was measured, so there is no
		// quantity to name. A caller must not read the empty string as
		// "unknown flavour of RSS".
		r := Reading{PID: pid, RSSBytes: -1, PeakBytes: -1}
		switch {
		case isAccessDenied(err):
			r.Reason = ReasonPermitted
			return r, unavailable(ReasonPermitted, err)
		case isInvalidHandle(err):
			// The process exited and was reaped: its PID no longer
			// names a process object.
			r.Reason = ReasonExited
			return r, unavailable(ReasonExited, err)
		default:
			r.Reason = ReasonQueryFailed
			return r, unavailable(ReasonQueryFailed, err)
		}
	}
	r := Reading{PID: pid, RSSBytes: -1, PeakBytes: -1,
		RSSSemantics: "windows:working_set"}
	if ws == 0 && peak == 0 {
		// A live process never has an empty working set and no peak.
		r.Reason = ReasonMalformed
		return r, unavailable(ReasonMalformed, nil)
	}
	r.RSSBytes = int64(ws)
	r.PeakBytes = int64(peak)
	r.PeakSemantics = "windows:peak_working_set"
	return r, nil
}

func isAccessDenied(err error) bool {
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return true
	}
	return strings.Contains(err.Error(), "Access is denied")
}

func isInvalidHandle(err error) bool {
	if errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "invalid handle") ||
		strings.Contains(s, "not found") ||
		strings.Contains(s, "The system cannot find")
}

// descendants walks the live process list and returns every process
// whose ancestry reaches root, so short-lived grandchildren are
// included as long as they are alive at enumeration time.
func descendants(root int) ([]int, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, unavailable(ReasonQueryFailed, err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()

	parent := map[int]int{}
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if err := windows.Process32First(snap, &e); err != nil {
		return nil, unavailable(ReasonQueryFailed, err)
	}
	for {
		parent[int(e.ProcessID)] = int(e.ParentProcessID)
		if err := windows.Process32Next(snap, &e); err != nil {
			break
		}
	}

	// Collect every pid whose chain reaches root, walking breadth-first
	// from root's direct children.
	children := map[int][]int{}
	for pid, ppid := range parent {
		children[ppid] = append(children[ppid], pid)
	}
	var out []int
	seen := map[int]bool{root: true}
	queue := []int{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, kid := range children[cur] {
			if seen[kid] {
				continue // PID reuse inside one snapshot cannot happen,
				// but a cycle must not loop forever
			}
			seen[kid] = true
			out = append(out, kid)
			queue = append(queue, kid)
		}
	}
	return out, nil
}

// ProcessPath returns the executable path of pid, for tests that
// confirm the psapi path against a known child.
func ProcessPath(pid int) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var buf [windows.MAX_LONG_PATH]uint16
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:size]), nil
}
