package config

import (
	"os"
	"path/filepath"
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

	SetProjectEnvDir(projectDir)
	t.Cleanup(func() { SetProjectEnvDir("") })

	// Change to another directory that has no .env
	otherDir := t.TempDir()
	t.Chdir(otherDir)
	t.Setenv(apiKeyName, "")

	key, source, err := ResolveEnvValue(apiKeyName)
	if err != nil {
		t.Fatalf("ResolveEnvValue error = %v", err)
	}
	if key != "nvapi-from-project-dir" {
		t.Errorf("key = %q, want nvapi-from-project-dir", key)
	}
	if !strings.Contains(source, projectDir) {
		t.Errorf("source = %q, want it to contain %s", source, projectDir)
	}
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

	SetProjectEnvDir("")
	t.Cleanup(func() { SetProjectEnvDir("") })

	t.Chdir(nested)
	t.Setenv(apiKeyName, "")

	key, source, err := ResolveEnvValue(apiKeyName)
	if err != nil {
		t.Fatalf("ResolveEnvValue error = %v", err)
	}
	if key != "nvapi-from-repo-root" {
		t.Errorf("key = %q, want nvapi-from-repo-root", key)
	}
	if !strings.Contains(source, repoDir) {
		t.Errorf("source = %q, want it to contain %s", source, repoDir)
	}
}
