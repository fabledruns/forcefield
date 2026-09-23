package mcp

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFingerprintStableAndSensitive(t *testing.T) {
	a := Config{Servers: map[string]ServerConfig{
		"b": {Command: "/bin/x", Args: []string{"1"}, Env: map[string]string{"K": "v1"}},
		"a": {Command: "/bin/y"},
	}}
	b := Config{Servers: map[string]ServerConfig{
		"a": {Command: "/bin/y"},
		"b": {Command: "/bin/x", Args: []string{"1"}, Env: map[string]string{"K": "v1"}},
	}}
	if FingerprintConfig(a) != FingerprintConfig(b) {
		t.Error("fingerprint must be order-independent")
	}
	if FingerprintConfig(a) == "" {
		t.Error("empty fingerprint")
	}
	changed := Config{Servers: map[string]ServerConfig{
		"a": {Command: "/bin/y"},
		"b": {Command: "/bin/x", Args: []string{"1"}, Env: map[string]string{"K": "v2"}},
	}}
	if FingerprintConfig(a) == FingerprintConfig(changed) {
		t.Error("env value change must alter the fingerprint")
	}
	// Enabled nil normalizes to true.
	tru := true
	explicit := Config{Servers: map[string]ServerConfig{"a": {Command: "/bin/y", Enabled: &tru}}}
	implicit := Config{Servers: map[string]ServerConfig{"a": {Command: "/bin/y"}}}
	if FingerprintConfig(explicit) != FingerprintConfig(implicit) {
		t.Error("nil Enabled must normalize to true")
	}
}

func writeTestStatus(t *testing.T, dir string, st StatusFile) {
	t.Helper()
	if err := WriteStatusFile(dir, st); err != nil {
		t.Fatalf("WriteStatusFile: %v", err)
	}
}

func TestStatusRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Servers: map[string]ServerConfig{"demo": {Command: "/bin/x"}}}
	st := StatusFile{
		Version:      StatusVersion,
		ConfigSHA256: FingerprintConfig(cfg),
		UpdatedUnix:  1700000000,
		Servers: []ServerStatus{
			{Key: "demo", Enabled: true, Ready: true, Healthy: true, Started: true, Tools: []string{"mcp__demo__echo"}},
		},
	}
	writeTestStatus(t, dir, st)
	got, err := ReadStatusFile(dir)
	if err != nil {
		t.Fatalf("ReadStatusFile: %v", err)
	}
	if !got.CurrentFor(cfg) {
		t.Error("freshly written status must be current")
	}
	if len(got.Servers) != 1 || got.Servers[0].Key != "demo" || !got.Servers[0].Healthy {
		t.Errorf("round trip mismatch: %+v", got.Servers)
	}
	info, err := os.Stat(StatusFilePath(dir))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestStatusStaleOnConfigChange(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Servers: map[string]ServerConfig{"demo": {Command: "/bin/x"}}}
	writeTestStatus(t, dir, StatusFile{Version: StatusVersion, ConfigSHA256: FingerprintConfig(cfg)})
	changed := Config{Servers: map[string]ServerConfig{"demo": {Command: "/bin/y"}}}
	got, err := ReadStatusFile(dir)
	if err != nil {
		t.Fatalf("ReadStatusFile: %v", err)
	}
	if got.CurrentFor(changed) {
		t.Error("changed command must make status stale")
	}
	// Disabled flag participates in freshness.
	off := false
	disabled := Config{Servers: map[string]ServerConfig{"demo": {Command: "/bin/x", Enabled: &off}}}
	if got.CurrentFor(disabled) {
		t.Error("disabling a server must make status stale")
	}
}

func TestStatusMissing(t *testing.T) {
	_, err := ReadStatusFile(t.TempDir())
	if !errors.Is(err, ErrStatusNotFound) {
		t.Errorf("err = %v, want ErrStatusNotFound", err)
	}
}

