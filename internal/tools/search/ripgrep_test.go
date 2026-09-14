package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

func writeSearchCodeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// requireRg skips the test when ripgrep is not installed, mirroring the
// git/shell tests that skip when their binary is absent.
func requireRg(t *testing.T) {
	t.Helper()
	if _, err := rgBinary("rg"); err != nil {
		t.Skip("rg not on PATH")
	}
}

// stubMissingRg replaces the binary probe with a miss and returns a
// restore function.
func stubMissingRg(t *testing.T) {
	t.Helper()
	old := rgBinary
	rgBinary = func(string) (string, error) { return "", &os.LinkError{Op: "lookpath", Err: os.ErrNotExist} }
	t.Cleanup(func() { rgBinary = old })
}

func TestSearchCode_SchemaShape(t *testing.T) {
	tool := NewSearchCode()
	if tool.Name() != "search_code" {
		t.Fatalf("Name() = %q, want search_code", tool.Name())
	}
	if !strings.Contains(strings.ToLower(tool.Description()), "ripgrep") {
		t.Fatalf("Description should name ripgrep for model routing, got: %q", tool.Description())
	}
	schema := tool.InputSchema()
	rawReq, ok := schema["required"].([]string)
	if !ok || len(rawReq) != 1 || rawReq[0] != "pattern" {
		t.Fatalf("required = %v, want [pattern]", schema["required"])
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties: %v", schema)
	}
	wantTypes := map[string]string{
		"pattern": "string", "path": "string", "include": "string",
		"regex": "boolean", "case_insensitive": "boolean", "hidden": "boolean",
		"max_results": "number", "timeout_seconds": "number",
	}
	for key, wantType := range wantTypes {
		prop, ok := props[key].(map[string]any)
		if !ok {
			t.Fatalf("missing schema property %q", key)
		}
		if prop["type"] != wantType {
			t.Fatalf("property %q type = %v, want %s", key, prop["type"], wantType)
		}
	}
}

func TestSearchCode_StrictValidationRejectsUnknownFields(t *testing.T) {
	tool := NewSearchCode()
	def := tools.Definition{Name: tool.Name(), InputSchema: tool.InputSchema()}
	if err := tools.ValidateArgs(def, map[string]any{"pattern": "x", "flags": "--no-ignore"}); err == nil {
		t.Fatalf("unknown field flags must be rejected")
	}
	if err := tools.ValidateArgs(def, map[string]any{"pattern": "x"}); err != nil {
		t.Fatalf("minimal valid args rejected: %v", err)
	}
}

func TestSearchCode_MetadataAndLimits(t *testing.T) {
	tool := NewSearchCode()
	meta := tools.MetadataOf(tool)
	if !meta.SupportsCancellation || !meta.SupportsParallel || meta.Retryable {
		t.Fatalf("metadata = %+v, want cancellation+parallel, non-retryable", meta)
	}
	if got := tool.ToolLimits(); got.MaxLines != tools.DefaultSearchCodeMaxLines || got.MaxBytes != tools.DefaultSearchCodeMaxBytes {
		t.Fatalf("default limits = %+v", got)
	}
	tool.SetLimits(tools.Limits{MaxLines: 5})
	if got := tool.ToolLimits(); got.MaxLines != 5 || got.MaxBytes != tools.DefaultSearchCodeMaxBytes {
		t.Fatalf("partial override = %+v, want MaxLines 5 with byte default kept", got)
	}
}

func TestSearchCode_LiteralMatch(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, "a.go"), "package main\n// TODO fix this\nfunc main() {}\n")
	writeSearchCodeFile(t, filepath.Join(dir, "b.go"), "package main\nfunc ok() {}\n")

	tool := NewSearchCode()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "TODO", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError: %s", res.Content)
	}
	if !strings.Contains(res.Content, "a.go:2:") {
		t.Fatalf("missing match, got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "b.go") {
		t.Fatalf("false positive, got:\n%s", res.Content)
	}
}

