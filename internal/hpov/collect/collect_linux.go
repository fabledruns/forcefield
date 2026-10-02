//go:build linux

package collect

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// readProcess parses /proc/<pid>/status.
//
// Two distinct quantities are read and never mixed: VmRSS is current
// resident memory, VmHWM is the kernel-tracked lifetime peak. VmHWM
// exists precisely for this purpose and is exact for the process
// lifetime, which is why the headless metric prefers it over sampling.
// VmSize (virtual) and VmPeak (virtual peak) are deliberately ignored:
// reporting them as resident memory would be wrong.
func readProcess(pid int) (Reading, error) {
	r := Reading{PID: pid, RSSBytes: -1, PeakBytes: -1,
		RSSSemantics: "linux:vm_rss"}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		if os.IsNotExist(err) {
			r.Reason = ReasonExited
			return r, unavailable(ReasonExited, err)
		}
		if os.IsPermission(err) {
			r.Reason = ReasonPermitted
			return r, unavailable(ReasonPermitted, err)
		}
		r.Reason = ReasonQueryFailed
		return r, unavailable(ReasonQueryFailed, err)
	}
	rss, hwm, ok := parseStatus(string(raw))
	if !ok {
		r.Reason = ReasonMalformed
		return r, unavailable(ReasonMalformed, nil)
	}
	if rss < 0 {
		r.RSSBytes = -1
		return r, unavailable(ReasonExited, nil)
	}
	r.RSSBytes = rss
	if hwm >= 0 {
		r.PeakBytes = hwm
		r.PeakSemantics = "linux:vm_hwm"
	}
	return r, nil
}

// parseStatus extracts VmRSS and VmHWM from /proc/<pid>/status text.
// Both are reported in kB by the kernel. ok=false when neither field
// is present or a value does not parse, which is the malformed-data
// path; a missing VmHWM alone leaves the peak unavailable without
// failing the current reading.
func parseStatus(status string) (rss, hwm int64, ok bool) {
	for _, line := range strings.Split(status, "\n") {
		field, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		kb, err := parseKB(rest)
		if err != nil {
			continue
		}
		switch field {
		case "VmRSS":
			rss = kb
			ok = true
		case "VmHWM":
			hwm = kb
		}
	}
	return rss, hwm, ok
}

// parseKB reads a "<n> kB" field, rejecting anything else.
func parseKB(rest string) (int64, error) {
	fields := strings.Fields(rest)
	if len(fields) != 2 || fields[1] != "kB" {
		return 0, fmt.Errorf("unexpected /proc value %q", strings.TrimSpace(rest))
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", fields[0], err)
	}
	if n < 0 {
		return 0, fmt.Errorf("negative value %d", n)
	}
	return n * 1024, nil
}

// descendants builds the live process tree from /proc/<pid>/stat
// ppid fields, so a descendant that spawns and exits between samples is
// missed (documented sampler bias) but one alive at enumeration time is
// always included.
func descendants(root int) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, unavailable(ReasonQueryFailed, err)
	}
	parent := map[int]int{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a pid directory
		}
		ppid, err := readPPID(pid)
		if err != nil {
			continue // exited between readdir and read
		}
		parent[pid] = ppid
	}
	return walkTree(parent, root), nil
}

// readPPID reads the ppid field (field 4) from /proc/<pid>/stat. The
// comm field can contain spaces and parentheses, so parsing starts
// after the final ')'.
func readPPID(pid int) (int, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return 0, fmt.Errorf("malformed stat for pid %d", pid)
	}
	fields := strings.Fields(s[i+2:])
	if len(fields) < 2 {
		return 0, fmt.Errorf("truncated stat for pid %d", pid)
	}
	return strconv.Atoi(fields[1])
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
