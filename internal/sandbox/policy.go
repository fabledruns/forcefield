package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveExistingDir validates a requested working directory without any
// scope enforcement - the exact historical behavior native mode preserves:
// empty means the process cwd, relative paths anchor there, and the only
// requirement is that the directory exists after symlink resolution.
func resolveExistingDir(dir string) (string, error) {
	target := dir
	if strings.TrimSpace(target) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve working directory: %w", err)
		}
		return filepath.Clean(wd), nil
	}
	if !filepath.IsAbs(target) {
		abs, err := filepath.Abs(target)
		if err != nil {
			return "", fmt.Errorf("resolve working directory %s: %w", target, err)
		}
		target = abs
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(target))
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidDir, target)
	}
	return resolved, nil
}

// resolveWithinWorkspace validates a requested working directory against
// the workspace and returns the absolute directory to run in.
//
// Enforcement rules, applied before any process is constructed:
//
//   - An empty request resolves to the workspace itself.
//   - The requested directory is made absolute against its own base
//     (host semantics: relative paths are relative to the workspace, NOT
//     to wherever Forcefield happens to be running) and then resolved
//     through EvalSymlinks so a symlink pointing outside the workspace is
//     caught as an escape rather than silently passing the prefix check.
//   - The resolved path must equal the workspace or live underneath it.
//     Scope is never expanded to make a request succeed.
//
// Windows and Linux path shapes differ (drive letters, backslashes,
// case-insensitivity, UNC); every comparison goes through filepath
// helpers plus an explicit case-insensitive boundary-aware prefix match,
// never raw string math.
func resolveWithinWorkspace(workspace, dir string) (string, error) {
	ws := workspace
	if strings.TrimSpace(ws) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve workspace: %w", err)
		}
		ws = wd
	}

	wsAbs, err := filepath.Abs(filepath.Clean(ws))
	if err != nil {
		return "", fmt.Errorf("resolve workspace %s: %w", ws, err)
	}
	wsResolved, err := filepath.EvalSymlinks(wsAbs)
	if err != nil {
		return "", fmt.Errorf("%w: %s (%v)", ErrInvalidDir, wsAbs, err)
	}

	target := dir
	if strings.TrimSpace(target) == "" {
		return wsResolved, nil
	}

	// Relative requests are interpreted from inside the workspace, which
	// is the only base that makes sense under a filesystem scope.
	base := wsResolved
	if filepath.IsAbs(target) || isAbsLike(target) {
		base = ""
	}

	if runtimeCaseInsensitive() { // Windows host path semantics apply
		switch {
		case strings.HasPrefix(target, "/"):
			// A Linux-absolute path addresses the WSL filesystem, which no
			// Windows-side workspace contains. Refuse as an escape instead
			// of letting volume guessing turn it into C:\home\...
			return "", fmt.Errorf("%w: %s is a Linux filesystem path; the sandboxed workspace is %s",
				ErrWorkspaceEscape, target, wsResolved)
		case len(target) >= 2 && target[1] == ':' && len(target) > 2 && target[2] != '\\' && target[2] != '/':
			// Drive-relative ("C:foo"): its meaning depends on the current
			// directory of that drive, which makes it ambiguous under a
			// filesystem scope. Reject rather than guess.
			return "", fmt.Errorf("%w: %s is a drive-relative path", ErrInvalidDir, target)
		case len(target) == 2 && target[1] == ':':
			return "", fmt.Errorf("%w: %s is a bare drive letter", ErrInvalidDir, target)
		}
	}

	abs, err := filepath.Abs(filepath.Join(base, filepath.Clean(target)))
	if err != nil {
		return "", fmt.Errorf("resolve working directory %s: %w", target, err)
	}

	// The workspace has two equally valid spellings after resolution
	// (e.g. /var/x vs /private/var/x on macOS, or long vs 8.3 short user
	// names on Windows); EvalSymlinks may return either depending on which
	// components it walks. Containment therefore accepts any KNOWN
	// spelling of the same root - and nothing else. Targets that resolve
	// under some third path are escapes.
	//
	// Resolve first, classify second: symlinks can point INWARD (a
	// differently-spelled alias of the workspace) or OUTWARD (an escape),
	// and only the resolved form tells them apart. The unresolved path is
	// used solely to classify failures for nonexistent directories.
	resolved, evalErr := filepath.EvalSymlinks(abs)
	if evalErr != nil {
		if !withinAny(wsAbs, wsResolved, abs) {
			return "", fmt.Errorf("%w: %s is outside %s", ErrWorkspaceEscape, abs, wsResolved)
		}
		return "", fmt.Errorf("%w: %s", ErrInvalidDir, abs)
	}

	// The workspace has two equally valid spellings after resolution
	// (e.g. /var/x vs /private/var/x on macOS, or long vs 8.3 short user
	// names on Windows); EvalSymlinks may return either depending on which
	// components it walks. Containment therefore accepts any KNOWN
	// spelling of the same root - and nothing else. A target that
	// resolves under some third path is an escape.
	if !withinAny(wsAbs, wsResolved, resolved) {
		return "", fmt.Errorf("%w: %s resolves outside %s", ErrWorkspaceEscape, resolved, wsResolved)
	}
	return resolved, nil
}

