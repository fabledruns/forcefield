// Package skills implements Forcefield's on-demand skill system.
//
// Skills are global, filesystem-first Markdown files under
// ~/.forcefield/skills/. They are indexed once at startup into an
// in-memory Store. The model receives only a lightweight catalog (id,
// name, description) and can load a skill's full Markdown body on
// demand via the load_skill tool.
//
// Skill metadata is optional. YAML frontmatter is preferred, but plain
// Markdown files are fully supported. Directory skills are also
// supported: a subdirectory containing SKILL.md (e.g.
// ~/.forcefield/skills/git-review/SKILL.md) is indexed as one skill
// whose id defaults to the directory name.
package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// maxSkillFileBytes bounds a single skill file. Skills are intended
	// to be concise instruction packs; an overly large file likely
	// indicates an accidental paste or binary. The limit keeps
	// startup and prompt building cheap and deterministic.
	maxSkillFileBytes = 1 << 20 // 1 MiB

	// maxCatalogEntries caps the number of skills indexed. Beyond this
	// the catalog is truncated deterministically.
	maxCatalogEntries = 256
)

// ErrSkillNotFound is returned when a skill ID cannot be resolved.
var ErrSkillNotFound = errors.New("skill not found")

// Skill is a lightweight catalog entry.
type Skill struct {
	ID          string
	Name        string
	Description string
	Path        string
}

// Store holds an in-memory index of all discovered skills.
type Store struct {
	dir     string // absolute skills dir, for confinement checks
	catalog []Skill
	byID    map[string]Skill
}

// New builds a Store by scanning the global skills directory once.
// Skills are global-only (see skills.Dir). Supported layouts:
//
//   - File skill:   ~/.forcefield/skills/<name>.md
//   - Directory:    ~/.forcefield/skills/<name>/SKILL.md
//
// Directory-based skills allow supporting files alongside the main
// instructions; only SKILL.md is indexed and supporting files are never
// executed automatically.
func New(forcefieldHome string) (*Store, error) {
	dir, err := Dir(forcefieldHome)
	if err != nil {
		return nil, err
	}

	candidates, err := collectCandidates(dir)
	if err != nil {
		return nil, err
	}

	catalog := make([]Skill, 0, len(candidates))
	byID := make(map[string]Skill, len(candidates))

	for _, c := range candidates {
		raw, err := os.ReadFile(c.path)
		if err != nil {
			return nil, fmt.Errorf("read skill file %s: %w", c.path, err)
		}
		if len(raw) > maxSkillFileBytes {
			// Oversized files are skipped, not fatal, to keep startup
			// resilient against accidental large pastes.
			continue
		}
		if strings.TrimSpace(string(raw)) == "" {
			continue
		}

		p := parse(c.logicalName, string(raw))
		// An empty id after normalization is not usable; derive from
		// logical name but if that also yields empty, skip the file
		// rather than indexing an unaddressable skill.
		if p.id == "" {
			continue
		}
		skill := Skill{
			ID:          p.id,
			Name:        p.name,
			Description: p.description,
			Path:        c.path,
		}
		catalog = append(catalog, skill)

		if _, exists := byID[skill.ID]; !exists {
			byID[skill.ID] = skill
		}
		// Enforce catalog cap deterministically: first N sorted
		// candidates win. The candidate list is already sorted.
		if len(catalog) >= maxCatalogEntries {
			break
		}
	}

	return &Store{
		dir:     dir,
		catalog: catalog,
		byID:    byID,
	}, nil
}

// Catalog returns a copy of the indexed skills.
func (s *Store) Catalog() []Skill {
	if s == nil || len(s.catalog) == 0 {
		return nil
	}
	out := make([]Skill, len(s.catalog))
	copy(out, s.catalog)
	return out
}

// Get returns a skill by ID.
func (s *Store) Get(id string) (Skill, bool) {
	if s == nil {
		return Skill{}, false
	}
	skill, ok := s.byID[id]
	return skill, ok
}

// Load returns the Markdown body of a skill.
func (s *Store) Load(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("load skill: id cannot be empty")
	}
	if strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("load skill %q: %w", id, ErrSkillNotFound)
	}

	skill, ok := s.Get(id)
	if !ok {
		return "", fmt.Errorf("load skill %q: %w", id, ErrSkillNotFound)
	}

	// Confinement: the stored path must still be within the skills dir
	// and not escape via a swapped symlink.
	if s != nil && s.dir != "" {
		resolved, err := filepath.EvalSymlinks(skill.Path)
		if err != nil {
			// If the file vanished or symlink is broken, surface as read error.
			raw, rerr := os.ReadFile(skill.Path)
			if rerr != nil {
				return "", fmt.Errorf("read skill file %s: %w", skill.Path, rerr)
			}
			_ = resolved
			_ = raw
		} else {
			if !isWithin(s.dir, resolved) {
				return "", fmt.Errorf("load skill %q: %w", id, ErrSkillNotFound)
			}
		}
	}

	info, err := os.Stat(skill.Path)
	if err != nil {
		return "", fmt.Errorf("read skill file %s: %w", skill.Path, err)
	}
	if info.Size() > maxSkillFileBytes {
		return "", fmt.Errorf("skill %q exceeds size limit (%d bytes)", id, maxSkillFileBytes)
	}

	raw, err := os.ReadFile(skill.Path)
	if err != nil {
		return "", fmt.Errorf("read skill file %s: %w", skill.Path, err)
	}

	p := parse(filepath.Base(skill.Path), string(raw))
	return p.body, nil
}

