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
// else is dropped; GIT_*, FF_* (except benchmark-set) and proxy
// variables are recorded in Removed.
func baseAllowlist() []string {
	if runtime.GOOS == "windows" {
		return []string{"PATH", "PATHEXT", "SYSTEMROOT", "TEMP", "TMP", "OS"}
	}
	return []string{"PATH", "HOME", "TMPDIR"}
}

// ScrubEnv builds the child environment from the allowlist plus set
// overrides. It returns the block and the removed special variables
// (recorded in environment.env_overrides).
func ScrubEnv(set map[string]string) (env []string, removed []string) {
	keep := map[string]bool{}
	for _, k := range baseAllowlist() {
		keep[k] = true
	}
	for k := range set {
		keep[k] = true
	}
	for _, kv := range os.Environ() {
		k := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			k = kv[:i]
		}
		up := strings.ToUpper(k)
		if isSpecialRemoved(k, up) {
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

func isSpecialRemoved(k, up string) bool {
	if strings.HasPrefix(up, "GIT_") || strings.HasPrefix(up, "FF_") {
		return true
	}
	switch up {
	case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
		return true
	}
	_ = k
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
