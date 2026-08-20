// Package skills implements Forcefield's on-demand skill system.
//
// Skills are indexed once at startup into an in-memory Store. The model
// receives only a lightweight catalog (id, name, description) and can load
// a skill's full Markdown body on demand via the load_skill tool.
//
// Skill metadata is optional. YAML frontmatter is preferred, but plain
// Markdown files are fully supported.
package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	catalog []Skill
	byID    map[string]Skill
}

// New builds a Store by scanning the skills directory once.
func New(forcefieldHome string) (*Store, error) {
	dir, err := Dir(forcefieldHome)
	if err != nil {
		return nil, err
	}

	names, err := markdownFiles(dir)
	if err != nil {
		return nil, err
	}

	catalog := make([]Skill, 0, len(names))
	byID := make(map[string]Skill, len(names))

	for _, name := range names {
		path := filepath.Join(dir, name)

		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read skill file %s: %w", path, err)
		}
		if strings.TrimSpace(string(raw)) == "" {
			continue
		}

		p := parse(name, string(raw))
		skill := Skill{
			ID:          p.id,
			Name:        p.name,
			Description: p.description,
			Path:        path,
		}
		catalog = append(catalog, skill)

		if _, exists := byID[skill.ID]; !exists {
			byID[skill.ID] = skill
		}
	}

	return &Store{
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

	skill, ok := s.Get(id)
	if !ok {
		return "", fmt.Errorf("load skill %q: %w", id, ErrSkillNotFound)
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
