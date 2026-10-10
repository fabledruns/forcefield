// Capability probing for sandbox boundary tests.
//
// Probe collects raw, read-only machine facts (OS, kernel release, LSM
// list, user-namespace sysctls, Landlock ABI status) so CI logs show
// why a boundary test ran or skipped. It never claims Forcefield
// enforces anything: every backend field reports the truth that no
// OS-enforced shell boundary exists yet outside Windows/WSL, and the
// Landlock fields report only what the version query observed, never
// that restrictions are applied.
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
	// Landlock is the stable machine-readable Landlock capability
	// status: "supported", "unsupported", "disabled", or "error".
	// It reports only what the version query observed. An ABI being
	// available does NOT mean a filesystem boundary is enforced:
	// BoundaryEnforced below stays false until a backend exists that
	// creates rulesets and proves denials.
	Landlock string
	// LandlockABI is the detected Landlock ABI version (>= 1), or -1
	// when unavailable. CI greps this alongside Landlock.
	LandlockABI int
	// LandlockDetail carries human context for the status (kernel
	// errno text for failures, empty when supported). Informational
	// only: CI must match on Landlock, never on this free-form text.
	LandlockDetail string
	// BoundaryEnforced is false: no OS-enforced shell boundary is
	// established by probing (Windows/WSL has its backends; Linux has
	// opt-in isolated mode, which enforces only when configured and
	// probed OK). It exists so future probes extend this struct
	// instead of inventing a second source of truth.
	BoundaryEnforced bool
	// BoundaryDetail explains the above in one line for CI logs.
	BoundaryDetail string
}

// Probe reads the machine facts described above. It performs no
// privileged operations, spawns no processes, applies no restrictions,
// and never fails: every unreadable source becomes "unknown" (or the
// Landlock unavailable statuses) with the boundary honestly reported
// as unenforced.
func Probe() Capabilities {
	landlockABI, landlockStatus, landlockDetail := queryLandlock()
	c := Capabilities{
		OS:               runtime.GOOS,
		Arch:             runtime.GOARCH,
		Kernel:           kernelRelease(),
		LSMs:             readTrim("/sys/kernel/security/lsm"),
		UserNSClone:      readTrim("/proc/sys/kernel/unprivileged_userns_clone"),
		MaxUserNS:        readTrim("/proc/sys/user/max_user_namespaces"),
		Landlock:         landlockStatus,
		LandlockABI:      landlockABI,
		LandlockDetail:   landlockDetail,
		BoundaryEnforced: false,
		BoundaryDetail:   "probe establishes no boundary; Windows/WSL backends and opt-in Linux isolated mode enforce only when configured",
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
	fmt.Fprintf(&b, "landlock=%s\n", c.Landlock)
	fmt.Fprintf(&b, "landlock_abi=%d\n", c.LandlockABI)
	if strings.TrimSpace(c.LandlockDetail) != "" {
		fmt.Fprintf(&b, "landlock_detail=%s\n", c.LandlockDetail)
	}
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
