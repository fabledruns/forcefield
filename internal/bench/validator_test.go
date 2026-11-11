package bench

import (
	"context"
	"strings"
	"testing"
)

func TestValidateCommand_TrustBoundary(t *testing.T) {
	// Whitelisted commands
	if err := ValidateCommand([]string{"go", "test", "-v", "./..."}); err != nil {
		t.Fatalf("expected valid command to pass, got: %v", err)
	}
	if err := ValidateCommand([]string{"git", "status"}); err != nil {
		t.Fatalf("expected valid git command to pass, got: %v", err)
	}

	// Disallowed commands
	cases := [][]string{
		{"curl", "http://evil.com"},
		{"sh", "-c", "rm -rf /"},
		{"bash", "script.sh"},
		{"python", "-c", "print(1)"},
		{"../bin/custom"},
		{"go", "test; rm -rf /"},
		{"go", "test", "foo | bar"},
		{"go", "test", "`id`"},
		{"go", "test", "$VAR"},
		{},
	}

	for _, tc := range cases {
		if err := ValidateCommand(tc); err == nil {
			t.Fatalf("expected command %v to be rejected by trust boundary, got nil", tc)
		}
	}
}

func TestRunValidation_Execution(t *testing.T) {
	tmpBase := t.TempDir()
	mgr := NewWorkspaceManager(tmpBase, false, false)

	task := &Task{
		ID:       "VAL-TEST",
		Category: CategoryDebugging,
		Fixtures: map[string]string{
			"go.mod": "module valtest\n\ngo 1.21\n",
			"val_test.go": `package valtest
import "testing"
func TestPassing(t *testing.T) {}
`,
		},
		Validation: ValidationSpec{
			Commands: [][]string{{"go", "test", "-v", "./..."}},
		},
	}

	ws, err := mgr.Prepare(context.Background(), task, "val-run")
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	defer mgr.Cleanup(ws, true)

	res := RunValidation(context.Background(), ws)
	if !res.Passed {
		t.Fatalf("expected validation to pass, got failure: %+v", res)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "PASS") {
		t.Fatalf("expected PASS in stdout, got: %s", res.Stdout)
	}
}

func TestRunValidation_FailingCommand(t *testing.T) {
	tmpBase := t.TempDir()
	mgr := NewWorkspaceManager(tmpBase, false, false)

	task := &Task{
		ID:       "VAL-FAIL",
		Category: CategoryDebugging,
		Fixtures: map[string]string{
			"go.mod": "module valfail\n\ngo 1.21\n",
			"val_test.go": `package valfail
import "testing"
func TestFailing(t *testing.T) {
	t.Fatal("intended test failure")
}
`,
		},
		Validation: ValidationSpec{
			Commands: [][]string{{"go", "test", "./..."}},
		},
	}

	ws, err := mgr.Prepare(context.Background(), task, "val-run-fail")
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	defer mgr.Cleanup(ws, true)

	res := RunValidation(context.Background(), ws)
	if res.Passed {
		t.Fatal("expected validation to fail for failing test, got pass")
	}
	if res.ExitCode == 0 {
		t.Fatalf("expected non-zero exit code, got 0")
	}
}
