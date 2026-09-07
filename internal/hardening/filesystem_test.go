package hardening

import (
	"os"
	"path/filepath"
	"testing"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools/filesystem"
)

// P1.15 lab: filesystem boundary proofs. Lexical checks are not isolation;
// these tests pin what each mode actually enforces.
func TestReadFileStrictConfinesTraversal(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("topsecret"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := sandbox.Policy{Mode: sandbox.ModeNative, Workspace: root, Strict: true}
	tool := filesystem.NewReadFileWithPolicy(policy)
	res, err := tool.Execute(t.Context(), map[string]any{"path": secret})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("strict read_file escaped workspace: %q", res.Content)
	}
}

func TestReadFileNativePermissiveDocumented(t *testing.T) {
	// Native/permissive is historical unrestricted behavior by design.
	// This test documents (not fixes) that absolute paths are readable.
	// P1.16 must make this explicit in docs + sensitive-path escalation.
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := filesystem.NewReadFile()
	res, err := tool.Execute(t.Context(), map[string]any{"path": p})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.IsError || res.Content != "hello" {
		t.Fatalf("native read failed: %#v", res)
	}
}

func TestWriteFileSymlinkRefusedStrict(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "target.txt")
	if err := os.WriteFile(target, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	policy := sandbox.Policy{Mode: sandbox.ModeNative, Workspace: root, Strict: true}
	tool := filesystem.NewWriteFileWithPolicy(policy)
	res, err := tool.Execute(t.Context(), map[string]any{"path": link, "content": "evil"})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("strict write_file followed symlink: %#v", res)
	}
}

func TestWriteFileHugeContentBounded(t *testing.T) {
	// Reproduction for RC5 H05: write_file had no input cap (disk fill in
	// one call). P1.19 must bound it; this test fails until then.
	root := t.TempDir()
	policy := sandbox.Policy{Mode: sandbox.ModeNative, Workspace: root, Strict: true}
	tool := filesystem.NewWriteFileWithPolicy(policy)
	huge := string(make([]byte, 8<<20)) // 8 MiB
	for i := range []byte(huge) {
		_ = i
	}
	res, err := tool.Execute(t.Context(), map[string]any{
		"path":    filepath.Join(root, "huge.txt"),
		"content": huge,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("write_file accepted 8MiB unbounded content (disk-fill vector)")
	}
}
