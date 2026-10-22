package search

import (
	"context"
	"fmt"
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
	// Schema-only: no filesystem use, so no workspace fixture needed.
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
	// Validation-only: no filesystem use, so no workspace fixture needed.
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
	// Metadata-only: no filesystem use, so no workspace fixture needed.
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

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
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

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
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

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
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
	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
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
	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "x", "path": dir, "include": "[["})
	if err != nil {
		t.Fatalf("invalid glob must be soft error, got hard: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "invalid glob") {
		t.Fatalf("want invalid-glob soft error, got:\n%s", res.Content)
	}
}

func TestSearchCode_EmptyPatternIsHardError(t *testing.T) {
	// Pattern validation fails before any path resolution, so no
	// workspace fixture is needed here.
	tool := NewSearchCode()
	if _, err := tool.Execute(context.Background(), map[string]any{"pattern": "  "}); err == nil {
		t.Fatalf("empty pattern must be a hard error")
	}
	if _, err := tool.Execute(context.Background(), map[string]any{}); err == nil {
		t.Fatalf("missing pattern must be a hard error")
	}
}

func TestSearchCode_WrongTypesAreHardErrors(t *testing.T) {
	// Argument validation fails before any path resolution, so no
	// workspace fixture is needed here.
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
	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
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

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
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

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
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

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
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

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
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

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
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

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
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

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
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

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
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

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
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
	for _, want := range []string{"--json", "--color never", "--path-separator /", "-F", "-e foo", "-- .", "!.git/**", "!node_modules/**", "!*.lock", "--max-filesize 512K"} {
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

// --- parser-level: rg --json edge shapes the fixed parser must keep ---

// A CRLF line's trailing \r is part of rg's line content: submatches
// may span it, and both the span validation and the reported column
// are relative to that un-trimmed content. The display text keeps
// vimgrep parity by trimming exactly the trailing \r. The parser used
// to validate spans against the \r-trimmed display text, which
// dropped every row whose match touched the terminator.
func TestSearchCode_ParseRgJSON_CRLFTrailingCRKept(t *testing.T) {
	// "abcd\r\n": content is "abcd\r" (5 bytes) after the \n trim; the
	// match covering one \r byte reports col 5 / matchLen 1 and the
	// display text without the \r.
	rows := parseRgJSONLine(`{"type":"match","data":{"path":{"text":"./cr.go"},"line_number":1,` +
		`"lines":{"text":"abcd\r\n"},"submatches":[{"start":4,"end":5}]}}`)
	if len(rows) != 1 || rows[0].line != 1 || rows[0].col != 5 || rows[0].matchLen != 1 || rows[0].text != "abcd" {
		t.Fatalf("submatch covering the trailing \\r = %+v, want col 5 matchLen 1 text %q", rows, "abcd")
	}
	// A whole-line match ending on the terminator \r: content is
	// "endline\r" (8 bytes), so end 8 is valid, not out of range.
	rows = parseRgJSONLine(`{"type":"match","data":{"path":{"text":"./cr.go"},"line_number":1,` +
		`"lines":{"text":"endline\r\n"},"submatches":[{"start":0,"end":8}]}}`)
	if len(rows) != 1 || rows[0].col != 1 || rows[0].matchLen != 8 || rows[0].text != "endline" {
		t.Fatalf("match ending on trailing \\r = %+v, want matchLen 8", rows)
	}
	// The content-space guard keeps working: 6 > len("abcd\r").
	rows = parseRgJSONLine(`{"type":"match","data":{"path":{"text":"./cr.go"},"line_number":1,` +
		`"lines":{"text":"abcd\r\n"},"submatches":[{"start":4,"end":6}]}}`)
	if len(rows) != 0 {
		t.Fatalf("submatch past the line content must still drop, got %+v", rows)
	}
	// Zero-width parity on a no-CR line: the end-of-content position
	// stays valid, exactly as before this fix.
	rows = parseRgJSONLine(`{"type":"match","data":{"path":{"text":"./zw.go"},"line_number":1,` +
		`"lines":{"text":"abcd"},"submatches":[{"start":4,"end":4}]}}`)
	if len(rows) != 1 || rows[0].col != 5 || rows[0].matchLen != 0 || rows[0].text != "abcd" {
		t.Fatalf("zero-width submatch at content end = %+v, want col 5 matchLen 0", rows)
	}
}

// When the line content is not valid UTF-8, rg omits lines.text and
// sends lines.bytes instead: the base64 encoding of the full line
// (terminator included). The parser must decode it. Previously these
// events silently produced no rows at all.
func TestSearchCode_ParseRgJSON_Base64LineBytes(t *testing.T) {
	// base64("bad\xff\n") — LF line.
	rows := parseRgJSONLine(`{"type":"match","data":{"path":{"text":"./nu.go"},"line_number":1,` +
		`"lines":{"bytes":"YmFk/wo="},"submatches":[{"start":0,"end":3}]}}`)
	if len(rows) != 1 || rows[0].col != 1 || rows[0].matchLen != 3 || rows[0].text != "bad\xff" {
		t.Fatalf("base64 line (LF) = %+v, want col 1 matchLen 3 text bad\\xff", rows)
	}
	// base64("bad\xff\r\n") — CRLF line: both terminator bytes go and
	// the row still lands.
	rows = parseRgJSONLine(`{"type":"match","data":{"path":{"text":"./nu.go"},"line_number":1,` +
		`"lines":{"bytes":"YmFk/w0K"},"submatches":[{"start":0,"end":3}]}}`)
	if len(rows) != 1 || rows[0].col != 1 || rows[0].matchLen != 3 || rows[0].text != "bad\xff" {
		t.Fatalf("base64 line (CRLF) = %+v, want text bad\\xff with the \\r\\n stripped", rows)
	}
}

// A path may also arrive as base64 bytes (invalid-UTF-8 filenames).
func TestSearchCode_ParseRgJSON_PathBytesFallback(t *testing.T) {
	// bytes("ab.go")
	rows := parseRgJSONLine(`{"type":"match","data":{"path":{"bytes":"YWIuZ28="},"line_number":2,` +
		`"lines":{"text":"MARKVAR\n"},"submatches":[{"start":0,"end":7}]}}`)
	if len(rows) != 1 || rows[0].path != "ab.go" || rows[0].line != 2 || rows[0].text != "MARKVAR" {
		t.Fatalf("path bytes fallback = %+v, want path ab.go line 2", rows)
	}
}

// --- Execute-level: rg output shapes the parser relies on ---

// rgRowCount counts output rows for one path prefix, one row per
// reported match.
func rgRowCount(content, pathPrefix string) int {
	n := 0
	for _, ln := range strings.Split(content, "\n") {
		if strings.HasPrefix(ln, pathPrefix) {
			n++
		}
	}
	return n
}

// End-of-line whitespace patterns on CRLF files: rg keeps the trailing
// \r in the line content, so \r?$, \s+$ and \s*$ all match one byte
// per line (cols 5, 5, 8) and every row survives the parser. Today
// each query collapses to "no matches" because the parser validates
// against the \r-trimmed text.
func TestSearchCode_CRLFLineEndMatches(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, "crlf.txt"), "abcd\r\nabab\r\nendline\r\n")
	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	for _, pattern := range []string{`\r?$`, `\s+$`, `\s*$`} {
		res, err := tool.Execute(context.Background(), map[string]any{"pattern": pattern, "path": dir, "regex": true})
		if err != nil || res.IsError {
			t.Fatalf("Execute `%s`: %v %v", pattern, err, res.Content)
		}
		for _, want := range []string{"crlf.txt:1:5: abcd", "crlf.txt:2:5: abab", "crlf.txt:3:8: endline"} {
			if !strings.Contains(res.Content, want) {
				t.Fatalf("pattern %s: missing row %q, got:\n%s", pattern, want, res.Content)
			}
		}
		if got := rgRowCount(res.Content, "crlf.txt:"); got != 3 {
			t.Fatalf("pattern %s: %d rows, want 3:\n%s", pattern, got, res.Content)
		}
	}

	// The LF twin has no trailing whitespace, so \s+$ matches nothing.
	writeSearchCodeFile(t, filepath.Join(dir, "lf.txt"), "abcd\nabab\nendline\n")
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": `\s+$`, "path": dir, "include": "lf.txt", "regex": true})
	if err != nil || res.IsError {
		t.Fatalf("Execute lf twin: %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, fmt.Sprintf("no matches for %q", `\s+$`)) {
		t.Fatalf("LF twin must find no trailing whitespace, got:\n%s", res.Content)
	}
}

// A pattern involving the CR byte. Regex mode, sent verbatim: rg
// receives the full regex "d\r" (TrimSpace is only the empty-pattern
// gate in parseSearchCodeArgs, never a rewriter of the pattern),
// and matches the trailing carriage return itself — span
// {3,5}, col 4 — which is exactly a match inside the \r region of
// rg's content space. Under the pre-fix parser this row was dropped
// wholesale, because that span was validated against the \r-trimmed
// display text instead. A mid-line CR in content ("a\rb", here via the
// \x0d escape so no terminator byte rides in the argv) is ordinary
// mid-line content and always worked; asserted here as parity.
func TestSearchCode_CRLFPatternInvolvingCR(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, "crlf.txt"), "abcd\r\nabab\r\nendline\r\n")
	writeSearchCodeFile(t, filepath.Join(dir, "cr.txt"), "a\rb\n")
	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "d\r", "path": dir, "regex": true})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, "crlf.txt:1:4: abcd") {
		t.Fatalf("missing crlf.txt:1:4: abcd (terminator byte must stay valid):\n%s", res.Content)
	}
	res, err = tool.Execute(context.Background(), map[string]any{"pattern": "a\\x0db", "path": dir, "include": "cr.txt", "regex": true})
	if err != nil || res.IsError {
		t.Fatalf("Execute a\\x0db: %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, "cr.txt:1:1: a\rb") {
		t.Fatalf("missing cr.txt:1:1: a\\rb:\n%s", res.Content)
	}
}

