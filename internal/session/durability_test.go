package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

func TestSaveIsAtomicUnderSimulatedCrash(t *testing.T) {
	chdirTemp(t)

	s := New()
	s.AddMessage("user", "first draft")

	if err := s.Save(); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}
	path := filepath.Join(".forcefield", "sessions", s.ID+".json")
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved session: %v", err)
	}

	// Simulate a crash mid-write: garbage bytes in place of the file must
	// never be produced by Save itself, but if a previous version of
	// Forcefield (or a killed process) left one behind, Load must report it
	// instead of silently returning an empty session.
	if err := os.WriteFile(path, []byte(`{"id":"`), 0o644); err != nil {
		t.Fatalf("clobber session: %v", err)
	}
	if _, err := Load(s.ID); err == nil {
		t.Fatal("Load() on corrupted file returned no error")
	}

	// Re-saving must fully replace the corrupted file with valid data.
	s.AddMessage("assistant", "recovered")
	if err := s.Save(); err != nil {
		t.Fatalf("second Save() error = %v", err)
	}

	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load() after re-save error = %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("loaded %d messages, want 2", len(loaded.Messages))
	}

	after, _ := os.ReadFile(path)
	if string(after) == string(good) {
		t.Error("re-saved file is byte-identical to pre-corruption content; save did not replace it")
	}
}

func TestSaveLeavesNoTempDebris(t *testing.T) {
	chdirTemp(t)

	for i := 0; i < 5; i++ {
		s := New()
		if err := s.Save(); err != nil {
			t.Fatalf("Save() error = %v", err)
		}
	}

	entries, err := os.ReadDir(filepath.Join(".forcefield", "sessions"))
	if err != nil {
		t.Fatalf("read sessions dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
		if filepath.Ext(e.Name()) != ".json" {
			t.Errorf("unexpected non-json entry: %s", e.Name())
		}
	}
}

func TestListSkipsCorruptFilesAndReportsThem(t *testing.T) {
	chdirTemp(t)

	healthy := New()
	healthy.AddMessage("user", "i am fine")
	if err := healthy.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	corruptPath := filepath.Join(".forcefield", "sessions", "deadbeef.json")
	if err := os.WriteFile(corruptPath, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	sessions, corrupt, err := ListCorrupt()
	if err != nil {
		t.Fatalf("ListCorrupt() error = %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != healthy.ID {
		t.Fatalf("sessions = %+v, want only healthy session", sessions)
	}
	if len(corrupt) != 1 {
		t.Fatalf("corrupt = %+v, want exactly the deadbeef file", corrupt)
	}
	if !strings.Contains(strings.ReplaceAll(filepath.ToSlash(corrupt[0].Path), "\\", "/"), "deadbeef.json") {
		t.Errorf("corrupt path = %q, want deadbeef.json", corrupt[0].Path)
	}

	// List (the compatibility wrapper) still succeeds despite the corrupt
	// neighbor - one bad file must not break /sessions entirely.
	sessions, err = List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("List() = %d sessions, want 1", len(sessions))
	}
}

func TestLoadRejectsUnsafeIDs(t *testing.T) {
	chdirTemp(t)

	for _, id := range []string{"", ".", "..", "../escape", `..\escape`, "a/b", strings.Repeat("x", 200)} {
		if _, err := Load(id); err == nil {
			t.Errorf("Load(%q) succeeded, want rejection", id)
		}
	}
}

func TestSaveRejectsUnsafeIDs(t *testing.T) {
	chdirTemp(t)

	s := New()
	s.ID = "../../escape"
	if err := s.Save(); err == nil {
		t.Fatal("Save() with traversal ID succeeded, want rejection")
	}
	if _, err := os.Stat(filepath.Join("..", "escape.json")); err == nil {
		t.Fatal("file written outside sessions directory")
	}
}

func TestLoadMissingSessionGivesFriendlyError(t *testing.T) {
	chdirTemp(t)

	_, err := Load("00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatal("Load() on missing session returned no error")
	}
	if !strings.Contains(err.Error(), "no saved session") {
		t.Errorf("error = %v, want a 'no saved session' message", err)
	}
}

func TestLoadBackfillsMissingIDFromFilename(t *testing.T) {
	chdirTemp(t)

	body := []byte(`{"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","messages":[]}`)
	if err := os.MkdirAll(filepath.Join(".forcefield", "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(".forcefield", "sessions", "legacy-id.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Load("legacy-id")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if s.ID != "legacy-id" {
		t.Errorf("ID = %q, want backfilled \"legacy-id\"", s.ID)
	}
}

func TestConcurrentSavesOfSameSession(t *testing.T) {
	chdirTemp(t)

	s := New()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			local := *s
			local.Messages = append([]Message(nil), s.Messages...)
			local.AddMessage("user", "concurrent writer")
			if err := local.Save(); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Save() error = %v", err)
	}

	// The file must still parse: concurrent atomic renames leave either a
	// complete old or complete new file, never a torn one.
	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load() after concurrent saves: %v", err)
	}
	if len(loaded.Messages) != 1 {
		t.Fatalf("loaded %d messages, want 1", len(loaded.Messages))
	}
}

func TestUnicodeContentSurvivesSaveLoadList(t *testing.T) {
	chdirTemp(t)

	text := "héllo 世界 🚀 \x1b[31mnot-ansi\x1b[0m ✓"
	s := New()
	s.AddMessage("user", text)
	if err := s.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Messages[0].Content != text {
		t.Errorf("content = %q, want verbatim %q", loaded.Messages[0].Content, text)
	}

	sessions, err := List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(sessions) != 1 || sessions[0].Messages[0].Content != text {
		t.Errorf("List() round-trip lost content: %+v", sessions)
	}
}

func TestListEmptyWhenNoSessionsDir(t *testing.T) {
	chdirTemp(t)

	sessions, corrupt, err := ListCorrupt()
	if err != nil {
		t.Fatalf("ListCorrupt() error = %v", err)
	}
	if len(sessions) != 0 || len(corrupt) != 0 {
		t.Errorf("ListCorrupt() = (%d sessions, %d corrupt), want empty", len(sessions), len(corrupt))
	}
}

func TestCorruptionErrorMessage(t *testing.T) {
	c := Corruption{Path: "x.json", Err: errors.New("bad")}
	if !strings.Contains(c.Error(), "x.json") || !strings.Contains(c.Error(), "bad") {
		t.Errorf("Corruption.Error() = %q", c.Error())
	}
}
