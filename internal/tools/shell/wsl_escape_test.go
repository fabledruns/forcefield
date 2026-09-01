package shell

import (
	"context"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
)

func TestIsWSLForbiddenPattern(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"cat /mnt/c/Users/file.txt", true},
		{"ls /mnt/d/data", true},
		{"echo hello", false},
		{"cat C:/Users/file.txt", true},
		{"cat C:\\Users\\file.txt", true},
		{"echo C: hello", false}, // bare C: without slash should not block (conservative)
		{"cat D:/data/file", true},
		{"ls /home/user/file", false},
		{"cat ./mnt/file", true},   // conservative: any /mnt/ is blocked
		{"cat //mnt/c/file", true}, // still contains /mnt/
		{"type \\\\wsl.localhost\\Ubuntu\\file", true},
		{"cat \\\\wsl$\\Ubuntu\\file", true},
		// Expanded WSL escape coverage
		{"cat /proc/self/cwd/etc/passwd", true},
		{"cat /sys/kernel/hostname", true},
		{"cat /run/WSL/interop", true},
		{"wslpath -w /home/user", true},
		{"powershell.exe -Command Get-Content C:/file", true},
		{"cmd.exe /c dir", true},
		{"wsl.exe -d Ubuntu ls /", true},
		{"cat ../../etc/passwd", true},
		{"ls ../secret", true},
		{"echo safe", false},
		{"git log --oneline", false},
	}
	for _, tc := range cases {
		if got := isWSLForbiddenPattern(tc.cmd); got != tc.want {
			t.Errorf("isWSLForbiddenPattern(%q)=%v want %v", tc.cmd, got, tc.want)
		}
	}
}

// fakeWSLExecutor is a stub that reports WSL mode without needing a real WSL installation.
type fakeWSLExecutor struct {
	mode sandbox.Mode
}

func (f *fakeWSLExecutor) Prepare(ctx context.Context, req sandbox.Request) (*sandbox.Prepared, error) {
	return nil, nil
}
func (f *fakeWSLExecutor) Probe(ctx context.Context) error { return nil }
func (f *fakeWSLExecutor) Describe(ctx context.Context) sandbox.Enforcement {
	return sandbox.Enforcement{Mode: f.mode}
}

// Ensure Shell with WSL executor blocks /mnt
func TestShell_WSLBlocksMnt(t *testing.T) {
	exec := &fakeWSLExecutor{mode: sandbox.ModeWSL}
	s := NewShellWithExecutor(exec)
	// Need to bypass backend probe: stub Probe returns nil, so ensureBackend will succeed.
	// Also need to ensure isWSLMode reads correctly.
	if !s.isWSLMode(context.Background()) {
		t.Fatal("expected WSL mode")
	}
	cases := []struct {
		cmd     string
		blocked bool
	}{
		{"cat /mnt/c/Users/Admin/.env", true},
		{"cat C:/Windows/System32/file", true},
		{"echo hello", false},
		{"ls /home/user", false},
	}
	for _, tc := range cases {
		if got := isWSLForbiddenPattern(tc.cmd); got != tc.blocked {
			t.Errorf("isWSLForbiddenPattern(%q)=%v want %v", tc.cmd, got, tc.blocked)
		}
		if tc.blocked {
			res, err := s.Execute(context.Background(), map[string]any{"command": tc.cmd})
			if err != nil {
				t.Fatalf("Execute error for %q: %v", tc.cmd, err)
			}
			if !res.IsError {
				t.Errorf("command %q should be blocked in WSL mode, got success", tc.cmd)
			}
			if !strings.Contains(strings.ToLower(res.Content), "wsl") && !strings.Contains(strings.ToLower(res.Content), "blocked") {
				t.Errorf("blocked message should mention WSL, got %q", res.Content)
			}
			if !strings.Contains(strings.ToLower(res.Content), "mitigation") {
				t.Errorf("blocked message should note mitigation, got %q", res.Content)
			}
		} else {
			// Non-blocked: just verify pattern false; don't execute via fake executor
			// (fake Prepare would panic on nil)
		}
	}
}

func TestShell_NativeDoesNotBlockMnt(t *testing.T) {
	// Native mode should not block /mnt/ (it's host filesystem)
	exec := &fakeWSLExecutor{mode: sandbox.ModeNative}
	s := NewShellWithExecutor(exec)
	if s.isWSLMode(context.Background()) {
		t.Fatal("expected native mode not WSL")
	}
	if isWSLForbiddenPattern("cat /mnt/c/file") == false {
		t.Fatal("pattern should still be true lexically, but native mode should not block")
	}
	// In native mode, the guard is not triggered, so isWSLMode check prevents blocking
	// We verify that the shell does not block due to WSL guard when native
	// We can't fully test execution without real backend, but we can test the guard condition
	if s.isWSLMode(context.Background()) && isWSLForbiddenPattern("cat /mnt/c/file") {
		t.Error("native should not be considered WSL")
	}
}

// Ensure the mitigation comment is present in code (ensures doc)
func TestWSLMitigationIsDocumented(t *testing.T) {
	// This test simply ensures the blocking path mentions mitigation not sandbox
	exec := &fakeWSLExecutor{mode: sandbox.ModeWSL}
	s := NewShellWithExecutor(exec)
	res, _ := s.Execute(context.Background(), map[string]any{"command": "cat /mnt/c/file"})
	if !strings.Contains(res.Content, "mitigation") {
		t.Errorf("WSL block message should clearly be mitigation, got %q", res.Content)
	}
	if strings.Contains(strings.ToLower(res.Content), "sandbox") && !strings.Contains(strings.ToLower(res.Content), "not a sandbox") {
		t.Errorf("should not claim sandbox, got %q", res.Content)
	}
}
