package skills

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeSkill(t *testing.T, home, name, content string) {
	t.Helper()
	dir := filepath.Join(home, "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func newStore(t *testing.T, home string) *Store {
	t.Helper()
	store, err := New(home)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store
}

func TestStore_FullFrontmatter(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "architecture-review.md", "---\n"+
		"id: architecture-review\n"+
		"name: Architecture Review\n"+
		"description: Design maintainable software systems.\n"+
		"---\n"+
		"# Before proposing an architecture\n"+
		"...\n")

	catalog := newStore(t, home).Catalog()
	if len(catalog) != 1 {
		t.Fatalf("got %d skills, want 1", len(catalog))
	}

	got := catalog[0]
	want := Skill{
		ID:          "architecture-review",
		Name:        "Architecture Review",
		Description: "Design maintainable software systems.",
	}
	if got.ID != want.ID || got.Name != want.Name || got.Description != want.Description {
		t.Errorf("got %+v, want ID/Name/Description %+v", got, want)
	}
}

func TestStore_FrontmatterWithoutID_DerivesFromFilename(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "Go Style Guide.md", "---\n"+
		"name: Go\n"+
		"description: Idiomatic Go development.\n"+
		"---\n"+
		"# Go\n...\n")

	catalog := newStore(t, home).Catalog()
	if len(catalog) != 1 {
		t.Fatalf("got %d skills, want 1", len(catalog))
	}
	if got, want := catalog[0].ID, "go-style-guide"; got != want {
		t.Errorf("ID = %q, want %q", got, want)
	}
}

func TestStore_PlainMarkdownWithHeading(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "go.md", "# Go\nPrefer small interfaces.\n")

	got := newStore(t, home).Catalog()[0]
	if got.ID != "go" {
		t.Errorf("ID = %q, want %q", got.ID, "go")
	}
	if got.Name != "Go" {
		t.Errorf("Name = %q, want %q (inferred from H1)", got.Name, "Go")
	}
	if got.Description != "" {
		t.Errorf("Description = %q, want empty (never inferred)", got.Description)
	}
}

func TestStore_PlainMarkdownWithoutHeadings(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "go.md", "Prefer small interfaces.\nReturn explicit errors.\n")

	got := newStore(t, home).Catalog()[0]
	if got.ID != "go" {
		t.Errorf("ID = %q, want %q", got.ID, "go")
	}
	if got.Name != "go" {
		t.Errorf("Name = %q, want filename fallback %q", got.Name, "go")
	}
}

func TestStore_ArbitraryMarkdown_NeverFails(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "misc.md", "Some *arbitrary* markdown with no structure at all.\n\n- a\n- b\n")

	catalog := newStore(t, home).Catalog()
	if len(catalog) != 1 || catalog[0].ID != "misc" {
		t.Fatalf("got %+v, want single skill with id 'misc'", catalog)
	}
}

func TestStore_MalformedFrontmatter_TreatedAsPlainMarkdown(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "broken.md", "---\nname: Broken\n# Fallback Heading\nbody text\n")

	got := newStore(t, home).Catalog()[0]
	if got.ID != "broken" {
		t.Errorf("ID = %q, want %q", got.ID, "broken")
	}
	if got.Name != "Fallback Heading" {
		t.Errorf("Name = %q, want %q (frontmatter should not have parsed)", got.Name, "Fallback Heading")
	}
}

func TestStore_InvalidYAML_TreatedAsPlainMarkdown(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "invalid.md", "---\nname: [unterminated\n---\n# Real Heading\n")

	got := newStore(t, home).Catalog()[0]
	if got.Name != "Real Heading" {
		t.Errorf("Name = %q, want %q (invalid YAML should not be parsed as frontmatter)", got.Name, "Real Heading")
	}
}

func TestStore_NonStringFrontmatterFields_TreatedAsAbsent(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "weird.md", "---\n"+
		"id: 123\n"+
		"name:\n  - a\n  - b\n"+
		"description: A real description.\n"+
		"---\n"+
		"# Weird\n")

	got := newStore(t, home).Catalog()[0]
	if got.ID != "weird" {
		t.Errorf("ID = %q, want filename-derived %q (numeric id should be ignored)", got.ID, "weird")
	}
	if got.Name != "Weird" {
		t.Errorf("Name = %q, want heading-derived %q (list name should be ignored)", got.Name, "Weird")
	}
	if got.Description != "A real description." {
		t.Errorf("Description = %q, want %q", got.Description, "A real description.")
	}
}

func TestStore_IgnoresNonMarkdownAndEmptyFiles(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "real.md", "# Real\nbody\n")
	writeSkill(t, home, "notes.txt", "not markdown\n")
	writeSkill(t, home, "empty.md", "   \n\n")

	catalog := newStore(t, home).Catalog()
	if len(catalog) != 1 || catalog[0].ID != "real" {
		t.Fatalf("got %+v, want only the 'real' skill", catalog)
	}
}

func TestStore_DeterministicOrdering(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "zebra.md", "# Zebra\n")
	writeSkill(t, home, "alpha.md", "# Alpha\n")
	writeSkill(t, home, "mango.md", "# Mango\n")

	catalog := newStore(t, home).Catalog()
	var ids []string
	for _, s := range catalog {
		ids = append(ids, s.ID)
	}
	want := []string{"alpha", "mango", "zebra"}
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("order[%d] = %q, want %q (full: %v)", i, ids[i], want[i], ids)
		}
	}
}

