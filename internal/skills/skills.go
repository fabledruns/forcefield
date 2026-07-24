// Package skills loads reusable Markdown "skill" files from
// ~/.forcefield/skills/ and concatenates them into a single block of text
// that gets appended to an agent's system prompt.
//
// Skills are intentionally dumb: no frontmatter, no metadata, no
// conditional loading. Every .md file in the directory is included, every
// time. That simplicity is the point of this prototype.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dir returns the skills directory (~/.forcefield/skills), creating it if necessary.
func Dir(forcefieldHome string) (string, error) {
	dir := filepath.Join(forcefieldHome, "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create skills directory %s: %w", dir, err)
	}
	return dir, nil
}

// Load reads every *.md file directly inside the skills directory and
// concatenates their contents, sorted by filename for deterministic output.
// A missing or empty directory is not an error: it simply yields no skills.
func Load(forcefieldHome string) (string, error) {
	dir, err := Dir(forcefieldHome)
	if err != nil {
		return "", err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read skills directory %s: %w", dir, err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var parts []string
	for _, name := range names {
		path := filepath.Join(dir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read skill file %s: %w", path, err)
		}
		trimmed := strings.TrimSpace(string(content))
		if trimmed == "" {
			continue
		}
		parts = append(parts, trimmed)
	}

	return strings.Join(parts, "\n\n---\n\n"), nil
}