func TestStatusCorrupt(t *testing.T) {
	dir := t.TempDir()
	mkfile := func(body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".forcefield"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(StatusFilePath(dir), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"garbage":     "{not json",
		"array":       `[]`,
		"bad-version": `{"version":99,"servers":[]}`,
		"bad-shape":   `{"version":1,"servers":"nope"}`,
	} {
		t.Run(name, func(t *testing.T) {
			mkfile(body)
			_, err := ReadStatusFile(dir)
			if err == nil || errors.Is(err, ErrStatusNotFound) {
				t.Errorf("err = %v, want corrupt error", err)
			}
		})
	}
}

func TestStatusBoundsAndNoSecrets(t *testing.T) {
	dir := t.TempDir()
	secret := "status-probe-secret-value-abcdef"
	cfg := Config{Servers: map[string]ServerConfig{
		"demo": {Command: "/bin/x --token=" + secret, Env: map[string]string{"API_TOKEN": secret}},
	}}
	var tools []string
	for i := 0; i < MaxStatusTools+50; i++ {
		tools = append(tools, "mcp__demo__tool")
	}
	st := StatusFile{
		Version:      StatusVersion,
		ConfigSHA256: FingerprintConfig(cfg),
		Servers: []ServerStatus{{
			Key: "demo", Enabled: true, Ready: true, Healthy: true,
			Tools:      tools,
			LastError:  strings.Repeat("e", MaxErrorDetailRunes+5000),
			StderrTail: strings.Repeat("s", MaxStatusStderrChars+5000),
		}},
	}
	// NewStatusFile path caps tools; direct struct needs no cap here, but
	// the file must still never carry secrets.
	writeTestStatus(t, dir, st)
	raw, err := os.ReadFile(StatusFilePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{secret, "API_TOKEN"} {
		if strings.Contains(string(raw), needle) {
			t.Errorf("status file leaks %q", needle)
		}
	}
	// Fingerprint covers values without storing them: changing the secret
	// alters the hash while the file holds no trace of it.
	changed := Config{Servers: map[string]ServerConfig{
		"demo": {Command: "/bin/x --token=" + secret, Env: map[string]string{"API_TOKEN": "other"}},
	}}
	if FingerprintConfig(cfg) == FingerprintConfig(changed) {
		t.Error("secret rotation must alter the fingerprint")
	}
}

func TestNewStatusFileCaps(t *testing.T) {
	snap := HostSnapshot{Servers: []ServerSnapshot{
		{
			Key: "b", Enabled: true, Ready: true, Healthy: true, Started: true,
			LastError: strings.Repeat("e", MaxErrorDetailRunes+100),
			Stderr:    strings.Repeat("s", MaxStatusStderrChars+100),
			Tools:     nil,
		},
		{Key: "a", Enabled: false},
	}}
	st := NewStatusFile(snap, Config{}, 42)
	if len(st.Servers) != 2 || st.Servers[0].Key != "a" || st.Servers[1].Key != "b" {
		t.Errorf("servers not sorted: %+v", st.Servers)
	}
	if len([]rune(st.Servers[1].LastError)) > MaxErrorDetailRunes {
		t.Error("LastError unbounded")
	}
	if len([]rune(st.Servers[1].StderrTail)) > MaxStatusStderrChars {
		t.Error("StderrTail unbounded")
	}
	if st.Version != StatusVersion || st.UpdatedUnix != 42 {
		t.Errorf("metadata = %+v", st)
	}
	var nilFile *StatusFile
	if nilFile.CurrentFor(Config{}) {
		t.Error("nil status must never be current")
	}
}

func TestWriteStatusFailure(t *testing.T) {
	// A path that is a file, not a dir, makes MkdirAll fail.
	f, err := os.CreateTemp("", "mcp-status-blocker-*")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	if err := WriteStatusFile(path, StatusFile{Version: StatusVersion}); err == nil {
		t.Error("expected write failure through a file path")
	}
}
