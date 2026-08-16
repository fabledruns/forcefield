// Package memory implements Forcefield's persistent project memory: a
// small set of durable, user-approved facts that survive across
// sessions so an agent doesn't start from zero every time it works on
// the same project.
//
// Memory is scoped in two layers, kept in separate files so one never
// leaks into the other:
//
//   - Project memory lives under ~/.forcefield/memory/projects/ and is
//     keyed off the current project's Git repository root (falling back
//     to the working directory outside a repo). This is what "ff memory"
//     operates on and what gets loaded into an agent's context.
//   - Global memory lives at ~/.forcefield/memory/global.json and holds
//     facts that apply everywhere, independent of project.
//
// Both scopes share the same on-disk format and the same Store
// implementation; only the file path differs.
package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Entry is a single durable memory fact.
type Entry struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Text      string    `json:"text"`
}

// Store manages the entries persisted in a single JSON file. It is safe
// to use for both project and global scopes; construct one via
// ProjectStore or GlobalStore rather than directly.
type Store struct {
	path string
}

// newStore returns a Store backed by path. The file is created lazily,
// on the first write - Load on a missing file simply returns no entries.
func newStore(path string) *Store {
	return &Store{path: path}
}

// Path returns the JSON file this store reads from and writes to.
func (s *Store) Path() string { return s.path }

// Load returns every entry currently persisted, oldest first. A store
// that has never been written to returns an empty (non-nil) slice.
func (s *Store) Load() ([]Entry, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read memory file %s: %w", s.path, err)
	}

	if strings.TrimSpace(string(data)) == "" {
		return []Entry{}, nil
	}

	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse memory file %s: %w", s.path, err)
	}
	if entries == nil {
		entries = []Entry{}
	}
	return entries, nil
}

// save overwrites the store's file with entries.
func (s *Store) save(entries []Entry) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create memory directory for %s: %w", s.path, err)
	}

	if entries == nil {
		entries = []Entry{}
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal memory entries: %w", err)
	}

	if err := os.WriteFile(s.path, data, 0o644); err != nil {
		return fmt.Errorf("write memory file %s: %w", s.path, err)
	}
	return nil
}

// ErrEmptyText is returned by Add when given blank text.
var ErrEmptyText = fmt.Errorf("memory text cannot be empty")

// Add persists a new memory entry containing text, unless an entry with
// the same normalized text already exists, in which case the existing
// entry is returned unchanged and added is false. This is the only
// duplicate-prevention rule: exact (whitespace-trimmed) text matches.
func (s *Store) Add(text string) (entry Entry, added bool, err error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Entry{}, false, ErrEmptyText
	}

	entries, err := s.Load()
	if err != nil {
		return Entry{}, false, err
	}

	for _, e := range entries {
		if strings.EqualFold(strings.TrimSpace(e.Text), text) {
			return e, false, nil
		}
	}

	entry = Entry{
		ID:        newID(entries),
		CreatedAt: time.Now(),
		Text:      text,
	}

	entries = append(entries, entry)
	if err := s.save(entries); err != nil {
		return Entry{}, false, err
	}

	return entry, true, nil
}

// ErrNotFound is returned by Remove when no entry has the given ID.
var ErrNotFound = fmt.Errorf("memory entry not found")

// Remove deletes the entry with the given ID.
func (s *Store) Remove(id string) error {
	entries, err := s.Load()
	if err != nil {
		return err
	}

	kept := make([]Entry, 0, len(entries))
	found := false
	for _, e := range entries {
		if e.ID == id {
			found = true
			continue
		}
		kept = append(kept, e)
	}

	if !found {
		return fmt.Errorf("remove memory entry %q: %w", id, ErrNotFound)
	}

	return s.save(kept)
}

// Clear removes every entry from the store.
func (s *Store) Clear() error {
	return s.save([]Entry{})
}

// newID generates a short, human-typeable ID that doesn't collide with
// any ID already present in existing.
func newID(existing []Entry) string {
	taken := make(map[string]bool, len(existing))
	for _, e := range existing {
		taken[e.ID] = true
	}

	for {
		id := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
		if !taken[id] {
			return id
		}
	}
}

// FormatForPrompt renders entries for inclusion in an agent's system
// prompt. Returns "" for no entries, so callers can skip the section
// entirely rather than emit an empty header.
func FormatForPrompt(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}

	var b strings.Builder
	for i, e := range entries {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "- %s", e.Text)
	}
	return b.String()
}