func TestSearchCode_RegexIncludeAndCase(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, "a.go"), "func Foo() {}\n")
	writeSearchCodeFile(t, filepath.Join(dir, "b.txt"), "func Foo() {}\n")

	tool := NewSearchCode()
	res, err := tool.Execute(context.Background(), map[string]any{
		"pattern": `func\s+\w+\(\)`, "path": dir, "regex": true, "include": "*.go",
	})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, "a.go") || strings.Contains(res.Content, "b.txt") {
		t.Fatalf("include glob not honored, got:\n%s", res.Content)
	}

	res, err = tool.Execute(context.Background(), map[string]any{
		"pattern": "FUNC FOO", "path": dir, "case_insensitive": true,
	})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, "a.go") {
		t.Fatalf("case-insensitive miss, got:\n%s", res.Content)
	}
}

func TestSearchCode_NoMatches(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, "a.go"), "package main\n")

	tool := NewSearchCode()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_ABSENT_XYZ", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("no-match must not be an error, got: %s", res.Content)
	}
	if !strings.Contains(res.Content, "no matches") {
		t.Fatalf("want no-matches note, got:\n%s", res.Content)
	}
}

func TestSearchCode_InvalidRegexIsSoftError(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	tool := NewSearchCode()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "([", "path": dir, "regex": true})
	if err != nil {
		t.Fatalf("invalid regex must be soft error, got hard: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "invalid regex") {
		t.Fatalf("want invalid-regex soft error, got:\n%s", res.Content)
	}
}

func TestSearchCode_InvalidGlobIsSoftError(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	tool := NewSearchCode()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "x", "path": dir, "include": "[["})
	if err != nil {
		t.Fatalf("invalid glob must be soft error, got hard: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "invalid glob") {
		t.Fatalf("want invalid-glob soft error, got:\n%s", res.Content)
	}
}

func TestSearchCode_EmptyPatternIsHardError(t *testing.T) {
	tool := NewSearchCode()
	if _, err := tool.Execute(context.Background(), map[string]any{"pattern": "  "}); err == nil {
		t.Fatalf("empty pattern must be a hard error")
	}
	if _, err := tool.Execute(context.Background(), map[string]any{}); err == nil {
		t.Fatalf("missing pattern must be a hard error")
	}
}

func TestSearchCode_WrongTypesAreHardErrors(t *testing.T) {
	tool := NewSearchCode()
	for _, args := range []map[string]any{
		{"pattern": "x", "regex": "yes"},
		{"pattern": "x", "case_insensitive": "true"},
		{"pattern": "x", "hidden": "false"},
		{"pattern": "x", "max_results": "many"},
		{"pattern": "x", "timeout_seconds": "soon"},
		{"pattern": "x", "max_results": 0},
		{"pattern": "x", "max_results": 201},
		{"pattern": "x", "max_results": 2.5},
		{"pattern": "x", "timeout_seconds": 0},
		{"pattern": "x", "timeout_seconds": 301},
	} {
		if _, err := tool.Execute(context.Background(), args); err == nil {
			t.Fatalf("args %v must be a hard error", args)
		}
	}
}

func TestSearchCode_NonDirectoryIsSoftError(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.go")
	writeSearchCodeFile(t, f, "x\n")
	tool := NewSearchCode()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "x", "path": f})
	if err != nil {
		t.Fatalf("file path must be soft error, got hard: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "not a directory") {
		t.Fatalf("want not-a-directory soft error, got:\n%s", res.Content)
	}
}

func TestSearchCode_WSLConfinesRoot(t *testing.T) {
	ws := t.TempDir()
	tool := NewSearchCodeWithPolicy(sandbox.Policy{Mode: sandbox.ModeWSL, Workspace: ws})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "x", "path": `..\..`})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("traversal outside workspace must fail, got:\n%s", res.Content)
	}
}

