package security

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
)

func TestSecretScan_DetectsAWSKeyInFile(t *testing.T) {
	dir := t.TempDir()
	secret := "AKIAIOSFODNN7EXAMPLE"
	p := filepath.Join(dir, "config.go")
	os.WriteFile(p, []byte("key = \""+secret+"\"\n"), 0o644)

	tool := NewSecretScan()
	res, err := tool.Execute(context.Background(), map[string]any{"path": p})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError: %s", res.Content)
	}
	if !strings.Contains(res.Content, "aws-access-key") {
		t.Fatalf("missing rule id, got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, secret) {
		t.Fatalf("full secret must be redacted, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "AK") {
		t.Fatalf("redacted snippet should keep edges, got:\n%s", res.Content)
	}
}

func TestSecretScan_DetectsPrivateKeyAndInlineText(t *testing.T) {
	tool := NewSecretScan()
	res, err := tool.Execute(context.Background(), map[string]any{
		"text": "header\n-----BEGIN RSA PRIVATE KEY-----\nabc\n",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "private-key-block") {
		t.Fatalf("missing rule, got:\n%s", res.Content)
	}
}

func TestSecretScan_CleanFileReportsNoFindings(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "main.go")
	os.WriteFile(p, []byte("package main\nfunc main() {}\n"), 0o644)

	tool := NewSecretScan()
	res, err := tool.Execute(context.Background(), map[string]any{"path": p})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "no hardcoded secrets detected") {
		t.Fatalf("want clean report, got:\n%s", res.Content)
	}
}

func TestSecretScan_PathAndTextBothIsHardError(t *testing.T) {
	tool := NewSecretScan()
	if _, err := tool.Execute(context.Background(), map[string]any{"path": "a", "text": "b"}); err == nil {
		t.Fatalf("both path+text must be a hard error")
	}
	if _, err := tool.Execute(context.Background(), map[string]any{}); err == nil {
		t.Fatalf("neither path nor text must be a hard error")
	}
}

func TestSecretScan_DirectoryIsSoftError(t *testing.T) {
	dir := t.TempDir()
	tool := NewSecretScan()
	res, err := tool.Execute(context.Background(), map[string]any{"path": dir})
	if err != nil {
		t.Fatalf("directory must be soft error, got hard: %v", err)
	}
	if !res.IsError {
		t.Fatalf("want IsError for directory")
	}
}

func TestSecretScan_OversizeIsSoftError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.txt")
	f, _ := os.Create(p)
	f.Write(make([]byte, maxScanBytes+10))
	f.Close()

	tool := NewSecretScan()
	res, err := tool.Execute(context.Background(), map[string]any{"path": p})
	if err != nil {
		t.Fatalf("oversize must be soft error, got hard: %v", err)
	}
	if !res.IsError {
		t.Fatalf("want IsError for oversize")
	}
}

func TestSecretScan_WSLConfinesPath(t *testing.T) {
	ws := t.TempDir()
	tool := NewSecretScanWithPolicy(sandbox.Policy{Mode: sandbox.ModeWSL, Workspace: ws})
	res, err := tool.Execute(context.Background(), map[string]any{"path": `..\..` + string(filepath.Separator) + `x`})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("traversal must fail, got:\n%s", res.Content)
	}
}

func TestSecretScan_NoNetworkNoValidation(t *testing.T) {
	// Static guarantee by construction: this package imports no net
	// packages. Runtime check: scan works fully offline on garbage.
	tool := NewSecretScan()
	res, err := tool.Execute(context.Background(), map[string]any{"text": "nothing secret here\n"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError || !strings.Contains(res.Content, "no hardcoded secrets") {
		t.Fatalf("got:\n%s", res.Content)
	}
}