// ResolveWithinWorkspace is the exported, trusted primitive for
// workspace-caged path resolution. It is the same enforcement shell
// uses (resolveWithinWorkspace) and must be reused by filesystem
// tools when sandbox.mode=wsl so the boundary is not duplicated.
func ResolveWithinWorkspace(workspace, dir string) (string, error) {
	return resolveWithinWorkspace(workspace, dir)
}

// ResolveExistingDir is the exported historical resolver used by
// native mode (existence-only, no scope).
func ResolveExistingDir(dir string) (string, error) {
	return resolveExistingDir(dir)
}

// EnsureWithinWorkspace validates a path intended for creation (the
// file may not yet exist) against the workspace. It lexically ensures
// the target is inside the workspace, follows symlinks for the full
// path when it exists, and otherwise walks existing ancestors to catch
// symlink escapes. The returned path is the absolute, cleaned location
// safe to use when the check succeeds.
func EnsureWithinWorkspace(workspace, path string) (string, error) {
	ws := workspace
	if strings.TrimSpace(ws) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve workspace: %w", err)
		}
		ws = wd
	}
	wsAbs, err := filepath.Abs(filepath.Clean(ws))
	if err != nil {
		return "", fmt.Errorf("resolve workspace %s: %w", ws, err)
	}
	wsResolved, err := filepath.EvalSymlinks(wsAbs)
	if err != nil {
		return "", fmt.Errorf("%w: %s (%v)", ErrInvalidDir, wsAbs, err)
	}
	target := path
	if strings.TrimSpace(target) == "" {
		return wsResolved, nil
	}
	if runtimeCaseInsensitive() {
		switch {
		case strings.HasPrefix(target, "/"):
			return "", fmt.Errorf("%w: %s is a Linux filesystem path; the sandboxed workspace is %s",
				ErrWorkspaceEscape, target, wsResolved)
		case len(target) >= 2 && target[1] == ':' && len(target) > 2 && target[2] != '\\' && target[2] != '/':
			return "", fmt.Errorf("%w: %s is a drive-relative path", ErrInvalidDir, target)
		case len(target) == 2 && target[1] == ':':
			return "", fmt.Errorf("%w: %s is a bare drive letter", ErrInvalidDir, target)
		}
	}
	base := wsResolved
	if filepath.IsAbs(target) || isAbsLike(target) {
		base = ""
	}
	abs, err := filepath.Abs(filepath.Join(base, filepath.Clean(target)))
	if err != nil {
		return "", fmt.Errorf("resolve path %s: %w", target, err)
	}
	if !withinAny(wsAbs, wsResolved, abs) {
		return "", fmt.Errorf("%w: %s is outside %s", ErrWorkspaceEscape, abs, wsResolved)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		if !withinAny(wsAbs, wsResolved, resolved) {
			return "", fmt.Errorf("%w: %s resolves outside %s", ErrWorkspaceEscape, resolved, wsResolved)
		}
		return resolved, nil
	}
	// Not existent: walk existing ancestors to catch symlink escapes.
	cur := abs
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
		if _, err := os.Lstat(cur); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			continue
		}
		resolvedParent, err := filepath.EvalSymlinks(cur)
		if err != nil {
			return "", fmt.Errorf("%w: %s", ErrInvalidDir, abs)
		}
		if !withinAny(wsAbs, wsResolved, resolvedParent) {
			return "", fmt.Errorf("%w: %s resolves outside %s", ErrWorkspaceEscape, resolvedParent, wsResolved)
		}
		break
	}
	return abs, nil
}

// within reports whether path equals root or lies underneath it,
// comparing case-insensitively on Windows-style volumes and exactly
// elsewhere. Both arguments must be cleaned absolute paths.
func within(root, path string) bool {
	if samePath(root, path) {
		return true
	}
	sep := string(os.PathSeparator)
	prefix := root
	if !strings.HasSuffix(prefix, sep) {
		prefix += sep
	}
	if runtimeCaseInsensitive() {
		return strings.HasPrefix(strings.ToLower(path), strings.ToLower(prefix))
	}
	return strings.HasPrefix(path, prefix)
}

func samePath(a, b string) bool {
	if runtimeCaseInsensitive() {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// withinAny reports whether path lies under rootA or rootB - the two
// spellings a workspace root may take after partial symlink resolution.
// Both must denote the same directory by construction.
func withinAny(rootA, rootB, path string) bool {
	return within(rootA, path) || within(rootB, path)
}

// runtimeCaseInsensitive reports whether the host compares paths
// case-insensitively. Windows (including drive-letter paths) does; Linux
// and macOS default builds do not. Kept as a variable for tests.
var runtimeCaseInsensitive = defaultCaseInsensitive

func defaultCaseInsensitive() bool { return os.PathSeparator == '\\' && os.PathListSeparator == ';' }

// isAbsLike catches absolute-looking paths that filepath.IsAbs misses:
// Linux-style "/..." paths while running on Windows (a WSL cwd argument
// like /home/user is already absolute inside the distribution), and
// Windows drive-relative forms like "C:foo" whose meaning differs across
// systems. Treating them as absolute keeps them out of the
// join-with-workspace branch where they could masquerade as relative and
// slip past containment checks.
func isAbsLike(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return true
	}
	if len(p) >= 2 && p[1] == ':' {
		return true // "C:foo" or "C:\foo": volume-anchored either way
	}
	return false
}