func TestSearchCode_SkipsSensitiveFiles(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, ".env"), "MARKER_SECRET=hunter2\n")
	writeSearchCodeFile(t, filepath.Join(dir, "app.go"), "MARKER_SECRET=hunter2\n")

	tool := NewSearchCode()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_SECRET", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(res.Content, ".env") {
		t.Fatalf("sensitive .env must be skipped, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "app.go") {
		t.Fatalf("normal file must match, got:\n%s", res.Content)
	}
}

func TestSearchCode_SkipsGitDir(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, ".git", "objects", "x"), "MARKER_GITDATA\n")
	writeSearchCodeFile(t, filepath.Join(dir, "code.go"), "nothing here\n")

	tool := NewSearchCode()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_GITDATA", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "no matches") {
		t.Fatalf(".git must be skipped, got:\n%s", res.Content)
	}
}

func TestSearchCode_HiddenDefaultSkipsDotfiles(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, ".hidden"), "MARKER_HIDDEN\n")
	writeSearchCodeFile(t, filepath.Join(dir, "vis.go"), "nothing\n")

	tool := NewSearchCode()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_HIDDEN", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "no matches") {
		t.Fatalf("hidden files must be skipped by default, got:\n%s", res.Content)
	}

	res, err = tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_HIDDEN", "path": dir, "hidden": true})
	if err != nil || res.IsError {
		t.Fatalf("Execute hidden: %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, ".hidden") {
		t.Fatalf("hidden:true must find dotfiles, got:\n%s", res.Content)
	}
}

func TestSearchCode_SymlinkEscapeSkipped(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	outside := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(outside, "secret.txt"), "MARKER_OUTSIDE\n")
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	tool := NewSearchCode()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_OUTSIDE", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "no matches") {
		t.Fatalf("symlink escape must be skipped, got:\n%s", res.Content)
	}
}

func TestSearchCode_MaxResultsTruncates(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < 150; i++ {
		b.WriteString("MARKER_MANY line\n")
	}
	writeSearchCodeFile(t, filepath.Join(dir, "big.go"), b.String())

	tool := NewSearchCode()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_MANY", "path": dir, "max_results": 10})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "truncated at 10 matches") {
		t.Fatalf("expected truncation marker, got:\n%.500s", res.Content)
	}
	if res.Metadata == nil || res.Metadata["truncated"] != true || res.Metadata["matches"] != 10 {
		t.Fatalf("want truncation metadata, got: %v", res.Metadata)
	}
}

func TestSearchCode_ConfiguredLimitApplies(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < 50; i++ {
		b.WriteString("MARKER_CFG line\n")
	}
	writeSearchCodeFile(t, filepath.Join(dir, "big.go"), b.String())

	tool := NewSearchCode()
	tool.SetLimits(tools.Limits{MaxLines: 3})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_CFG", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "truncated at 3 matches") {
		t.Fatalf("expected configured-limit truncation, got:\n%.500s", res.Content)
	}
}

func TestSearchCode_MissingRgFallsBack(t *testing.T) {
	stubMissingRg(t)
	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, "a.go"), "package main\n// TODO fallback\n")

	tool := NewSearchCode()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "TODO", "path": dir})
	if err != nil {
		t.Fatalf("fallback must not hard-fail, got: %v", err)
	}
	if res.IsError {
		t.Fatalf("fallback found a match yet reports error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "rg not found on PATH") {
		t.Fatalf("fallback must note provenance, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "a.go:") {
		t.Fatalf("fallback must return walker matches, got:\n%s", res.Content)
	}
	if res.Metadata == nil || res.Metadata["fallback"] != true || res.Metadata["rg_used"] != false {
		t.Fatalf("want fallback metadata, got: %v", res.Metadata)
	}
}

func TestSearchCode_CancelledContextIsSoftError(t *testing.T) {
	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, "a.go"), "x\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tool := NewSearchCode()
	res, err := tool.Execute(ctx, map[string]any{"pattern": "x", "path": dir})
	if err != nil {
		t.Fatalf("cancellation must be soft, got hard: %v", err)
	}
	if !res.IsError || !strings.Contains(strings.ToLower(res.Content), "cancell") {
		t.Fatalf("want cancellation soft error, got:\n%s", res.Content)
	}
}

