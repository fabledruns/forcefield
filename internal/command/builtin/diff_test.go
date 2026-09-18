package builtin

import (
	"errors"
	"strings"
	"testing"
)

// TestDiff_PrintsDiff is the smallest /diff behavior: it surfaces the
// workspace diff without invoking the model.
func TestDiff_PrintsDiff(t *testing.T) {
	ctx := &fakeContext{gitOut: "diff --git a/foo.go b/foo.go"}
	if err := NewDiff().Execute(ctx, nil); err != nil {
		t.Fatalf("Diff.Execute returned an error: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	if !strings.Contains(out, "diff --git") {
		t.Fatalf("/diff output missing diff content:\n%s", out)
	}
}

func TestDiff_ForwardsPathScope(t *testing.T) {
	ctx := &fakeContext{gitOut: "clean"}
	if err := NewDiff().Execute(ctx, []string{"internal/foo"}); err != nil {
		t.Fatalf("Diff.Execute returned an error: %v", err)
	}
	if ctx.gitAction != "diff" || ctx.gitPath != "internal/foo" {
		t.Fatalf("Git(action, path) = (%q, %q), want (diff, internal/foo)",
			ctx.gitAction, ctx.gitPath)
	}
}

func TestDiff_RejectsTooManyArgs(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewDiff().Execute(ctx, []string{"a", "b"}); err == nil {
		t.Fatal("Diff.Execute accepted more than one argument")
	}
}

func TestDiff_PropagatesGitError(t *testing.T) {
	ctx := &fakeContext{gitErr: errors.New("not a git repository")}
	if err := NewDiff().Execute(ctx, nil); err == nil {
		t.Fatal("Diff.Execute swallowed the git inspection error")
	}
}