// A matched line whose content is not valid UTF-8 arrives from rg as
// base64 lines.bytes. Previously the tool reported plain "no matches"
// for such files; the decoded bytes make the match arrive.
func TestSearchCode_InvalidUTF8LineSearched(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, "nonutf8.txt"), "bad\xff\n")
	writeSearchCodeFile(t, filepath.Join(dir, "nonul.txt"), "bad\xff") // no-EOL twin
	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	for _, f := range []string{"nonutf8.txt", "nonul.txt"} {
		res, err := tool.Execute(context.Background(), map[string]any{"pattern": "bad", "path": dir, "include": f})
		if err != nil || res.IsError {
			t.Fatalf("Execute %s: %v %v", f, err, res.Content)
		}
		if !strings.Contains(res.Content, f+":1:1:") || !strings.Contains(res.Content, "bad\xff") {
			t.Fatalf("%s: missing row with the raw bytes, got:\n%s", f, res.Content)
		}
	}
}

// Zero-length matches (x* on a line without x) are reported once per
// byte position of the line content, including one past the last
// byte. On CRLF content the \r is a real byte, so the position after
// it (cols 6, 6, 9) is a legitimate row; the parser used to drop it.
func TestSearchCode_ZeroWidthMatchesCRLF(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, "crlf.txt"), "abcd\r\nabab\r\nendline\r\n")
	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "x*", "path": dir, "regex": true})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	// 6 + 6 + 9 positions (content bytes + 1, per line).
	if got := rgRowCount(res.Content, "crlf.txt:"); got != 21 {
		t.Fatalf("zero-width rows = %d, want 21 (trailing \\r positions restored):\n%s", got, res.Content)
	}
	for _, want := range []string{"crlf.txt:1:6: abcd", "crlf.txt:2:6: abab", "crlf.txt:3:9: endline"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("missing restored trailing zero-width %q:\n%s", want, res.Content)
		}
	}
}

