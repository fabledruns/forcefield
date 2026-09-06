package builtin

import (
	"testing"

	"forcefield/internal/tools"
)

func TestWithLimits_ReachesToolInstances(t *testing.T) {
	m, err := NewManager(WithLimits(map[string]tools.Limits{
		"shell":        {MaxBytes: 1024},
		"shell_job":    {MaxBytes: 2048},
		"search_files": {MaxLines: 7},
		"find_files":   {MaxLines: 9},
		"secret_scan":  {MaxLines: 3},
		"read_file":    {MaxBytes: 2048},
		"list_files":   {MaxLines: 11},
	}))
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	for name, want := range map[string]tools.Limits{
		"shell":        {MaxBytes: 1024},
		"shell_job":    {MaxBytes: 2048},
		"search_files": {MaxLines: 7},
		"find_files":   {MaxLines: 9},
		"secret_scan":  {MaxLines: 3},
		"read_file":    {MaxBytes: 2048},
		"list_files":   {MaxLines: 11},
	} {
		tool, ok := m.Lookup(name)
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		lp, ok := tool.(tools.LimitsProvider)
		if !ok {
			t.Fatalf("tool %q does not report limits", name)
		}
		got := lp.ToolLimits()
		if (name == "shell" || name == "shell_job" || name == "read_file") && got.MaxBytes != want.MaxBytes {
			t.Errorf("%s MaxBytes = %d, want %d", name, got.MaxBytes, want.MaxBytes)
		}
		if (name == "search_files" || name == "find_files" || name == "secret_scan" || name == "list_files") && got.MaxLines != want.MaxLines {
			t.Errorf("%s MaxLines = %d, want %d", name, got.MaxLines, want.MaxLines)
		}
	}
}

func TestWithLimits_PartialOverrideKeepsDefaults(t *testing.T) {
	m, err := NewManager(WithLimits(map[string]tools.Limits{
		"shell": {Timeout: 0}, // nothing overridden
	}))
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	tool, _ := m.Lookup("shell")
	got := tool.(tools.LimitsProvider).ToolLimits()
	if got.MaxBytes != tools.DefaultShellMaxBytes || got.Timeout != tools.DefaultShellTimeout {
		t.Errorf("shell limits = %+v, want defaults", got)
	}
}

func TestWithLimits_UnknownToolIgnored(t *testing.T) {
	// Entries for tools without configurable bounds (or filtered out)
	// must not fail registration.
	m, err := NewManager(WithLimits(map[string]tools.Limits{
		"pwd": {MaxBytes: 10},
	}))
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if _, ok := m.Lookup("pwd"); !ok {
		t.Fatal("pwd not registered")
	}
}
