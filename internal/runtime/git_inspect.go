package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// GitInspect runs read-only git through the full manager (same confinement
// and caps as model calls). Human-typed /diff|/git is consent: no prompts.
// See docs/Runtime.md.
func (r *Runtime) GitInspect(ctx context.Context, action, path string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("runtime not available")
	}
	r.mu.RLock()
	m := r.fullManager
	r.mu.RUnlock()
	if m == nil {
		return "", fmt.Errorf("runtime not available")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	args := map[string]any{"action": action}
	if strings.TrimSpace(path) != "" {
		args["path"] = strings.TrimSpace(path)
	}
	res, err := m.Execute(ctx, "git", args)
	if err != nil {
		return "", err
	}
	if res.IsError {
		return "", fmt.Errorf("%s", res.Content)
	}
	return res.Content, nil
}

// TreeSignature returns the hex SHA-256 of the workspace status output: a
// compact drift signal /plan stores and /build compares. Errors (git
// missing, not a repository) surface with an empty signature; callers
// treat empty as "unknown" and skip the workspace check, never as clean.
func (r *Runtime) TreeSignature(ctx context.Context) (string, error) {
	out, err := r.GitInspect(ctx, "status", "")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(out))
	return hex.EncodeToString(sum[:]), nil
}
