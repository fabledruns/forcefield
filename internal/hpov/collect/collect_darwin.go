//go:build darwin

package collect

import (
	"golang.org/x/sys/unix"
)

// readProcess reads current resident size from the kernel process
// list.
//
// macOS exposes no per-process peak high-water mark for a live
// process: VmHWM has no equivalent and task_info carries only the
// current figure. The peak is therefore reported unavailable here
// rather than approximated from a single read. Peak RSS for a macOS
// child comes from rusage.Maxrss (in BYTES, unlike Linux's KiB) via
// internal/hpov/spawn, which requires having waited on the process.
func readProcess(pid int) (Reading, error) {
	r := Reading{PID: pid, RSSBytes: -1, PeakBytes: -1,
		RSSSemantics: "darwin:resident_size"}
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		r.Reason = ReasonQueryFailed
		return r, unavailable(ReasonQueryFailed, err)
	}
	pageSize := int64(unix.Getpagesize())
	for i := range procs {
		if procs[i].Proc.P_pid != int32(pid) {
			continue
		}
		rss := int64(procs[i].Eproc.Xrssize) * pageSize
		if rss <= 0 {
			// A live process with no resident pages is not plausible;
			// treat it as unreadable rather than as 0 bytes.
			r.Reason = ReasonMalformed
			return r, unavailable(ReasonMalformed, nil)
		}
		r.RSSBytes = rss
		return r, nil
	}
	r.Reason = ReasonExited
	return r, unavailable(ReasonExited, nil)
}

// descendants walks kern.proc.all matching ppid against root.
func descendants(root int) ([]int, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, unavailable(ReasonQueryFailed, err)
	}
	parent := map[int]int{}
	for i := range procs {
		parent[int(procs[i].Proc.P_pid)] = int(procs[i].Eproc.Ppid)
	}
	return walkTree(parent, root), nil
}

func walkTree(parent map[int]int, root int) []int {
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
				continue
			}
			seen[kid] = true
			out = append(out, kid)
			queue = append(queue, kid)
		}
	}
	return out
}
