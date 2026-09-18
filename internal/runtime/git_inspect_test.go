package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"forcefield/internal/tools"
)

type stubGitTool struct {
	content string
	isError bool
}

func (s stubGitTool) Name() string        { return "git" }
func (s stubGitTool) Description() string { return "stub git" }
func (s stubGitTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (s stubGitTool) Execute(_ context.Context, _ map[string]any) (tools.Result, error) {
	return tools.Result{Content: s.content, IsError: s.isError}, nil
}

func gitTestRuntime(content string, isError bool) *Runtime {
	manager := tools.NewManager(tools.NewRegistry())
	if err := manager.Register(stubGitTool{content: content, isError: isError}); err != nil {
		panic(err)
	}
	return &Runtime{fullManager: manager}
}

func TestGitInspect_PassesThroughContent(t *testing.T) {
	r := gitTestRuntime("diff --git a/foo b/foo", false)
	out, err := r.GitInspect(context.Background(), "diff", "")
	if err != nil {
		t.Fatalf("GitInspect() error = %v", err)
	}
	if out != "diff --git a/foo b/foo" {
		t.Fatalf("GitInspect() = %q, want passthrough", out)
	}
}

func TestGitInspect_SoftErrorBecomesError(t *testing.T) {
	r := gitTestRuntime("not a git repository", true)
	_, err := r.GitInspect(context.Background(), "diff", "")
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("GitInspect() error = %v, want the soft error text", err)
	}
}

func TestGitInspect_NilRuntimeErrors(t *testing.T) {
	var r *Runtime
	if _, err := r.GitInspect(context.Background(), "diff", ""); err == nil {
		t.Fatal("nil GitInspect did not return an error")
	}
}

func TestGitInspect_MissingToolErrors(t *testing.T) {
	r := newTestRuntime(&scriptedProvider{turns: testTurns()})
	if _, err := r.GitInspect(context.Background(), "diff", ""); err == nil {
		t.Fatal("GitInspect without a git tool did not return an error")
	}
}

func TestTreeSignature_HashesStatus(t *testing.T) {
	r := gitTestRuntime("## main\n M foo.go", false)
	got, err := r.TreeSignature(context.Background())
	if err != nil {
		t.Fatalf("TreeSignature() error = %v", err)
	}
	sum := sha256.Sum256([]byte("## main\n M foo.go"))
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("TreeSignature() = %q, want %q", got, want)
	}
}

func TestTreeSignature_PropagatesInspectionError(t *testing.T) {
	r := gitTestRuntime("not a git repository", true)
	if got, err := r.TreeSignature(context.Background()); err == nil || got != "" {
		t.Fatalf("TreeSignature() = (%q, %v), want empty with error", got, err)
	}
}
