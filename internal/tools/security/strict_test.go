package security

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
)

// TestSecretScan_StrictConfinesRoot pins the same boundary for secret
// scanning: traversal and absolute-outside fail, inside works.
func TestSecretScan_StrictConfinesRoot(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "app.go"), []byte("x := 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewSecretScanWithPolicy(sandbox.Policy{Mode: sandbox.ModeNative, Workspace: ws, Strict: true})

	res, err := tool.Execute(context.Background(), map[string]any{"path": "app.go"})
	if err != nil || res.IsError {
		t.Fatalf("inside scan = %+v err=%v, want success", res, err)
	}
	if res, err := tool.Execute(context.Background(), map[string]any{"path": `..\..`}); err != nil || !res.IsError {
		t.Fatalf("traversal scan = %+v err=%v, want denial", res, err)
	}
	outside := filepath.Join(t.TempDir(), "o.go")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, err := tool.Execute(context.Background(), map[string]any{"path": outside}); err != nil || !res.IsError {
		t.Fatalf("outside scan = %+v err=%v, want denial", res, err)
	}
	// Inline text still scans (no path involved).
	res, err = tool.Execute(context.Background(), map[string]any{"text": "nothing here"})
	if err != nil || res.IsError {
		t.Fatalf("text scan = %+v err=%v, want success", res, err)
	}
	if !strings.Contains(res.Content, "no hardcoded secrets") {
		t.Errorf("text scan = %q", res.Content)
	}
}