func TestStore_EmptyDirectory(t *testing.T) {
	home := t.TempDir()

	catalog := newStore(t, home).Catalog()
	if len(catalog) != 0 {
		t.Errorf("got %d skills, want 0", len(catalog))
	}
}

func TestStore_Get(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "go.md", "# Go\nPrefer small interfaces.\n")

	store := newStore(t, home)

	got, ok := store.Get("go")
	if !ok {
		t.Fatal("Get(\"go\"): expected found")
	}
	if got.ID != "go" || got.Name != "Go" {
		t.Errorf("Get(\"go\") = %+v, want ID=go Name=Go", got)
	}
	if got.Path == "" {
		t.Error("Get(\"go\"): Path should be set")
	}

	if _, ok := store.Get("missing"); ok {
		t.Error("Get(\"missing\"): expected not found")
	}
}

func TestStore_DuplicateID_FirstWins(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "alpha.md", "---\nid: shared\nname: From Alpha\n---\n# Alpha body\n")
	writeSkill(t, home, "beta.md", "---\nid: shared\nname: From Beta\n---\n# Beta body\n")

	store := newStore(t, home)
	catalog := store.Catalog()
	if len(catalog) != 2 {
		t.Fatalf("Catalog len = %d, want 2 (both files indexed)", len(catalog))
	}

	got, ok := store.Get("shared")
	if !ok {
		t.Fatal("Get(\"shared\"): expected found")
	}
	if got.Name != "From Alpha" {
		t.Errorf("Get name = %q, want %q (first filename wins)", got.Name, "From Alpha")
	}

	body, err := store.Load("shared")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if body != "# Alpha body\n" {
		t.Errorf("Load body = %q, want alpha body", body)
	}
}

func TestStore_Load_StripsValidFrontmatter(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "architecture.md", "---\n"+
		"name: Architecture Review\n"+
		"description: Design maintainable software systems.\n"+
		"---\n\n"+
		"# Before proposing an architecture\n\nUnderstand:\n\n- Scale\n")

	got, err := newStore(t, home).Load("architecture")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := "\n# Before proposing an architecture\n\nUnderstand:\n\n- Scale\n"
	if got != want {
		t.Errorf("Load body = %q, want %q", got, want)
	}
}

func TestStore_Load_PreservesFileExactlyWhenNoFrontmatter(t *testing.T) {
	home := t.TempDir()
	content := "# Go\r\nPrefer small interfaces.\r\nReturn explicit errors.\r\n"
	writeSkill(t, home, "go.md", content)

	got, err := newStore(t, home).Load("go")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != content {
		t.Errorf("Load body = %q, want exact original %q", got, content)
	}
}

func TestStore_Load_PreservesFileExactlyWhenFrontmatterMalformed(t *testing.T) {
	home := t.TempDir()
	content := "---\nname: Broken\n# No closing delimiter\nbody\n"
	writeSkill(t, home, "broken.md", content)

	got, err := newStore(t, home).Load("broken")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != content {
		t.Errorf("Load body = %q, want exact original %q", got, content)
	}
}

func TestStore_Load_CRLFFrontmatter(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "code-review.md",
		"---\r\nname: Code Review\r\ndescription: Review code.\r\n---\r\n\r\nReview in this order:\r\n\r\n1. Correctness\r\n")

	got, err := newStore(t, home).Load("code-review")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := "\r\nReview in this order:\r\n\r\n1. Correctness\r\n"
	if got != want {
		t.Errorf("Load body = %q, want %q", got, want)
	}
}

func TestStore_Load_NotFound(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "go.md", "# Go\n")

	_, err := newStore(t, home).Load("does-not-exist")
	if err == nil {
		t.Fatal("Load: expected error for unknown id, got nil")
	}
	if !errors.Is(err, ErrSkillNotFound) {
		t.Errorf("Load error = %v, want errors.Is(..., ErrSkillNotFound)", err)
	}
}

func TestStore_Load_EmptyID(t *testing.T) {
	home := t.TempDir()

	_, err := newStore(t, home).Load("")
	if err == nil {
		t.Fatal("Load: expected error for empty id, got nil")
	}
}

func TestStore_Catalog_ReturnsCopy(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, home, "go.md", "# Go\n")

	store := newStore(t, home)
	a := store.Catalog()
	a[0].ID = "mutated"
	b := store.Catalog()
	if b[0].ID != "go" {
		t.Errorf("Catalog() returned shared state; got ID %q after mutating a prior result", b[0].ID)
	}
}

func TestFormatCatalog(t *testing.T) {
	catalog := []Skill{
		{ID: "architecture-review", Name: "Architecture Review", Description: "Design maintainable software systems."},
		{ID: "go", Name: "Go"},
	}

	got := FormatCatalog(catalog)
	want := "- id: `architecture-review`, name: \"Architecture Review\" — Design maintainable software systems.\n" +
		"- id: `go`, name: \"Go\""
	if got != want {
		t.Errorf("FormatCatalog() =\n%s\nwant:\n%s", got, want)
	}
}

func TestFormatCatalog_Empty(t *testing.T) {
	if got := FormatCatalog(nil); got != "" {
		t.Errorf("FormatCatalog(nil) = %q, want empty", got)
	}
}

func TestNormalizeID(t *testing.T) {
	cases := map[string]string{
		"go":                  "go",
		"Go Style Guide":      "go-style-guide",
		"architecture-review": "architecture-review",
		"  Weird__Name!! ":    "weird-name",
		"---":                 "",
	}
	for in, want := range cases {
		if got := normalizeID(in); got != want {
			t.Errorf("normalizeID(%q) = %q, want %q", in, got, want)
		}
	}
}
