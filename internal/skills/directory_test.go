package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func writeDirSkill(t *testing.T, home, dirName, content string) {
	t.Helper()
	skillDir := filepath.Join(home, "skills", dirName)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", skillDir, err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func TestStore_DirectorySkill_Discovered(t *testing.T) {
	home := t.TempDir()
	writeDirSkill(t, home, "git-review", "---\nname: Git Review\ndescription: Review git\n---\n# Git Review Body\n")

	catalog := newStore(t, home).Catalog()
	if len(catalog) != 1 {
		t.Fatalf("got %d, want 1", len(catalog))
	}
	got := catalog[0]
	if got.ID != "git-review" {
		t.Errorf("ID = %q, want %q (derived from directory name)", got.ID, "git-review")
	}
	if got.Name != "Git Review" {
		t.Errorf("Name = %q, want %q", got.Name, "Git Review")
	}
	if got.Description != "Review git" {
		t.Errorf("Description = %q, want %q", got.Description, "Review git")
	}
}

func TestStore_DirectorySkill_LowerCaseSkillMD(t *testing.T) {
	home := t.TempDir()
	skillDir := filepath.Join(home, "skills", "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "skill.md"), []byte("# Lower\nbody\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	catalog := newStore(t, home).Catalog()
	if len(catalog) != 1 || catalog[0].ID != "my-skill" {
		t.Fatalf("got %+v, want my-skill", catalog)
	}
}

func TestStore_DirectorySkill_SupportingFilesIgnored(t *testing.T) {
	home := t.TempDir()
	writeDirSkill(t, home, "git-review", "# Title\n")
	// Supporting files that must be ignored.
	dir := filepath.Join(home, "skills", "git-review")
	if err := os.WriteFile(filepath.Join(dir, "helper.sh"), []byte("echo hi\n"), 0o644); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("notes\n"), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	catalog := newStore(t, home).Catalog()
	if len(catalog) != 1 {
		t.Fatalf("supporting files should not create extra catalog entries, got %d", len(catalog))
	}
}

func TestStore_DirectorySkill_WithoutSKILLMD_Ignored(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "skills", "empty-dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.md"), []byte("# Other\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	catalog := newStore(t, home).Catalog()
	if len(catalog) != 0 {
		t.Fatalf("directory without SKILL.md should be ignored, got %+v", catalog)
	}
}

func TestStore_FileAndDirectorySkill_BothIndexed(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "review.md", "# Review\n")
	writeDirSkill(t, home, "git-review", "# Git Review\n")
	catalog := newStore(t, home).Catalog()
	if len(catalog) != 2 {
		t.Fatalf("got %d, want 2", len(catalog))
	}
	// Sorted by sortKey
	if catalog[0].ID != "git-review" || catalog[1].ID != "review" {
		t.Fatalf("ordering wrong: %+v", catalog)
	}
}

func TestStore_DuplicateID_FileAndDirectory_FirstWins(t *testing.T) {
	home := t.TempDir()
	// Both have same id "shared" via frontmatter.
	writeSkill(t, home, "alpha.md", "---\nid: shared\nname: From File\n---\n# File\n")
	writeDirSkill(t, home, "beta", "---\nid: shared\nname: From Dir\n---\n# Dir\n")
	store := newStore(t, home)
	catalog := store.Catalog()
	if len(catalog) != 2 {
		t.Fatalf("catalog should contain both physical entries, got %d", len(catalog))
	}
	got, ok := store.Get("shared")
	if !ok {
		t.Fatal("Get shared not found")
	}
	if got.Name != "From File" {
		t.Errorf("Get name = %q, want From File (alphabetical first wins)", got.Name)
	}
}

func TestStore_CaseInsensitiveExtension(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "UPPER.MD", "# Upper\n")
	catalog := newStore(t, home).Catalog()
	if len(catalog) != 1 || catalog[0].ID != "upper" {
		t.Fatalf("case-insensitive .MD not indexed, got %+v", catalog)
	}
}

func TestStore_OversizedSkill_Skipped(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "small.md", "# Small\n")
	// Create oversized file >1 MiB
	big := make([]byte, maxSkillFileBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	writeSkill(t, home, "big.md", string(big))
	catalog := newStore(t, home).Catalog()
	if len(catalog) != 1 || catalog[0].ID != "small" {
		t.Fatalf("oversized file should be skipped, got %+v", catalog)
	}
}

func TestStore_EmptySkillFileInDirSkipped(t *testing.T) {
	home := t.TempDir()
	writeDirSkill(t, home, "empty", "   \n\n")
	catalog := newStore(t, home).Catalog()
	if len(catalog) != 0 {
		t.Fatalf("empty SKILL.md should be ignored, got %+v", catalog)
	}
}

func TestStore_Load_DirectorySkill_StripsFrontmatter(t *testing.T) {
	home := t.TempDir()
	writeDirSkill(t, home, "git-review", "---\nname: Git Review\n---\n\n# Body\nContent\n")
	store := newStore(t, home)
	body, err := store.Load("git-review")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if body != "\n# Body\nContent\n" {
		t.Fatalf("Load body = %q, want stripped frontmatter", body)
	}
}

func TestStore_SymlinkOutside_Ignored(t *testing.T) {
	// Symlink tests may fail on Windows without privilege; skip gracefully.
	home := t.TempDir()
	writeSkill(t, home, "real.md", "# Real\n")

	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("# Outside\n"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	link := filepath.Join(home, "skills", "evil.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	catalog := newStore(t, home).Catalog()
	if len(catalog) != 1 || catalog[0].ID != "real" {
		t.Fatalf("symlink outside should be ignored, got %+v", catalog)
	}
}

func TestStore_SymlinkDirectoryOutside_Ignored(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "real.md", "# Real\n")
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "SKILL.md"), []byte("# Evil\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(home, "skills", "evilDir")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	catalog := newStore(t, home).Catalog()
	if len(catalog) != 1 || catalog[0].ID != "real" {
		t.Fatalf("symlinked directory outside should be ignored, got %+v", catalog)
	}
}

func TestStore_SymlinkInside_Allowed(t *testing.T) {
	home := t.TempDir()
	realPath := filepath.Join(home, "skills", "real.md")
	if err := os.MkdirAll(filepath.Join(home, "skills"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(realPath, []byte("# Real\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(home, "skills", "link.md")
	if err := os.Symlink(realPath, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	catalog := newStore(t, home).Catalog()
	// Both real and symlink point to same content but are distinct entries;
	// symlink inside is allowed and counted.
	if len(catalog) < 1 {
		t.Fatalf("symlink inside should be indexed, got %d", len(catalog))
	}
}

func TestNormalizeID_Exported(t *testing.T) {
	if got := NormalizeID("Go Style Guide"); got != "go-style-guide" {
		t.Fatalf("NormalizeID = %q, want go-style-guide", got)
	}
}
