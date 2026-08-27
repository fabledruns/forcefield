package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ProjectRoot identifies the project rooted at dir: the top level of its
// Git repository if dir is inside one, otherwise dir itself (absolute).
// This is the single source of truth for "which project is this" used
// by both the CLI and the runtime, so a repo checked out at different
// paths - or a subdirectory within it - always resolves to the same
// project identity.
func ProjectRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path for %s: %w", dir, err)
	}

	if root, ok := gitRoot(abs); ok {
		return root, nil
	}

	return abs, nil
}

// gitRoot runs "git rev-parse --show-toplevel" in dir and reports the
// repository root, if dir is inside a Git working tree at all. Any
// failure (git not installed, not a repo, etc.) is treated as "no git
// root" rather than a hard error, since falling back to the working
// directory is an explicit part of the contract.
func gitRoot(dir string) (string, bool) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir

	out, err := cmd.Output()
	if err != nil {
		return "", false
	}

	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", false
	}
	return root, true
}

// projectKey derives a stable, filesystem-safe identifier for a project
// root: a short slug from its base name (for readability) followed by a
// hash of the full path (for uniqueness, since two projects can share a
// base name).
func projectKey(root string) string {
	sum := sha256.Sum256([]byte(root))
	hash := hex.EncodeToString(sum[:])[:12]

	base := filepath.Base(root)
	slug := slugify(base)
	if slug == "" {
		return hash
	}
	return slug + "-" + hash
}

func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// ProjectStore returns the memory Store for the project rooted at
// projectRoot, under forcefieldHome/memory/projects/.
func ProjectStore(forcefieldHome, projectRoot string) (*Store, error) {
	dir := filepath.Join(forcefieldHome, "memory", "projects")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create project memory directory %s: %w", dir, err)
	}
	_ = os.Chmod(dir, 0o700)
	// Also ensure parent memory dir is restrictive.
	_ = os.Chmod(filepath.Join(forcefieldHome, "memory"), 0o700)

	path := filepath.Join(dir, projectKey(projectRoot)+".json")
	return newStore(path), nil
}

// GlobalStore returns the memory Store for facts that apply across every
// project, kept separate from any project-scoped store.
func GlobalStore(forcefieldHome string) (*Store, error) {
	dir := filepath.Join(forcefieldHome, "memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create memory directory %s: %w", dir, err)
	}
	_ = os.Chmod(dir, 0o700)

	return newStore(filepath.Join(dir, "global.json")), nil
}

// CurrentProjectStore is a convenience that resolves the project root
// starting from the current working directory and returns its Store.
func CurrentProjectStore(forcefieldHome string) (*Store, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}

	root, err := ProjectRoot(cwd)
	if err != nil {
		return nil, err
	}

	return ProjectStore(forcefieldHome, root)
}