func TestSearchCode_TimeoutFires(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, "a.go"), "x\n")

	tool := NewSearchCode()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "x", "path": dir, "timeout_seconds": 0.000000001})
	if err != nil {
		t.Fatalf("timeout must be soft, got hard: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "timed out") {
		t.Fatalf("want timeout soft error, got:\n%s", res.Content)
	}
}

func TestSearchCode_BuildArgv(t *testing.T) {
	argv := buildRgArgv("foo", "", false, false, false)
	joined := strings.Join(argv, " ")
	for _, want := range []string{"--vimgrep", "--color never", "--path-separator /", "-F", "-e foo", "-- .", "!.git/**", "!node_modules/**", "!*.lock", "--max-filesize 512K"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv %q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "-i") || strings.Contains(joined, "--hidden") {
		t.Fatalf("default argv must not set -i/--hidden: %q", joined)
	}

	argv = buildRgArgv("-leading-dash", "*.go", true, true, true)
	joined = strings.Join(argv, " ")
	if strings.Contains(joined, " -F ") && !strings.Contains(joined, "-e -leading-dash") {
		t.Fatalf("regex argv wrong: %q", joined)
	}
	for _, want := range []string{"-i", "--hidden", "--glob *.go", "-e -leading-dash"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv %q missing %q", joined, want)
		}
	}
	// The pattern must travel as an -e value, never as a bare positional.
	for i, a := range argv {
		if a == "-leading-dash" && (i == 0 || argv[i-1] != "-e") {
			t.Fatalf("pattern must follow -e: %q", joined)
		}
	}
}

func TestSearchCode_RgEnvStripsConfigInjection(t *testing.T) {
	t.Setenv("RIPGREP_CONFIG_PATH", `C:\evil\config`)
	t.Setenv("RIPGREP_CONFIG_FOO", "x")
	t.Setenv("SEARCHCODE_SENTINEL", "keepme")
	env := rgEnv()
	for _, kv := range env {
		if strings.HasPrefix(kv, "RIPGREP_CONFIG") {
			t.Fatalf("rg env must not carry config injection: %q", kv)
		}
	}
	found := false
	for _, kv := range env {
		if kv == "SEARCHCODE_SENTINEL=keepme" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rg env must pass through the host environment")
	}
}

func TestSearchCode_ParseVimgrep(t *testing.T) {
	m, ok := parseVimgrepLine("./a.go:12:4:hello: world\n")
	if !ok || m.path != "./a.go" || m.line != 12 || m.col != 4 || m.text != "hello: world" {
		t.Fatalf("parse = %+v,%v", m, ok)
	}
	// Binary-match notices and malformed lines are dropped.
	for _, raw := range []string{
		"binary file matches (found \"\\0\" byte around offset 10)\n",
		"no-colons-here\n",
		"a.go:notnum:1: x\n",
		":1:1: x\n",
	} {
		if _, ok := parseVimgrepLine(raw); ok {
			t.Fatalf("line %q must not parse", raw)
		}
	}
}

func TestSearchCode_ModelVisibleDefinition(t *testing.T) {
	mgr := tools.NewManager(tools.NewRegistry())
	if err := mgr.Register(NewSearchCode()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	found := false
	for _, d := range mgr.Definitions() {
		if d.Name != "search_code" {
			continue
		}
		found = true
		if err := tools.ValidateArgs(d, map[string]any{"pattern": "x", "regex": true}); err != nil {
			t.Fatalf("definition must accept typed args: %v", err)
		}
	}
	if !found {
		t.Fatalf("search_code missing from Definitions")
	}

	// End-to-end through the normal dispatch path.
	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, "a.go"), "MARKER_DISPATCH\n")
	res, err := mgr.Execute(context.Background(), "search_code", map[string]any{"pattern": "MARKER_DISPATCH", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError || !strings.Contains(res.Content, "a.go:") {
		t.Fatalf("dispatch failed, got:\n%s", res.Content)
	}
}
