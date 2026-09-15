package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCheckBoundary_AcceptsInsidePaths pins the pre-flight contract for
// the read/write/list tools: inside-workspace spellings resolve to a
// canonical absolute path under the workspace, including absolute
// inside paths (legitimate absolute paths keep working).
func TestCheckBoundary_AcceptsInsidePaths(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "sub", "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := map[string]interface {
		CheckBoundary(map[string]any) (string, error)
	}{
		"read_file":  NewReadFileWithPolicy(strictPolicy(ws)),
		"write_file": NewWriteFileWithPolicy(strictPolicy(ws)),
		"list_files": NewListFilesWithPolicy(strictPolicy(ws)),
	}
	for name, tool := range tools {
		for _, req := range []string{"sub", "sub/note.txt", filepath.Join(ws, "sub")} {
			resolved, err := tool.CheckBoundary(map[string]any{"path": req})
			if err != nil {
				t.Errorf("%s CheckBoundary(%q) = %v, want acceptance", name, req, err)
				continue
			}
			// The contract is inside-ness, not spelling: compare
			// case-insensitively against the canonical workspace so
			// the test holds on hosts with aliased temp paths (8.3
			// short names, /var vs /private/var).
			assertWithin(t, name, req, ws, resolved)
		}
	}
	// Nonexistent inside paths: creation-aware write accepts, while
	// existence-requiring read/list refuse (the file is not there to open).
	if _, err := tools["write_file"].CheckBoundary(map[string]any{"path": "new/note2.txt"}); err != nil {
		t.Errorf("write CheckBoundary(new/note2.txt) = %v, want acceptance", err)
	}
	for _, name := range []string{"read_file", "list_files"} {
		if _, err := tools[name].CheckBoundary(map[string]any{"path": "new/note2.txt"}); err == nil {
			t.Errorf("%s CheckBoundary(new/note2.txt) succeeded, want refusal for the missing path", name)
		}
	}
}

// assertWithin reports a test failure unless resolved is an absolute
// path equal to or underneath the canonical workspace root,
// compared case-insensitively so aliased host spellings pass.
func assertWithin(t *testing.T, tool, req, ws, resolved string) {
	t.Helper()
	if !filepath.IsAbs(resolved) {
		t.Errorf("%s CheckBoundary(%q) = %q, want an absolute path", tool, req, resolved)
		return
	}
	cleanWS, err := filepath.EvalSymlinks(ws)
	if err != nil {
		cleanWS = ws
	}
	cleanWS = filepath.Clean(cleanWS)
	cleanResolved := filepath.Clean(resolved)
	same := strings.EqualFold(cleanWS, cleanResolved)
	under := strings.HasPrefix(strings.ToLower(cleanResolved), strings.ToLower(cleanWS+string(filepath.Separator)))
	if !same && !under {
		t.Errorf("%s CheckBoundary(%q) = %q, want a path inside %q", tool, req, resolved, cleanWS)
	}
}

// TestCheckBoundary_RejectsOutsidePaths pins that the pre-flight denies
// every escape shape without touching the filesystem.
func TestCheckBoundary_RejectsOutsidePaths(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	tools := map[string]interface {
		CheckBoundary(map[string]any) (string, error)
	}{
		"read_file":  NewReadFileWithPolicy(strictPolicy(ws)),
		"write_file": NewWriteFileWithPolicy(strictPolicy(ws)),
		"list_files": NewListFilesWithPolicy(strictPolicy(ws)),
	}
	requests := []string{
		"..",
		"sub/../../..",
		outside,
		filepath.Join(outside, "f.txt"),
		ws + "-evil",
	}
	for name, tool := range tools {
		for _, req := range requests {
			if _, err := tool.CheckBoundary(map[string]any{"path": req}); err == nil {
				t.Errorf("%s CheckBoundary(%q) succeeded, want rejection", name, req)
			}
		}
	}
}

