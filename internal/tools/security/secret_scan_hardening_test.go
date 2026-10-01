package security

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
)

func scanPolicy(ws string) sandbox.Policy {
	return sandbox.Policy{Workspace: ws}
}

// Symlink escapes use the same safe path as read_file: refused, never
// followed, with no outside content scanned.
func TestSecretScan_SymlinkEscapeRefused(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("password = hunter2hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ws, "link.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	sc := NewSecretScanWithPolicy(scanPolicy(ws))
	res, err := sc.Execute(context.Background(), map[string]any{"path": "link.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("symlink scan must be denied, got %q", res.Content)
	}
}

// Cancelled contexts fail fast instead of reading.
func TestSecretScan_CancelledCtx(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "f.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sc := NewSecretScanWithPolicy(scanPolicy(ws))
	res, err := sc.Execute(ctx, map[string]any{"path": "f.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "context canceled") {
		t.Errorf("cancelled scan = %+v, want context-canceled soft error", res)
	}
}

// Oversized inputs are refused before scanning, for both text and files.
func TestSecretScan_OversizedRefused(t *testing.T) {
	sc := NewSecretScanWithPolicy(scanPolicy(t.TempDir()))
	big := strings.Repeat("A", maxScanBytes+1)
	if res, err := sc.Execute(context.Background(), map[string]any{"text": big}); err != nil {
		t.Fatal(err)
	} else if !res.IsError {
		t.Error("oversized inline text must be refused")
	}
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "big.txt"), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	sc2 := NewSecretScanWithPolicy(scanPolicy(ws))
	if res, err := sc2.Execute(context.Background(), map[string]any{"path": "big.txt"}); err != nil {
		t.Fatal(err)
	} else if !res.IsError {
		t.Error("oversized file must be refused")
	}
}

// Normal detection still works through the hardened path (no regression).
func TestSecretScan_DetectionKept(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "k.txt"), []byte("aws key AKIAIOSFODNN7EXAMPLE here"), 0o600); err != nil {
		t.Fatal(err)
	}
	sc := NewSecretScanWithPolicy(scanPolicy(ws))
	res, err := sc.Execute(context.Background(), map[string]any{"path": "k.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "aws-access-key") {
		t.Errorf("detection regressed: %+v", res)
	}
}
