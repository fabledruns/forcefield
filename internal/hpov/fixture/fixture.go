// Package fixture builds isolated, untimed benchmark fixtures:
// run-scoped temp roots, per-iteration homes and workdirs, and
// allowlist-scrubbed environments.
package fixture

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"forcefield/internal/hpov/bench"
)

// NewRunRoot creates the run-scoped temp root on the chosen
// filesystem. On WSL a /mnt/c workroot is refused (translation layers
// distort every number); the Linux filesystem must be used.
func NewRunRoot(workroot string) (string, error) {
	if workroot == "" {
		workroot = os.TempDir()
	}
	if runtime.GOOS == "linux" {
		if raw, err := os.ReadFile("/proc/version"); err == nil {
			if strings.Contains(strings.ToLower(string(raw)), "microsoft") {
				if abs, err := filepath.Abs(workroot); err == nil {
					if strings.HasPrefix(abs, "/mnt/") {
						return "", fmt.Errorf("wsl workroot must be on the Linux filesystem, got %s", abs)
					}
				}
			}
		}
	}
	root, err := os.MkdirTemp(workroot, "hpov-*")
	if err != nil {
		return "", fmt.Errorf("create run root: %w", err)
	}
	return root, nil
}

// NewHome creates one isolated home directory (fresh per first-run
// iteration, shared for steady).
func NewHome(root, name string) (string, error) {
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create home %s: %w", name, err)
	}
	return dir, nil
}

// NewWorkDir creates one isolated working directory.
func NewWorkDir(root, name string) (string, error) {
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create workdir %s: %w", name, err)
	}
	return dir, nil
}

// baseAllowlist is the inherited environment allowlist. Everything
// else is dropped; proxy variables, and whatever the subject's contract
// names, are recorded in Removed.
func baseAllowlist() []string {
	if runtime.GOOS == "windows" {
		return []string{"PATH", "PATHEXT", "SYSTEMROOT", "TEMP", "TMP", "OS"}
	}
	return []string{"PATH", "HOME", "TMPDIR"}
}

// ScrubEnv builds the child environment from the allowlist plus set
// overrides. It returns the block and the removed special variables
// (recorded in environment.env_overrides).
//
// scrub names the variables the subject's own contract keeps out of a
// measured process. HPOV itself has no product to hide: a subject that
// declares nothing gets only the generic allowlist and proxy scrubbing.
func ScrubEnv(set map[string]string, scrub bench.Env) (env []string, removed []string) {
	keep := map[string]bool{}
	for _, k := range baseAllowlist() {
		keep[k] = true
	}
	for k := range set {
		keep[k] = true
	}
	scrubbed := func(up string) bool {
		if isGenericRemoved(up) {
			return true
		}
		for _, p := range scrub.ScrubPrefixes {
			if strings.HasPrefix(up, strings.ToUpper(p)) {
				return true
			}
		}
		for _, e := range scrub.ScrubExact {
			if up == strings.ToUpper(e) {
				return true
			}
		}
		return false
	}
	for _, kv := range os.Environ() {
		k := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			k = kv[:i]
		}
		up := strings.ToUpper(k)
		if scrubbed(up) {
			if _, overridden := set[k]; !overridden {
				removed = append(removed, k)
			}
			continue
		}
		if keep[k] || keep[up] {
			env = append(env, kv)
		}
	}
	for k, v := range set {
		env = append(env, k+"="+v)
	}
	sort.Strings(removed)
	sort.Strings(env)
	return env, removed
}

// isGenericRemoved lists inherited variables that must never reach a
// measured process regardless of subject: proxies would silently reroute
// a subject's network calls, and GIT_* would let ambient repository
// state change what the subject does.
func isGenericRemoved(up string) bool {
	if strings.HasPrefix(up, "GIT_") {
		return true
	}
	switch up {
	case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
		return true
	}
	return false
}

// HomeEnv returns the home-override variables isolating config.Dir
// (os.UserHomeDir): USERPROFILE on Windows, HOME on Unix. HOME-like
// variables other tools might read are isolated too.
func HomeEnv(home string) map[string]string {
	if runtime.GOOS == "windows" {
		return map[string]string{"USERPROFILE": home, "HOME": home}
	}
	return map[string]string{"HOME": home}
}
