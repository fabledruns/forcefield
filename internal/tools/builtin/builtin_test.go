package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"forcefield/internal/sandbox"
)

func TestNewManager_RegistersAllBuiltins(t *testing.T) {
	m, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager() unexpected error: %v", err)
	}

	want := []string{"read_file", "write_file", "list_files", "pwd", "shell", "shell_job", "search_files", "search_code", "find_files", "git", "secret_scan"}
	for _, name := range want {
		found := false
		for _, tool := range m.List() {
			if tool.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("built-in tool %q not registered", name)
		}
	}

	if len(m.List()) != len(want) {
		t.Errorf("List() returned %d tools, want %d", len(m.List()), len(want))
	}
}

// TestManager_PermissivePolicyStillConfines pins the runtime wiring:
// a manager built exactly like runtime.New builds it (permissive
// native policy rooted at the workspace) must deny an
// outside-workspace write. This is the reported incident
// (write_file /go/main.go resolving to a drive-root path) driven
// through the production registration path instead of a single tool.
func TestManager_PermissivePolicyStillConfines(t *testing.T) {
	ws := t.TempDir()
	policy := sandbox.Policy{Mode: sandbox.ModeNative, Workspace: ws}
	m, err := NewManager(WithPolicy(policy))
	if err != nil {
		t.Fatalf("NewManager() unexpected error: %v", err)
	}

	// Inside writes keep working through the wired manager.
	res, err := m.Execute(context.Background(), "write_file", map[string]any{"path": "note.txt", "content": "hi"})
	if err != nil || res.IsError {
		t.Fatalf("inside write through wired manager = %+v err=%v, want success", res, err)
	}
	if _, err := os.Stat(filepath.Join(ws, "note.txt")); err != nil {
		t.Fatalf("inside write not persisted: %v", err)
	}

	// The incident spelling is denied and creates nothing.
	probe := string(filepath.Separator) + "go" + string(filepath.Separator) + "main.go"
	res, err = m.Execute(context.Background(), "write_file", map[string]any{"path": probe, "content": "x"})
	if err != nil {
		t.Fatalf("Execute() returned a Go error = %v, want a domain denial", err)
	}
	if !res.IsError {
		t.Fatalf("wired write_file %q succeeded, want workspace denial", probe)
	}
}
