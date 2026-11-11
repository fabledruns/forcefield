package bench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkspaceManager_GuardrailRejectsRepoRootAndAncestors verifies that
// Cleanup strictly refuses to delete the repository root, ancestors of the
// repository root, filesystem root boundaries, and paths outside BaseDir.
func TestWorkspaceManager_GuardrailRejectsRepoRootAndAncestors(t *testing.T) {
	baseDir := t.TempDir()
	mgr := NewWorkspaceManager(baseDir, false, false)

	repoRoot := findEnclosingRepoRoot("")
	if repoRoot == "" {
		t.Fatal("could not find enclosing repo root")
	}

	testCases := []struct {
		name      string
		targetDir string
	}{
		{"RepoRoot", repoRoot},
		{"RepoParent", filepath.Dir(repoRoot)},
		{"EmptyString", ""},
		{"Dot", "."},
		{"Slash", "/"},
		{"OutsideBaseDir", t.TempDir()},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ws := &Workspace{Dir: tc.targetDir}
			err := mgr.Cleanup(ws, true)
			if err == nil {
				t.Fatalf("CRITICAL SAFETY FAILURE: Cleanup allowed deleting %s (%s)", tc.name, tc.targetDir)
			}
			if !strings.Contains(err.Error(), "CRITICAL REPOSITORY SAFETY GUARDRAIL") {
				t.Errorf("expected guardrail error message, got: %v", err)
			}
		})
	}
}

// TestWorkspaceManager_GuardrailRejectsSymlinkToRepoRoot verifies that symlinks
// pointing to the repository root cannot bypass the BaseDir boundary.
func TestWorkspaceManager_GuardrailRejectsSymlinkToRepoRoot(t *testing.T) {
	baseDir := t.TempDir()
	mgr := NewWorkspaceManager(baseDir, false, false)

	repoRoot := findEnclosingRepoRoot("")
	symlinkPath := filepath.Join(baseDir, "symlink-to-repo")
	if err := os.Symlink(repoRoot, symlinkPath); err != nil {
		t.Skipf("skipping symlink test on platform without symlink permissions: %v", err)
	}

	ws := &Workspace{Dir: symlinkPath}
	err := mgr.Cleanup(ws, true)
	if err == nil {
		t.Fatal("CRITICAL SAFETY FAILURE: Cleanup deleted symlink pointing to repo root")
	}
	if !strings.Contains(err.Error(), "CRITICAL REPOSITORY SAFETY GUARDRAIL") {
		t.Errorf("expected guardrail error, got: %v", err)
	}
	// Verify repo root is completely intact
	if _, err := os.Stat(repoRoot); err != nil {
		t.Fatalf("repo root was damaged: %v", err)
	}
}