// Dir returns the skills directory, creating it if necessary.
func Dir(forcefieldHome string) (string, error) {
	dir := filepath.Join(forcefieldHome, "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create skills directory %s: %w", dir, err)
	}
	return dir, nil
}

// FormatCatalog renders the catalog for inclusion in a system prompt.
func FormatCatalog(catalog []Skill) string {
	if len(catalog) == 0 {
		return ""
	}

	var b strings.Builder
	for i, sk := range catalog {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "- id: `%s`, name: %q", sk.ID, sk.Name)
		if sk.Description != "" {
			fmt.Fprintf(&b, " — %s", sk.Description)
		}
	}
	return b.String()
}

func markdownFiles(dir string) ([]string, error) {
	// Deprecated: retained for compatibility; new code uses collectCandidates.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read skills directory %s: %w", dir, err)
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
	return names, nil
}

// candidate is one discoverable skill file on disk.
type candidate struct {
	path        string // absolute, resolved
	logicalName string // filename used for id derivation (e.g. "git-review.md")
	sortKey     string // lower-cased logical name without extension for sorting
}

// collectCandidates discovers file and directory skills under dir.
// It enforces symlink confinement, size pre-check, and deterministic ordering.
func collectCandidates(dir string) ([]candidate, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read skills directory %s: %w", dir, err)
	}

	var out []candidate
	for _, e := range entries {
		name := e.Name()
		// Reject names that would be ambiguous or allow traversal if ever
		// joined naively. ReadDir names never contain separators, but check
		// defensively for "..".
		if name == "." || name == ".." || strings.Contains(name, "/") || strings.Contains(name, "\\") {
			continue
		}
		full := filepath.Join(dir, name)

		info, err := os.Lstat(full)
		if err != nil {
			// Unreadable entry is skipped, not fatal; startup remains usable.
			continue
		}
		isSymlink := info.Mode()&os.ModeSymlink != 0
		resolved := full
		var resolvedInfo os.FileInfo
		if isSymlink {
			r, err := filepath.EvalSymlinks(full)
			if err != nil {
				continue
			}
			if !isWithin(dir, r) {
				continue
			}
			ri, err := os.Stat(r)
			if err != nil {
				continue
			}
			resolved = r
			resolvedInfo = ri
		} else {
			resolvedInfo = info
		}

		isDir := resolvedInfo.IsDir()
		// Also handle DirEntry symlink case where ReadDir reported not-dir
		// but target is dir (covered by resolvedInfo).
		if e.IsDir() {
			isDir = true
		}

		if isDir {
			// Directory skill: look for SKILL.md (case-insensitive) inside.
			skillFile := findSkillFileInDir(resolved, dir)
			if skillFile == "" {
				continue
			}
			// Size pre-check.
			if fi, err := os.Stat(skillFile); err == nil && fi.Size() > maxSkillFileBytes {
				continue
			}
			// Logical name for id derivation is the directory name.
			logical := name + ".md"
			out = append(out, candidate{
				path:        skillFile,
				logicalName: logical,
				sortKey:     strings.ToLower(strings.TrimSuffix(logical, filepath.Ext(logical))),
			})
			continue
		}

		// File skill.
		if !strings.EqualFold(filepath.Ext(name), ".md") {
			continue
		}
		if resolvedInfo.Size() > maxSkillFileBytes {
			continue
		}
		out = append(out, candidate{
			path:        resolved,
			logicalName: name,
			sortKey:     strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name))),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].sortKey == out[j].sortKey {
			return out[i].logicalName < out[j].logicalName
		}
		return out[i].sortKey < out[j].sortKey
	})
	return out, nil
}

// findSkillFileInDir locates SKILL.md case-insensitively under skillDir.
// It returns the absolute path to the file or "" if none. Symlink
// confinement is already ensured for skillDir itself; the file inside
// is checked for symlink escape as well.
func findSkillFileInDir(skillDir, rootDir string) string {
	entries, err := os.ReadDir(skillDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.EqualFold(e.Name(), "SKILL.md") {
			continue
		}
		full := filepath.Join(skillDir, e.Name())
		// If the SKILL.md itself is a symlink, ensure it stays within root.
		if info, err := os.Lstat(full); err == nil && info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(full)
			if err != nil || !isWithin(rootDir, resolved) {
				continue
			}
			// Also ensure the resolved target is still within the skillDir's
			// resolved tree, not escaping to another skill's dir via symlink.
			// For global-only layout, staying within rootDir is sufficient.
			return resolved
		}
		return full
	}
	return ""
}

// isWithin reports whether target is inside or equal to dir.
// Both are cleaned and evaluated with filepath semantics.
func isWithin(dir, target string) bool {
	cleanDir := filepath.Clean(dir)
	cleanTarget := filepath.Clean(target)
	if cleanTarget == cleanDir {
		return true
	}
	// Ensure prefix check does not match /skills2 for /skills
	sep := string(os.PathSeparator)
	if !strings.HasSuffix(cleanDir, sep) {
		cleanDir += sep
	}
	return strings.HasPrefix(cleanTarget, cleanDir)
}