// TestWriteFile_PosixAbsoluteDeniedWithoutSideEffects is the regression
// test for the reported incident: a model passing the POSIX absolute
// path "/go/main.go" on Windows must be rejected before any directory
// is created or file is written. The probe name is unique per process
// so the absence assertion cannot pass vacuously on a machine that
// happens to have a real /go tree.
func TestWriteFile_PosixAbsoluteDeniedWithoutSideEffects(t *testing.T) {
	ws := t.TempDir()
	wf := NewWriteFileWithPolicy(strictPolicy(ws))

	// A fixed POSIX-absolute shape documents the incident; both it and
	// the unique probe below must be denied with no side effects.
	for _, req := range []string{string(filepath.Separator) + "go" + string(filepath.Separator) + "main.go"} {
		res, err := wf.Execute(context.Background(), map[string]any{"path": req, "content": "x"})
		if err != nil {
			t.Fatalf("Execute(%q) returned a Go error = %v, want a domain denial", req, err)
		}
		if !res.IsError {
			t.Fatalf("Execute(%q) succeeded, want workspace denial", req)
		}
		if !strings.Contains(res.Content, "workspace") && !strings.Contains(res.Content, "outside") {
			t.Errorf("denial for %q should name the boundary, got %q", req, res.Content)
		}
	}

	unique := string(filepath.Separator) + "ff-probe-ws-escape-check.txt"
	res, err := wf.Execute(context.Background(), map[string]any{"path": unique, "content": "x"})
	if err != nil || !res.IsError {
		t.Fatalf("Execute(%q) = %+v err=%v, want denial", unique, res, err)
	}
	// Nothing may have been created at any spelling the OS could have
	// given the request: neither under the workspace nor at the
	// filesystem location the raw path names.
	if _, err := os.Stat(filepath.Join(ws, "ff-probe-ws-escape-check.txt")); !os.IsNotExist(err) {
		t.Errorf("denied write leaked into the workspace: %v", err)
	}
	if _, err := os.Stat(unique); !os.IsNotExist(err) {
		t.Errorf("denied write reached the raw path %q (stat err = %v)", unique, err)
	}
}

// TestStrict_WindowsEscapeShapesDenied runs only on Windows, where
// drive-letter and UNC paths are real: every out-of-workspace shape
// must be denied by the tools (not just by the sandbox unit tests),
// and denied writes must create nothing.
func TestStrict_WindowsEscapeShapesDenied(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drive-letter and UNC escapes need a Windows host")
	}
	ws := t.TempDir()
	wf := NewWriteFileWithPolicy(strictPolicy(ws))
	rf := NewReadFileWithPolicy(strictPolicy(ws))

	vol := filepath.VolumeName(ws)
	other := `D:`
	if vol == `D:` {
		other = `C:`
	}
	probe := other + `\ff-probe-ws-escape-check.txt`
	requests := []string{
		`/go/main.go`, // the reported incident: POSIX absolute on Windows
		`C:foo`,       // drive-relative: ambiguous, always invalid
		`C:`,          // bare drive letter: always invalid
		probe,         // absolute path outside the workspace
		`\\127.0.0.1\ff-probe-ws-escape-check\x.txt`, // UNC: never inside a local workspace
	}
	for _, req := range requests {
		if _, err := wf.CheckBoundary(map[string]any{"path": req}); err == nil {
			t.Errorf("write CheckBoundary(%q) succeeded, want rejection", req)
		}
		res, err := wf.Execute(context.Background(), map[string]any{"path": req, "content": "x"})
		if err != nil || !res.IsError {
			t.Errorf("write Execute(%q) = %+v err=%v, want denial", req, res, err)
		}
		readRes, err := rf.Execute(context.Background(), map[string]any{"path": req})
		if err != nil {
			t.Errorf("read Execute(%q) returned a Go error = %v, want a domain denial", req, err)
		} else if !readRes.IsError {
			t.Errorf("read Execute(%q) succeeded, want denial", req)
		}
	}
	if _, err := os.Stat(probe); !os.IsNotExist(err) {
		t.Errorf("denied write created %q (stat err = %v)", probe, err)
	}
	if _, err := os.Stat(`C:\go\main.go`); err == nil {
		t.Logf("note: C:\\go\\main.go exists on this machine; the denials above prove this run created nothing there")
	}
}

// TestCheckBoundary_PropagatesArgErrors pins fail-closed argument
// handling: a missing path is a hard error, never an implicit ".".
func TestCheckBoundary_PropagatesArgErrors(t *testing.T) {
	ws := t.TempDir()
	if _, err := NewReadFileWithPolicy(strictPolicy(ws)).CheckBoundary(map[string]any{}); err == nil {
		t.Error("CheckBoundary with missing path must fail, not default")
	}
	if _, err := NewWriteFileWithPolicy(strictPolicy(ws)).CheckBoundary(map[string]any{"path": 123}); err == nil {
		t.Error("CheckBoundary with a non-string path must fail")
	}
}