// TestWorkspaceManager_IgnoredDotEnvSurvivesLifecycle verifies that user-owned
// root files (including git-ignored .env, .gitignore, and untracked files)
// survive all benchmark lifecycle stages: prepare, diff, restore, and cleanup.
func TestWorkspaceManager_IgnoredDotEnvSurvivesLifecycle(t *testing.T) {
	testHome := t.TempDir()
	t.Setenv("HOME", testHome)
	t.Setenv("USERPROFILE", testHome)

	// Simulated repository root with ignored .env
	simRepo := t.TempDir()
	t.Chdir(simRepo)

	envContent := "NVIDIA_API_KEY=synthetic_test_key_survives_lifecycle\nSECRET=12345\n"
	envPath := filepath.Join(simRepo, ".env")
	if err := os.WriteFile(envPath, []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}

	gitignoreContent := ".env\n*.log\n"
	gitignorePath := filepath.Join(simRepo, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(gitignoreContent), 0o644); err != nil {
		t.Fatal(err)
	}

	untrackedPath := filepath.Join(simRepo, "temp_user_notes.md")
	untrackedContent := "# User Notes\nImportant information.\n"
	if err := os.WriteFile(untrackedPath, []byte(untrackedContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Initialize simulated repo git
	if err := os.Mkdir(filepath.Join(simRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	benchBase := filepath.Join(t.TempDir(), "bench-workspaces")
	mgr := NewWorkspaceManager(benchBase, false, false)

	task := &Task{
		ID:       "SAFE-001",
		Category: CategoryDebugging,
		Fixtures: map[string]string{
			"main.go": "package main\nfunc main() {}\n",
		},
		CanonicalFiles: map[string]string{
			"main_test.go": "package main\nimport \"testing\"\nfunc TestMain(t *testing.T) {}\n",
		},
	}

	ctx := context.Background()

	// 1. Prepare
	ws, err := mgr.Prepare(ctx, task, "run-safe-1")
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	// 2. Collect diff
	if _, _, err := mgr.Diff(ctx, ws); err != nil {
		t.Fatalf("Diff failed: %v", err)
	}

	// 3. Restore canonical
	if err := mgr.RestoreCanonical(ws); err != nil {
		t.Fatalf("RestoreCanonical failed: %v", err)
	}

	// 4. Cleanup on success
	if err := mgr.Cleanup(ws, true); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}

	// ASSERT: Ignored .env is intact with exact contents
	gotEnv, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("CRITICAL FAILURE: .env was deleted or inaccessible: %v", err)
	}
	if string(gotEnv) != envContent {
		t.Fatalf("CRITICAL FAILURE: .env was corrupted or modified: got %q, want %q", string(gotEnv), envContent)
	}

	// ASSERT: .gitignore is intact
	gotIgnore, err := os.ReadFile(gitignorePath)
	if err != nil || string(gotIgnore) != gitignoreContent {
		t.Fatalf(".gitignore was modified or deleted: %v", err)
	}

	// ASSERT: untracked user file is intact
	gotNotes, err := os.ReadFile(untrackedPath)
	if err != nil || string(gotNotes) != untrackedContent {
		t.Fatalf("untracked file was modified or deleted: %v", err)
	}
}

// TestWorkspaceManager_RootFilesSurviveLifecycleFailureTimeoutPanic verifies that
// root repository files survive failure, timeout/cancellation, and panic recovery paths.
func TestWorkspaceManager_RootFilesSurviveLifecycleFailureTimeoutPanic(t *testing.T) {
	testHome := t.TempDir()
	t.Setenv("HOME", testHome)
	t.Setenv("USERPROFILE", testHome)

	simRepo := t.TempDir()
	t.Chdir(simRepo)

	envContent := "NVIDIA_API_KEY=synthetic_failure_test_key\n"
	envPath := filepath.Join(simRepo, ".env")
	if err := os.WriteFile(envPath, []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(simRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	benchBase := filepath.Join(t.TempDir(), "bench-workspaces")
	mgr := NewWorkspaceManager(benchBase, false, false)

	task := &Task{
		ID:       "FAIL-001",
		Category: CategoryDebugging,
		Fixtures: map[string]string{
			"app.go": "package app\n",
		},
	}

	// Scenario 1: Task Failure path
	t.Run("FailurePath", func(t *testing.T) {
		ws, err := mgr.Prepare(context.Background(), task, "run-fail")
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		if err := mgr.Cleanup(ws, false); err != nil {
			t.Fatalf("Cleanup: %v", err)
		}
		got, err := os.ReadFile(envPath)
		if err != nil || string(got) != envContent {
			t.Fatalf(".env damaged during failure cleanup: %v", err)
		}
	})

	// Scenario 2: Cancellation / Timeout path
	t.Run("TimeoutCancellationPath", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		ws, err := mgr.Prepare(ctx, task, "run-cancel")
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		// Cancel context simulating timeout
		cancel()

		// Attempt operations with canceled context
		_, _, _ = mgr.Diff(ctx, ws)
		_ = mgr.Cleanup(ws, false)

		got, err := os.ReadFile(envPath)
		if err != nil || string(got) != envContent {
			t.Fatalf(".env damaged during canceled run: %v", err)
		}
	})

	// Scenario 3: Panic Recovery path
	t.Run("PanicRecoveryPath", func(t *testing.T) {
		ws, err := mgr.Prepare(context.Background(), task, "run-panic")
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}

		func() {
			defer func() {
				_ = recover()
				// Teardown after panic
				_ = mgr.Cleanup(ws, false)
			}()
			panic("simulated fatal agent panic")
		}()

		got, err := os.ReadFile(envPath)
		if err != nil || string(got) != envContent {
			t.Fatalf(".env damaged during panic teardown: %v", err)
		}
	})
}
