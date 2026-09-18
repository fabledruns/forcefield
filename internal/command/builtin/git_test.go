package builtin

import (
	"errors"
	"strings"
	"testing"
)

// TestGit_PrintsStatus is the smallest /git behavior: it surfaces git
// status without invoking the model.
func TestGit_PrintsStatus(t *testing.T) {
	ctx := &fakeContext{gitOut: "## main\n M foo.go"}
	if err := NewGit().Execute(ctx, nil); err != nil {
		t.Fatalf("Git.Execute returned an error: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	if !strings.Contains(out, "## main") {
		t.Fatalf("/git output missing status content:\n%s", out)
	}
	if ctx.gitAction != "status" {
		t.Fatalf("Git(action, _) = %q, want status", ctx.gitAction)
	}
}

func TestGit_RejectsArgs(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewGit().Execute(ctx, []string{"log"}); err == nil {
		t.Fatal("Git.Execute accepted unexpected arguments")
	}
}

func TestGit_PropagatesGitError(t *testing.T) {
	ctx := &fakeContext{gitErr: errors.New("not a git repository")}
	if err := NewGit().Execute(ctx, nil); err == nil {
		t.Fatal("Git.Execute swallowed the git inspection error")
	}
}
