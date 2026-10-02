// Package envinfo collects host, environment, and quality signals
// for HPOV results: host fingerprint, clock calibration, idle load,
// power state, and tool availability.
//
// Collection is best-effort: anything unavailable is recorded as
// null/"unknown" with the reason, never zero. Absolute RSS semantics
// differ per OS and are labelled, never normalized.
package envinfo

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"forcefield/internal/hpov/schema"
)

// Info is the collected environment.
type Info struct {
	Host       schema.Host
	IdleCPUPct *float64
}

// Collect gathers host and environment signals for workroot. The idle
// CPU sample takes ~3s (whole-system busy percentage).
func Collect(workroot string) (Info, error) {
	var info Info
	h := schema.Host{
		HostID:        hostID(),
		OS:            runtime.GOOS,
		OSVersion:     osVersion(),
		Arch:          runtime.GOARCH,
		Gomaxprocs:    runtime.GOMAXPROCS(0),
		HPOVGoVersion: runtime.Version(),
	}
	h.CPU.Model = cpuModel()
	h.CPU.Logical = runtime.NumCPU()
	h.CPU.Physical = physicalCores()
	h.MemoryTotalBytes = memoryTotal()
	h.Power = schema.Power{Source: powerSource(), Plan: powerPlan()}
	h.Clock.Source, h.Clock.ResolutionNs = CalibrateClock()
	h.FS = schema.FS{Workroot: fsKind(workroot)}
	h.Tools = DetectTools()
	h.Env = envLabel()
	info.Host = h
	info.IdleCPUPct = idleCPU(3 * time.Second)
	return info, nil
}

// hostID is a salted, truncated hash of the hostname: stable per host
// for baseline fingerprinting, never the hostname itself.
func hostID() string {
	name, _ := os.Hostname()
	sum := sha256.Sum256([]byte("hpov-host|" + name))
	return fmt.Sprintf("h:%x", sum[:8])
}

// CalibrateClock measures the observable time.Now resolution by
// spinning until the reading changes, 1000 times, and returns the
// median step. Sub-millisecond metrics are refused when the
// resolution is coarse; all HPOV headline metrics are ms-scale.
func CalibrateClock() (source string, resolutionNs int64) {
	const iters = 1000
	deltas := make([]int64, 0, iters)
	for i := 0; i < iters; i++ {
		t0 := time.Now()
		for {
			d := time.Since(t0).Nanoseconds()
			if d > 0 {
				deltas = append(deltas, d)
				break
			}
		}
	}
	sort.Slice(deltas, func(i, j int) bool { return deltas[i] < deltas[j] })
	return "go-timeNow", deltas[len(deltas)/2]
}

// envLabel classifies the platform: wsl2 is compared only with wsl2.
func envLabel() string {
	if isWSL() {
		return "wsl2"
	}
	for _, k := range []string{"GITHUB_ACTIONS", "TF_BUILD", "JENKINS_URL", "GITLAB_CI", "CIRCLECI"} {
		if os.Getenv(k) != "" {
			return "ci"
		}
	}
	if v := os.Getenv("CI"); v == "true" || v == "1" {
		return "ci"
	}
	if isVM() {
		return "vm"
	}
	return "native"
}

// DetectTools records git/rg/go availability (null when absent).
func DetectTools() schema.Tools {
	var t schema.Tools
	if v, ok := toolVersion("git", "--version", 2); ok {
		t.Git = &v
	}
	if v, ok := toolVersion("rg", "--version", 0); ok {
		t.Rg = &v
	}
	if v, ok := toolVersion("go", "version", 2); ok {
		t.Go = &v
	}
	return t
}

// toolVersion runs name arg and returns the field-th token of the
// first output line.
func toolVersion(name, arg string, field int) (string, bool) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", false
	}
	_ = p
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, arg)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	f := strings.Fields(line)
	if field < len(f) {
		return strings.TrimSpace(f[field]), true
	}
	return line, true
}