// The LF twin: no \r, so the per-line counts were already right.
// Parity: 5 + 5 + 8 rows with the last zero-width at cols 5, 5, 8.
func TestSearchCode_ZeroWidthMatchesLF(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, "lf.txt"), "abcd\nabab\nendline\n")
	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "x*", "path": dir, "regex": true})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	if got := rgRowCount(res.Content, "lf.txt:"); got != 18 {
		t.Fatalf("zero-width rows = %d, want 18:\n%s", got, res.Content)
	}
	for _, want := range []string{"lf.txt:1:5: abcd", "lf.txt:2:5: abab", "lf.txt:3:8: endline"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("missing final zero-width %q:\n%s", want, res.Content)
		}
	}
}

// Grouped submatches of one match event (an alternation hitting twice
// on one line) each produce their own row: line 3 "axby" yields rows
// at cols 2 and 4. Parity shape (already worked, must keep working),
// and it exercises one-event-multiple-rows accounting.
func TestSearchCode_AlternationSubmatchRows(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, "alt.txt"), "x\ny\naxby\n")
	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "x|y", "path": dir, "regex": true})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	for _, want := range []string{"alt.txt:1:1: x", "alt.txt:2:1: y", "alt.txt:3:2: axby", "alt.txt:3:4: axby"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("missing %q:\n%s", want, res.Content)
		}
	}
	if got := rgRowCount(res.Content, "alt.txt:"); got != 4 {
		t.Fatalf("rows = %d, want 4:\n%s", got, res.Content)
	}
}

