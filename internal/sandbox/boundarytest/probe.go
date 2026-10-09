// Capability probing for sandbox boundary tests.
//
// Probe collects raw, read-only machine facts (OS, kernel release, LSM
// list, user-namespace sysctls) so CI logs show why a boundary test ran
// or skipped. It never claims Forcefield enforces anything: every
// backend field reports the truth that no OS-enforced shell boundary
// exists yet outside Windows/WSL. When P0-D lands a Linux backend, its
// Probe will consult these facts and this file will grow a seam for the
// new mechanism; until then the honest answer is "unavailable".
package boundarytest

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

// Capabilities are read-only facts about the test machine. Zero values
// mean "unknown", never "available": callers must treat missing files
// (non-Linux kernels, containers, locked-down runners) as unknown, not
// as evidence for or against a boundary.
type Capabilities struct {
	// OS and Arch mirror runtime.GOOS/GOARCH.
	OS   string
	Arch string
	// Kernel is the kernel release (Linux /proc/sys/kernel/osrelease,
	// best effort elsewhere) or "unknown".
	Kernel string
	// LSMs lists the active Linux Security Modules from
	// /sys/kernel/security/lsm, or "" when unreadable/off-platform.
	LSMs string
	// UserNSClone reports /proc/sys/kernel/unprivileged_userns_clone
	// ("1"/"0"/"unknown"). Only meaningful on Linux.
	UserNSClone string
	// MaxUserNS reports /proc/sys/user/max_user_namespaces or "unknown".
	MaxUserNS string
	// WSL reports whether the machine looks like WSL (kernel release
	// contains "microsoft" or WSL_DISTRO_NAME/WSLENV is set).
	WSL bool
	// BoundaryEnforced is always false until an OS-enforced shell
	// backend lands. It exists so future probes extend this struct
	// instead of inventing a second source of truth.
	BoundaryEnforced bool
	// BoundaryDetail explains the above in one line for CI logs.
	BoundaryDetail string
}

// Probe reads the machine facts described above. It performs no
// privileged operations, spawns no processes, and never fails: every
// unreadable source becomes "unknown" with the boundary honestly
// reported as unenforced.
func Probe() Capabilities {
	c := Capabilities{
		OS:               runtime.GOOS,
		Arch:             runtime.GOARCH,
		Kernel:           kernelRelease(),
		LSMs:             readTrim("/sys/kernel/security/lsm"),
		UserNSClone:      readTrim("/proc/sys/kernel/unprivileged_userns_clone"),
		MaxUserNS:        readTrim("/proc/sys/user/max_user_namespaces"),
		BoundaryEnforced: false,
		BoundaryDetail:   "no OS-enforced shell boundary exists on this platform outside Windows/WSL; native runs on the host",
	}
	if c.Kernel == "" {
		c.Kernel = "unknown"
	}
	if c.LSMs == "" {
		c.LSMs = "unknown"
	}
	if c.UserNSClone == "" {
		c.UserNSClone = "unknown"
	}
	if c.MaxUserNS == "" {
		c.MaxUserNS = "unknown"
	}
	c.WSL = detectWSL(c.Kernel)
	return c
}

// Report renders one greppable capability table for CI logs. The final
// line states the enforced boundary (today: none) so a log can never be
// mistaken for evidence of isolation.
func (c Capabilities) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "os=%s arch=%s kernel=%s\n", c.OS, c.Arch, c.Kernel)
	fmt.Fprintf(&b, "lsm=%s\n", c.LSMs)
	fmt.Fprintf(&b, "unprivileged_userns_clone=%s max_user_namespaces=%s\n", c.UserNSClone, c.MaxUserNS)
	fmt.Fprintf(&b, "wsl=%v\n", c.WSL)
	fmt.Fprintf(&b, "boundary_enforced=%v (%s)\n", c.BoundaryEnforced, c.BoundaryDetail)
	fmt.Fprintf(&b, "ff_require_boundary=%q\n", os.Getenv("FF_REQUIRE_BOUNDARY"))
	return b.String()
}

func kernelRelease() string {
	if v := readTrim("/proc/sys/kernel/osrelease"); v != "" {
		return v
	}
	// runtime version is not the kernel, but it keeps the field
	// non-empty off Linux without pretending to be a kernel release.
	if runtime.GOOS != "linux" {
		return runtime.GOOS + "/unknown"
	}
	return ""
}

func detectWSL(kernel string) bool {
	if strings.Contains(strings.ToLower(kernel), "microsoft") {
		return true
	}
	if _, ok := os.LookupEnv("WSL_DISTRO_NAME"); ok {
		return true
	}
	if _, ok := os.LookupEnv("WSLENV"); ok {
		return true
	}
	return false
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
