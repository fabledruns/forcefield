//go:build linux

package envinfo

import (
	"os"
	"strings"
	"syscall"
	"time"
)

func osVersion() string {
	raw, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return ""
}

func cpuModel() string {
	raw, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "model name"); ok {
			if i := strings.Index(v, ":"); i >= 0 {
				return strings.TrimSpace(v[i+1:])
			}
		}
	}
	return ""
}

func physicalCores() *int { return nil }

func memoryTotal() *int64 {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == "MemTotal:" && f[2] == "kB" {
			var kb int64
			for _, c := range f[1] {
				if c < '0' || c > '9' {
					return nil
				}
				kb = kb*10 + int64(c-'0')
			}
			v := kb * 1024
			return &v
		}
	}
	return nil
}

func powerSource() string {
	// No battery state without /sys power entries (absent on VMs):
	// AC online files decide, otherwise unknown.
	matches, _ := os.ReadDir("/sys/class/power_supply")
	sawAC := false
	for _, m := range matches {
		raw, err := os.ReadFile("/sys/class/power_supply/" + m.Name() + "/online")
		if err != nil {
			continue
		}
		sawAC = true
		if strings.TrimSpace(string(raw)) == "1" {
			return "ac"
		}
	}
	if sawAC {
		return "battery"
	}
	return "unknown"
}

func powerPlan() string {
	raw, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(raw))
}

var fsNames = map[int64]string{
	0xEF53:     "ext4",
	0x01021994: "tmpfs",
	0x794C7630: "overlayfs",
	0x9123683E: "btrfs",
	0x58465342: "xfs",
	0x5346544E: "ntfs",
	0x517B:     "smb",
	0x6969:     "nfs",
	0x9FA0:     "proc",
	0x62656572: "sysfs",
	0x64626720: "debugfs",
	0x01021997: "v9fs",
}

func fsKind(workroot string) string {
	var st syscall.Statfs_t
	if err := syscall.Statfs(workroot, &st); err != nil {
		return "unknown"
	}
	if n, ok := fsNames[int64(st.Type)]; ok {
		return n
	}
	return "unknown"
}

func isWSL() bool {
	raw, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(raw)), "microsoft")
}

func isVM() bool {
	raw, err := os.ReadFile("/sys/class/dmi/id/product_name")
	if err != nil {
		return false
	}
	s := strings.ToLower(string(raw))
	for _, k := range []string{"kvm", "vmware", "virtualbox", "hyper-v", "qemu", "xen", "parallels"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// idleCPU samples whole-system busy % from /proc/stat over d.
func idleCPU(d time.Duration) *float64 {
	sample := func() (idle, total uint64, ok bool) {
		raw, err := os.ReadFile("/proc/stat")
		if err != nil {
			return 0, 0, false
		}
		line := strings.SplitN(string(raw), "\n", 2)[0]
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "cpu" {
			return 0, 0, false
		}
		nums := make([]uint64, 0, len(f)-1)
		for _, s := range f[1:] {
			var v uint64
			for _, c := range s {
				if c < '0' || c > '9' {
					return 0, 0, false
				}
				v = v*10 + uint64(c-'0')
			}
			nums = append(nums, v)
		}
		// user nice system idle iowait irq softirq steal ...
		idle = nums[3]
		if len(nums) > 4 {
			idle += nums[4]
		}
		for _, v := range nums {
			total += v
		}
		return idle, total, true
	}
	i0, t0, ok := sample()
	if !ok {
		return nil
	}
	time.Sleep(d)
	i1, t1, ok := sample()
	if !ok || t1 <= t0 {
		return nil
	}
	v := (1 - float64(i1-i0)/float64(t1-t0)) * 100
	return &v
}
