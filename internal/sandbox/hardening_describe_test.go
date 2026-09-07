package sandbox

import (
	"context"
	"testing"
)

// P1.16 regression: shell FilesystemConfined must never be true — strict
// cages tools + cwd, never shell command text.
func TestNativeStrictDescribeHonestAboutShell(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []Policy{
		{Mode: ModeNative, Workspace: dir, Strict: true},
		{Mode: ModeNative, Workspace: dir},
	} {
		ex, err := NewExecutor(p)
		if err != nil {
			t.Fatalf("NewExecutor: %v", err)
		}
		d := ex.Describe(context.Background())
		if d.FilesystemConfined {
			t.Fatalf("mode=%v strict=%v: FilesystemConfined=true overclaims shell confinement", p.Mode, p.Strict)
		}
	}
}
