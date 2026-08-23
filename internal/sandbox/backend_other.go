//go:build !windows

package sandbox

// BackendFailure never reports a backend failure outside Windows: a
// failing command is always the command's own doing. It mirrors the
// Windows-side classifier so callers can share one code path.
func BackendFailure(_, _ string) (detail, hint string) { return "", "" }
