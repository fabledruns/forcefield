package builtin

import "testing"

func TestNewManager_RegistersAllBuiltins(t *testing.T) {
	m, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager() unexpected error: %v", err)
	}

	want := []string{"read_file", "write_file", "list_files", "pwd", "shell"}
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
