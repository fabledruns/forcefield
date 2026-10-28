package search

// White-box pin for the Z3 branch: ripgrep exits 1 ("no matches") while the
// capture bound fired on its begin/end byte stream (huge tree, all begin/end
// events, zero matches). No deterministic real-rg fixture can produce that
// shape — rg only emits >cap bytes by writing begin/end events per file, and
// a .cmd script cannot stand in for rg because modern os/exec refuses batch
// files on Windows — so this test builds a minimal stand-in executable and
// selects it through the rgBinary seam, exercising the real Execute → runRg
// exit handling (exec spawn, Wait, ExitError classification, exit-1 cut
// note) rather than calling the assembly by hand.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// buildFakeRg writes a fake rg that floods stdout (beyond any small reader
// cap) and exits 1 — exactly the Z3 shape (zero matches reported via exit
// code, plentiful pre-summary byte stream) — and compiles it into the test
// directory. The tool never parses stdout on the exit-1 path, so the flood
// bytes need no JSON shape.
func buildFakeRg(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "farg_flood.go")
	const program = `package main

import "os"

func main() {
	os.Stdout.Write(make([]byte, 8192))
	os.Exit(1)
}
`
	if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
		t.Fatalf("fake rg source: %v", err)
	}
	exe := filepath.Join(t.TempDir(), "rg")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	build := exec.Command("go", "build", "-o", exe, src)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake rg: %v\n%s", err, out)
	}
	return exe
}

// TestSearchCode_Z3ExitOneWithCutNamesCompletion pins the huge-tree zero-match
// shape: rg exits 1 AND the capture bound fired, so the zero-match note gains
// the read_file escape hatch and terminates with a second line stating the
// bound, the bytes produced, and that the search itself ran to completion —
// the search ran; only the report was capped.
func TestSearchCode_Z3ExitOneWithCutNamesCompletion(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("fake-rg build requires go on PATH")
	}
	exe := buildFakeRg(t)
	prev := rgBinary
	rgBinary = func(string) (string, error) { return exe, nil }
	t.Cleanup(func() { rgBinary = prev })

	dir := t.TempDir()
	writeSearchCodeFile(t, filepath.Join(dir, "tree.go"), "package tree\n")

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	// The fake rg writes 8192 bytes; a 1024-byte cap splits them exactly
	// like a real huge-tree begin/end stream would.
	tool.SetLimits(tools.Limits{MaxBytes: 1024})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "X", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("exit-1 zero-match with cut is a success shape, got IsError: %s", res.Content)
	}
	if !strings.Contains(res.Content, `no matches for "X" under`) {
		t.Fatalf("zero-match wording lost, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "(files over 512 KiB are not searched; if a match may be inside one, use read_file)") {
		t.Fatalf("zero-match note must keep the read_file escape hatch, got:\n%s", res.Content)
	}
	want := fmt.Sprintf("\n[search truncated: rg output capped at %d bytes, %d bytes produced; the search itself ran to completion; narrow pattern, path, or include glob]", 1024, 8192)
	if !strings.Contains(res.Content, want) {
		t.Fatalf("exit-1 + cut must name the completion on a second line, got:\n%s", res.Content)
	}
	if res.Metadata != nil {
		t.Fatalf("zero-row branches keep Metadata nil, got %v", res.Metadata)
	}
}