// A long CRLF line's $ lands on the trailing \r at col 2207 (the
// 2206-byte line plus its \r). Today that row is dropped and only
// line 1's zero-width row is left. All three rows must arrive, the
// long line elided with its head marked but its TTLEND tail visible
// and unmarked (nothing was dropped there).
func TestSearchCode_LongCRLFLineEndRows(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, "longcrlf.txt"),
		strings.Repeat("a", 2200)+"\n"+strings.Repeat("b", 2200)+"TTLEND\r\nSHORT\r\n")
	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": `\r?$`, "path": dir, "regex": true})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	if got := rgRowCount(res.Content, "longcrlf.txt:"); got != 3 {
		t.Fatalf("rows = %d, want 3 (line 2's trailing-CR match was being lost):\n%s", got, res.Content)
	}
	if !strings.Contains(res.Content, "longcrlf.txt:1:2201:") {
		t.Fatalf("missing line-1 zero-width row:\n%.300s", res.Content)
	}
	if !strings.Contains(res.Content, "longcrlf.txt:3:6: ") {
		t.Fatalf("missing line-3 CR-position row (col 6 = 'SHORT'+\\r):\n%s", res.Content)
	}
	for _, ln := range strings.Split(strings.TrimSpace(res.Content), "\n") {
		if !strings.HasPrefix(ln, "longcrlf.txt:2:2207: ") {
			continue
		}
		if strings.HasSuffix(ln, elideTail) {
			t.Fatalf("long line's dropped tail is unmarked (nothing past the \\r was cut):\n…%.80s", ln[len(ln)-80:])
		}
		if !strings.Contains(ln, "TTLEND") {
			t.Fatalf("long line's TTLEND tail not visible:\n…%.80s", ln[len(ln)-80:])
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

func TestSearchCode_ParseRgJSON(t *testing.T) {
	rows := parseRgJSONLine(`{"type":"match","data":{"path":{"text":"./a.go"},"line_number":12,` +
		`"absolute_offset":100,"lines":{"text":"hello: world\n"},"submatches":[{"match":{"text":"hi"},"start":4,"end":6}]}}`)
	if len(rows) != 1 {
		t.Fatalf("submatch rows = %d, want 1", len(rows))
	}
	m := rows[0]
	if m.path != "./a.go" || m.line != 12 || m.col != 5 || m.matchLen != 2 || m.text != "hello: world" {
		t.Fatalf("parse = %+v (the trailing newline must be trimmed)", m)
	}
	// A grouped match yields one row per submatch, each with its own span.
	rows = parseRgJSONLine(`{"type":"match","data":{"path":{"text":"./b.go"},"line_number":1,"lines":{"text":"abc"},` +
		`"submatches":[{"match":{"text":"a"},"start":0,"end":1},{"match":{"text":"c"},"start":2,"end":3}]}}`)
	if len(rows) != 2 || rows[0].col != 1 || rows[1].col != 3 || rows[1].matchLen != 1 || rows[1].line != 1 {
		t.Fatalf("grouped submatches = %+v", rows)
	}
	// Non-match events and malformed shapes are dropped, exactly like
	// --vimgrep's unparsable lines were.
	for _, raw := range []string{
		`{"type":"begin","data":{"path":{"text":"./c.go"}}}`,
		`{"type":"end","data":{"binary":true}}`,
		`{"type":"summary","data":{}}`,
		"not json at all",
		`{"type":"match","data":{}}`,
		`{"type":"match","data":{"line_number":0,"lines":{"text":"x"},"submatches":[{"start":0,"end":1}]}}`,
		`{"type":"match","data":{"path":{"text":""},"line_number":1,"lines":{"text":"x"},"submatches":[{"start":0,"end":1}]}}`,
		`{"type":"match","data":{"path":{"text":"./d.go"},"line_number":1,"lines":{"text":"abc"},"submatches":[{"start":3,"end":2}]}}`,
		`{"type":"match","data":{"path":{"text":"./d.go"},"line_number":1,"lines":{"text":"abc"},"submatches":[{"start":-1,"end":1}]}}`,
		`{"type":"match","data":{"path":{"text":"./d.go"},"line_number":1,"lines":{"text":"abc"},"submatches":[{"start":0,"end":9}]}}`,
	} {
		if rows := parseRgJSONLine(raw); len(rows) != 0 {
			t.Fatalf("line %s must not parse, got %+v", raw, rows)
		}
	}
}

func TestSearchCode_ModelVisibleDefinition(t *testing.T) {
	// End-to-end through the normal dispatch path, confined to the
	// fixture directory like every other filesystem test.
	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, "a.go"), "MARKER_DISPATCH\n")
	mgr := tools.NewManager(tools.NewRegistry())
	if err := mgr.Register(NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})); err != nil {
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
	res, err := mgr.Execute(context.Background(), "search_code", map[string]any{"pattern": "MARKER_DISPATCH", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError || !strings.Contains(res.Content, "a.go:") {
		t.Fatalf("dispatch failed, got:\n%s", res.Content)
	}
}
