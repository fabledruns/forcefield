package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseDotEnv(t *testing.T) {
	body := "# comment\n\nNVIDIA_API_KEY=\"abc123\"\nOTHER='x y'\nPLAIN=z\n"
	values, err := parseDotEnv(body)
	if err != nil {
		t.Fatalf("parseDotEnv error = %v", err)
	}
	if values["NVIDIA_API_KEY"] != "abc123" {
		t.Errorf("quoted value = %q", values["NVIDIA_API_KEY"])
	}
	if values["OTHER"] != "x y" {
		t.Errorf("single-quoted value = %q", values["OTHER"])
	}
	if values["PLAIN"] != "z" {
		t.Errorf("plain value = %q", values["PLAIN"])
	}
}

func TestParseDotEnvRejectsMalformedLines(t *testing.T) {
	_, err := parseDotEnv("GOOD=yes\nthis line has no equals sign\n")
	if err == nil {
		t.Fatal("parseDotEnv accepted a line without '='")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error = %v, want the offending line number", err)
	}
}

func TestResolveEnvValuePrefersEnvironment(t *testing.T) {
	isolateHome(t)

	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(`NVIDIA_API_KEY="from-dot-env"`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(apiKeyName, "from-environment")

	key, source, err := ResolveEnvValue(apiKeyName)
	if err != nil {
		t.Fatalf("ResolveEnvValue error = %v", err)
	}
	if key != "from-environment" || source != "environment" {
		t.Errorf("key=%q source=%q, want the environment to win", key, source)
	}
}

func TestResolveEnvValueReadsProjectDotEnv(t *testing.T) {
	isolateHome(t)

	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("NVIDIA_API_KEY=nvapi-test-value\n# done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(apiKeyName, "")

	key, source, err := ResolveEnvValue(apiKeyName)
	if err != nil {
		t.Fatalf("ResolveEnvValue error = %v", err)
	}
	if key != "nvapi-test-value" {
		t.Errorf("key = %q", key)
	}
	if !strings.Contains(source, ".env") {
		t.Errorf("source = %q, want it to name the .env file", source)
	}
}

func TestLoadPicksUpDotEnvKeyWithoutTouchingProcessEnv(t *testing.T) {
	home := isolateHome(t)

	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(`NVIDIA_API_KEY="nvapi-from-file"`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, "model:\n  provider: nvidia\n  endpoint: https://integrate.api.nvidia.com/v1\n  name: test-model\n")
	t.Setenv(apiKeyName, "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Model.APIKey != "nvapi-from-file" {
		t.Errorf("cfg.Model.APIKey = %q, want the .env value", cfg.Model.APIKey)
	}
	if os.Getenv(apiKeyName) != "" {
		t.Fatal(".env contents leaked into the process environment; they would be inherited by every shell command")
	}
	_ = home
}

func TestLoadFailsOnMalformedDotEnv(t *testing.T) {
	isolateHome(t)

	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("garbage line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, "model:\n  provider: nvidia\n  endpoint: https://x/v1\n  name: m\n")
	t.Setenv(apiKeyName, "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() accepted a malformed .env")
	}
	if !strings.Contains(err.Error(), ".env") {
		t.Errorf("error = %v, want it to name the .env file", err)
	}
}

func TestResolveEnvValueReadsProjectEnvDir(t *testing.T) {
	isolateHome(t)

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, ".env"), []byte("NVIDIA_API_KEY=nvapi-from-project-dir\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{ProjectDir: projectDir}

	// Change to another directory that has no .env
	otherDir := t.TempDir()
	t.Chdir(otherDir)
	t.Setenv(apiKeyName, "")

	key, source, err := cfg.ResolveEnvValue(apiKeyName)
	if err != nil {
		t.Fatalf("cfg.ResolveEnvValue error = %v", err)
	}
	if key != "nvapi-from-project-dir" {
		t.Errorf("key = %q, want nvapi-from-project-dir", key)
	}
	assertSameDotEnvSource(t, source, projectDir)

	// Also test ResolveEnvValueWithDir directly
	key2, source2, err := ResolveEnvValueWithDir(projectDir, apiKeyName)
	if err != nil || key2 != "nvapi-from-project-dir" {
		t.Errorf("ResolveEnvValueWithDir failed: key=%q, source=%q, err=%v", key2, source2, err)
	}
	assertSameDotEnvSource(t, source2, projectDir)
}

func TestResolveEnvValueUpwardDiscovery(t *testing.T) {
	isolateHome(t)

	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, ".env"), []byte("NVIDIA_API_KEY=nvapi-from-repo-root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	nested := filepath.Join(repoDir, "sub1", "sub2")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(nested)
	t.Setenv(apiKeyName, "")

	key, source, err := ResolveEnvValue(apiKeyName)
	if err != nil {
		t.Fatalf("ResolveEnvValue error = %v", err)
	}
	if key != "nvapi-from-repo-root" {
		t.Errorf("key = %q, want nvapi-from-repo-root", key)
	}
	assertSameDotEnvSource(t, source, repoDir)
}

func TestResolveEnvValueUpwardStopsAtGitBoundary(t *testing.T) {
	isolateHome(t)

	parentDir := t.TempDir()
	// Foreign .env outside the git repo
	if err := os.WriteFile(filepath.Join(parentDir, ".env"), []byte("NVIDIA_API_KEY=foreign-parent-key\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Non-git directory inside parentDir must NOT discover parentDir/.env
	nonGitDir := filepath.Join(parentDir, "nongit")
	if err := os.Mkdir(nonGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nonGitDir)
	t.Setenv(apiKeyName, "")

	key, _, err := ResolveEnvValue(apiKeyName)
	if err != nil {
		t.Fatalf("ResolveEnvValue error = %v", err)
	}
	if key != "" {
		t.Fatalf("security violation: upward discovery escaped non-git directory into parent: got %q", key)
	}

	// 2. Git repo inside parentDir must NOT discover parentDir/.env
	gitRepo := filepath.Join(parentDir, "gitrepo")
	if err := os.MkdirAll(filepath.Join(gitRepo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(gitRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(gitRepo, "sub"))

	key, _, err = ResolveEnvValue(apiKeyName)
	if err != nil {
		t.Fatalf("ResolveEnvValue error = %v", err)
	}
	if key != "" {
		t.Fatalf("security violation: upward discovery crossed .git boundary into parent: got %q", key)
	}
}

func TestResolveEnvValueUpwardDiscovery_WorktreeGitFile(t *testing.T) {
	isolateHome(t)

	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, ".env"), []byte("NVIDIA_API_KEY=nvapi-from-worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Git worktrees and submodules represent .git as a file rather than a directory.
	if err := os.WriteFile(filepath.Join(repoDir, ".git"), []byte("gitdir: /fake/path/to/parent/repo/.git/worktrees/wt\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	nested := filepath.Join(repoDir, "pkg", "core")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(nested)
	t.Setenv(apiKeyName, "")

	key, source, err := ResolveEnvValue(apiKeyName)
	if err != nil {
		t.Fatalf("ResolveEnvValue error = %v", err)
	}
	if key != "nvapi-from-worktree" {
		t.Errorf("key = %q, want nvapi-from-worktree", key)
	}
	assertSameDotEnvSource(t, source, repoDir)
}

func TestResolveEnvValueUpwardDiscovery_SymlinkedDir(t *testing.T) {
	isolateHome(t)

	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, ".env"), []byte("NVIDIA_API_KEY=nvapi-from-symlinked-repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(repoDir, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	// Symlink an external directory pointing into the repo's nested folder.
	linkDir := filepath.Join(t.TempDir(), "external-link")
	if err := os.Symlink(nested, linkDir); err != nil {
		t.Skipf("skipping symlink test on platform without symlink privileges: %v", err)
	}

	t.Chdir(linkDir)
	t.Setenv(apiKeyName, "")

	key, source, err := ResolveEnvValue(apiKeyName)
	if err != nil {
		t.Fatalf("ResolveEnvValue error = %v", err)
	}
	if key != "nvapi-from-symlinked-repo" {
		t.Errorf("key = %q, want nvapi-from-symlinked-repo", key)
	}
	assertSameDotEnvSource(t, source, repoDir)
}

func canonicalizeTestPath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	abs = strings.TrimPrefix(filepath.Clean(abs), `\\?\`)
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return abs
}

func pathContains(target, parent string) bool {
	canonTarget := canonicalizeTestPath(target)
	canonParent := canonicalizeTestPath(parent)
	if canonTarget == canonParent {
		return true
	}
	rel, err := filepath.Rel(canonParent, canonTarget)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func extractDotEnvPath(source string) string {
	s := strings.TrimSpace(source)
	s = strings.TrimPrefix(s, ".env file ")
	if idx := strings.Index(s, " ("); idx != -1 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

func assertSameDotEnvSource(t *testing.T, source, expectedDir string) {
	t.Helper()
	sourcePath := extractDotEnvPath(source)
	expectedFile := filepath.Join(expectedDir, ".env")
	fiSource, errSource := os.Stat(sourcePath)
	fiExpected, errExpected := os.Stat(expectedFile)
	if errSource == nil && errExpected == nil {
		if os.SameFile(fiSource, fiExpected) {
			return
		}
	}
	canonSource := canonicalizeTestPath(sourcePath)
	canonExpectedFile := canonicalizeTestPath(expectedFile)
	if canonSource == canonExpectedFile {
		return
	}
	canonExpectedDir := canonicalizeTestPath(expectedDir)
	if pathContains(canonSource, canonExpectedDir) {
		return
	}
	t.Fatalf("dotenv source mismatch:\n got:  %q (path: %q, canon: %q)\n want inside: %q (canon: %q)\n or exact file: %q (canon: %q)",
		source, sourcePath, canonSource, expectedDir, canonExpectedDir, expectedFile, canonExpectedFile)
}

func TestResolveEnvValue_PathCanonicalizationRegression(t *testing.T) {
	isolateHome(t)

	t.Run("canonical vs non-canonical temporary paths", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("NVIDIA_API_KEY=uncleaned-key\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		nested := filepath.Join(repo, "a", "b", "..", "c")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}

		t.Setenv(apiKeyName, "")
		t.Chdir(nested)
		key, source, err := ResolveEnvValue(apiKeyName)
		if err != nil {
			t.Fatalf("ResolveEnvValue error = %v", err)
		}
		if key != "uncleaned-key" {
			t.Errorf("key = %q, want uncleaned-key", key)
		}
		assertSameDotEnvSource(t, source, repo)

		// Also test with explicit uncleaned projDir
		uncleanedRepo := filepath.Join(repo, "sub", "..")
		keyDir, sourceDir, err := ResolveEnvValueWithDir(uncleanedRepo, apiKeyName)
		if err != nil {
			t.Fatalf("ResolveEnvValueWithDir error = %v", err)
		}
		if keyDir != "uncleaned-key" {
			t.Errorf("keyDir = %q, want uncleaned-key", keyDir)
		}
		assertSameDotEnvSource(t, sourceDir, repo)
	})

	t.Run("windows case differences", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("NVIDIA_API_KEY=case-key\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		sub := filepath.Join(repo, "SubDir")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}

		var testDir string
		if runtime.GOOS == "windows" {
			testDir = filepath.Join(repo, "subdir")
		} else {
			testDir = sub
		}

		t.Setenv(apiKeyName, "")
		t.Chdir(testDir)
		key, source, err := ResolveEnvValue(apiKeyName)
		if err != nil {
			t.Fatalf("ResolveEnvValue error = %v", err)
		}
		if key != "case-key" {
			t.Errorf("key = %q, want case-key", key)
		}
		assertSameDotEnvSource(t, source, repo)
	})

	t.Run("macOS /var vs /private/var or symlink alias equivalence", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("NVIDIA_API_KEY=symlink-alias-key\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		nested := filepath.Join(repo, "pkg")
		if err := os.Mkdir(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		symlinkRepo := filepath.Join(t.TempDir(), "repo-alias")
		if err := os.Symlink(repo, symlinkRepo); err != nil {
			t.Skipf("symlinks unavailable on this platform/privilege: %v", err)
		}

		t.Setenv(apiKeyName, "")
		t.Chdir(filepath.Join(symlinkRepo, "pkg"))
		key, source, err := ResolveEnvValue(apiKeyName)
		if err != nil {
			t.Fatalf("ResolveEnvValue error = %v", err)
		}
		if key != "symlink-alias-key" {
			t.Errorf("key = %q, want symlink-alias-key", key)
		}
		assertSameDotEnvSource(t, source, repo)
	})

	t.Run("windows short-name vs long-name", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("Windows 8.3 short names are specific to Windows hosts")
		}
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("NVIDIA_API_KEY=shortname-key\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		shortRepo, err := windowsShortPath(repo)
		if err != nil {
			t.Skipf("short path unavailable: %v", err)
		}

		t.Setenv(apiKeyName, "")
		key, source, err := ResolveEnvValueWithDir(shortRepo, apiKeyName)
		if err != nil {
			t.Fatalf("ResolveEnvValueWithDir error = %v", err)
		}
		if key != "shortname-key" {
			t.Errorf("key = %q, want shortname-key", key)
		}
		assertSameDotEnvSource(t, source, repo)
	})

	t.Run("genuine path outside git boundary", func(t *testing.T) {
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, ".env"), []byte("NVIDIA_API_KEY=forbidden-parent-key\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		repo := filepath.Join(outside, "repo")
		nested := filepath.Join(repo, "pkg", "inner")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}

		t.Setenv(apiKeyName, "")
		t.Chdir(nested)
		key, _, err := ResolveEnvValue(apiKeyName)
		if err != nil {
			t.Fatalf("ResolveEnvValue error = %v", err)
		}
		if key != "" {
			t.Fatalf("security violation: upward discovery crossed Git root: got %q", key)
		}
	})

	t.Run("parent directory traversal outside git repository", func(t *testing.T) {
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, ".env"), []byte("NVIDIA_API_KEY=outside-git-key\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		notGit := filepath.Join(outside, "notgit", "sub")
		if err := os.MkdirAll(notGit, 0o755); err != nil {
			t.Fatal(err)
		}

		t.Setenv(apiKeyName, "")
		t.Chdir(notGit)
		key, _, err := ResolveEnvValue(apiKeyName)
		if err != nil {
			t.Fatalf("ResolveEnvValue error = %v", err)
		}
		if key != "" {
			t.Fatalf("security violation: upward discovery escaped non-git directory: got %q", key)
		}
	})
}
