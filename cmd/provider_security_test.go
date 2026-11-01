package cmd

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"forcefield/internal/config"
)

func TestProviderTestBadKeyRefusedWithoutNetwork(t *testing.T) {
	isolateProviderHome(t)
	const secret = "sk-bad\nkey"
	t.Setenv("FF_TEST_CMD_BAD_KEY", secret)
	// No test server: Load resolves the active provider, so a bad
	// credential fails fast at startup without ever dialing.
	writeProviderConfig(t, "model:\n  provider: custom\n  name: m\nproviders:\n  custom:\n    type: openai-compatible\n    base_url: http://127.0.0.1:9/v1\n    api_key_env: FF_TEST_CMD_BAD_KEY\n")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() accepted a CRLF key")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "sk-bad") {
		t.Fatalf("error leaked the key: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "FF_TEST_CMD_BAD_KEY") {
		t.Errorf("error = %q, want it to name the variable", err.Error())
	}
}

func TestProbeAuthHeaderRejectsBadKey(t *testing.T) {
	isolateProviderHome(t)
	t.Setenv("FF_TEST_PROBE_BAD_KEY", "bad\r\nkey")
	writeProviderConfig(t, "model:\n  provider: custom\n  name: m\nproviders:\n  custom:\n    type: openai-compatible\n    base_url: http://127.0.0.1:9/v1\n    api_key_env: FF_TEST_PROBE_BAD_KEY\n")

	// Load itself must already fail, so the probe path is unreachable;
	// probeAuthHeader must also refuse a programmatically built bad key
	// without sending anything.
	if _, err := config.Load(); err == nil {
		t.Fatal("Load() accepted a CRLF key")
	}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:9/models", nil)
	bad := config.ResolvedProvider{ID: "custom", AuthEnvVar: "FF_TEST_PROBE_BAD_KEY", APIKey: "bad\r\nkey"}
	if err := probeAuthHeader(bad, req); err == nil {
		t.Fatal("probeAuthHeader accepted a CRLF key")
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty (nothing sent)", got)
	}
}

func TestProviderTestMetadataOptInPassesGate(t *testing.T) {
	isolateProviderHome(t)
	// With the opt-in, the metadata gate itself must pass. Resolution and
	// spec validation (no network) prove the gate; the live probe is not
	// attempted here because it would dial a real metadata address.
	writeProviderConfig(t, "model:\n  provider: meta\n  name: m\nproviders:\n  meta:\n    type: openai-compatible\n    base_url: http://169.254.169.254/v1\n    allow_metadata_endpoint: true\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() with opt-in error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("meta", "m")
	if err != nil {
		t.Fatalf("ResolveProvider with opt-in error = %v", err)
	}
	if err := resolved.Spec("m").Validate(); err != nil {
		t.Fatalf("Spec.Validate() with opt-in error = %v", err)
	}
}

// captureStdout runs fn with os.Stdout swapped for a pipe, collects
// everything the diagnostics path printed, and restores stdout. The
// provider diagnostics print with fmt.Printf straight to os.Stdout, so
// this captures exactly what a terminal would show.
//
// It is cleanup-safe: the swap is undone and the pipe closed even when
// fn panics, both pipe ends are always closed, pipe errors are checked,
// and the pipe is drained concurrently so fn's writes can never block
// on the pipe's fixed buffer size (a large capture would otherwise
// deadlock). Output printed through os.Stdout is collected even when
// fn errors or panics.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}

	// Drain concurrently and unboundedly (io.Copy into a buffer). Read
	// failures are reported once Copy returns; the reader end is closed
	// here so both descriptors are closed in every path. The buffered
	// send keeps the goroutine from leaking if this helper unwinds via
	// panic before the main path waits on it.
	var buf bytes.Buffer
	drained := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(&buf, r) // sees EOF once the write end closes
		_ = r.Close()
		drained <- copyErr
	}()

	os.Stdout = w
	done := false
	defer func() {
		if !done {
			// fn unwound via panic: restore the real stdout and close
			// the write end so the drain goroutine observes EOF and
			// finishes; the panic then continues unwinding.
			os.Stdout = origStdout
			_ = w.Close()
		}
	}()

	ferr := fn()
	done = true

	os.Stdout = origStdout
	if closeErr := w.Close(); closeErr != nil {
		t.Errorf("close captured stdout pipe: %v", closeErr)
	}
	if copyErr := <-drained; copyErr != nil {
		t.Errorf("collect captured stdout: %v", copyErr)
	}
	return buf.String(), ferr
}

// refuteCredential fails the test if text carries the credential value
// itself, a distinctive fragment of it (so a truncated print is still
// caught), or the auth-scheme-prefixed wire form. Diagnostics may name
// the environment variable; they must never echo the value.
func refuteCredential(t *testing.T, where, text, key, fragment string) {
	t.Helper()
	for _, m := range []string{key, fragment, "Bearer " + key} {
		if strings.Contains(text, m) {
			t.Errorf("%s contains the credential (%q): %q", where, m, text)
		}
	}
}

// TestProviderTestStdoutPrintsVarNameNotValue pins the CodeQL-audited
// invariant for the success path: with a key resolved from env, the
// happy-path stdout may name the variable ("(is set, value hidden)")
// but never carries the key value. The server records the Authorization
// header to prove a real credential was in play during the run.
func TestProviderTestStdoutPrintsVarNameNotValue(t *testing.T) {
	const (
		envName  = "FF_TEST_DIAG_SET_KEY"
		key      = "test-provider-key-alpha"
		fragment = "provider-key-alpha"
	)
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"m1"}]}`)
	}))
	defer srv.Close()

	isolateProviderHome(t)
	t.Setenv(envName, key)
	writeProviderConfig(t, "model:\n  provider: custom\n  name: m1\nproviders:\n  custom:\n    type: openai-compatible\n    base_url: "+srv.URL+"\n    api_key_env: "+envName+"\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	out, err := captureStdout(t, func() error { return runProviderTest(cfg, "custom", false, 0) })
	if err != nil {
		t.Fatalf("runProviderTest() error = %v", err)
	}
	if authHeader != "Bearer "+key {
		t.Fatalf("server Authorization = %q, want the resolved key so the no-leak assertions are meaningful", authHeader)
	}
	if !strings.Contains(out, envName) {
		t.Errorf("stdout = %q, want it to name the variable %q", out, envName)
	}
	if !strings.Contains(out, envName+" is set, value hidden)") {
		t.Errorf("stdout = %q, want the value-hidden marker for a resolved key", out)
	}
	refuteCredential(t, "stdout", out, key, fragment)
}

// TestProviderTestStdoutNeverLeaksKeyOnAuthFailure covers the probe
// failure path: the endpoint answers 401 and echoes the credential back
// like a misbehaving service would. The refusal must be clean on both
// surfaces — the returned error and the captured stdout — with the full
// credential, a distinctive fragment, and the Bearer-prefixed wire form
// all checked; naming the variable is allowed.
func TestProviderTestStdoutNeverLeaksKeyOnAuthFailure(t *testing.T) {
	const (
		envName  = "FF_TEST_DIAG_AUTHFAIL_KEY"
		key      = "test-provider-key-beta"
		fragment = "provider-key-beta"
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+key {
			t.Errorf("Authorization = %q, want the resolved key", got)
		}
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":"bad key %s"}`, key)
	}))
	defer srv.Close()

	isolateProviderHome(t)
	t.Setenv(envName, key)
	writeProviderConfig(t, "model:\n  provider: custom\n  name: m\nproviders:\n  custom:\n    type: openai-compatible\n    base_url: "+srv.URL+"\n    api_key_env: "+envName+"\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	out, err := captureStdout(t, func() error { return runProviderTest(cfg, "custom", false, 0) })
	if err == nil {
		t.Fatal("runProviderTest against 401 succeeded, want failure")
	}
	if !strings.Contains(out, envName) {
		t.Errorf("stdout = %q, want it to name the variable %q", out, envName)
	}
	refuteCredential(t, "error", err.Error(), key, fragment)
	refuteCredential(t, "stdout", out, key, fragment)
}

// TestProviderTestStdoutNamesVarWhenKeyUnset covers the missing-key
// refusal path: diagnostics print "(<var> is not set)" and the error
// names the variable to set. No value exists to leak, but the run must
// still refuse before probing.
func TestProviderTestStdoutNamesVarWhenKeyUnset(t *testing.T) {
	const envName = "FF_TEST_DIAG_UNSET_KEY"

	isolateProviderHome(t)
	t.Setenv(envName, "")
	writeProviderConfig(t, "model:\n  provider: custom\n  name: m\nproviders:\n  custom:\n    type: openai-compatible\n    base_url: http://127.0.0.1:9/v1\n    api_key_env: "+envName+"\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	out, err := captureStdout(t, func() error { return runProviderTest(cfg, "custom", false, 0) })
	if err == nil {
		t.Fatal("runProviderTest with unset key succeeded, want refusal")
	}
	if !strings.Contains(out, "("+envName+" is not set)") {
		t.Errorf("stdout = %q, want the not-set marker for %q", out, envName)
	}
	if !strings.Contains(err.Error(), envName) {
		t.Errorf("error = %q, want it to name the variable", err.Error())
	}
}

// TestProviderTestAzureDiagnosticsPrintVarNameNotValue pins the Azure
// diagnostic branches: Entra bearer setup guidance and the api-key
// warnings may name the credential variable but never print its value.
// Branch coverage is three-way, and credentialSet makes the split
// explicit: "bearer key set" configures api_key_env to resolve a real
// value, so the value-absence assertion applies there; in the two unset
// branches no credential value exists in process memory, so the checked
// contract is the name-based warning itself and checking a
// nonexistent value for absence would be vacuous. Azure path returns
// before probing, so no server is needed.
func TestProviderTestAzureDiagnosticsPrintVarNameNotValue(t *testing.T) {
	const (
		envName  = "FF_TEST_DIAG_AZ_KEY"
		key      = "ffdiag-SECRET-feedbeef87654321"
		fragment = "SECRET-feedbeef87654321"
	)
	for _, tt := range []struct {
		name          string
		authMode      string
		keyValue      string
		credentialSet bool // true: the value below is configured and resolved; false: only the variable is named
		wantMark      string
	}{
		{
			// Credential IS configured and resolves from the
			// environment; the value-hidden branch prints.
			name:          "bearer key set",
			authMode:      "bearer",
			keyValue:      key,
			credentialSet: true,
			wantMark:      "Entra bearer token from " + envName,
		},
		{
			// Credential variable is configured but UNSET; the warning
			// prints the name and tells the user what to populate.
			name:          "bearer key unset",
			authMode:      "bearer",
			keyValue:      "",
			credentialSet: false,
			wantMark:      "Entra mode - " + envName + " is not set",
		},
		{
			// Same unset shape, but for Azure's default api-key
			// authentication instead of the Entra bearer mode.
			name:          "api-key mode key unset",
			authMode:      "",
			keyValue:      "",
			credentialSet: false,
			wantMark:      "warning: " + envName + " is not set",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			isolateProviderHome(t)
			t.Setenv(envName, tt.keyValue)
			authModeLine := ""
			if tt.authMode != "" {
				authModeLine = "\n    auth_mode: " + tt.authMode
			}
			writeProviderConfig(t, "model:\n  provider: azc\n  name: d1\nproviders:\n  azc:\n    type: azure\n    base_url: https://azc.example.openai.azure.com\n    api_key_env: "+envName+authModeLine+"\n")

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			out, err := captureStdout(t, func() error { return runProviderTest(cfg, "azc", false, 0) })
			if err != nil {
				t.Fatalf("runProviderTest() error = %v", err)
			}
			if !strings.Contains(out, tt.wantMark) {
				t.Errorf("stdout = %q, want it to contain %q", out, tt.wantMark)
			}
			if tt.credentialSet {
				// A real value was configured AND resolved during this
				// run, so the printed diagnostics must stay on the
				// name-side of the value-hidden split.
				refuteCredential(t, "stdout", out, key, fragment)
			}
			// credentialSet == false: intentionally no value-absence
			// assertion here — no credential value was configured, so
			// asserting its absence would test nothing; the contract
			// for these branches is the name-based warning above.
		})
	}
}

// TestProviderTestRefusedKeyNamesVarNotValue covers the
// control-character credential refusal, explicitly reached via an
// INACTIVE provider: the malformed value is refused at resolution
// (before any network or print), and the error names the variable,
// never the value.
func TestProviderTestRefusedKeyNamesVarNotValue(t *testing.T) {
	const (
		envName = "FF_TEST_DIAG_BAD_KEY"
		key     = "bad\nsecretCRLF"
	)

	isolateProviderHome(t)
	t.Setenv(envName, key)
	writeProviderConfig(t, "model:\n  provider: ollama\n  name: m\nproviders:\n  badkey:\n    type: openai-compatible\n    base_url: http://127.0.0.1:9/v1\n    api_key_env: "+envName+"\n")

	// Deliberate non-obvious configuration: the ACTIVE provider is the
	// offline ollama preset, so config.Load resolves and validates only
	// ollama and accepts the file — an inactive entry's malformed
	// credential is invisible to Load, and this test cannot rely on
	// Load's fail-closed behavior to exercise it. runProviderTest then
	// targets the inactive "badkey" entry explicitly, pinning the
	// behavior required when a user tests an inactive provider: the
	// malformed credential is still refused, with no network call and
	// no stdout print, and the error names the variable but never
	// echoes the value.
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	out, err := captureStdout(t, func() error { return runProviderTest(cfg, "badkey", false, 0) })
	if err == nil {
		t.Fatal("runProviderTest with control-character key succeeded, want refusal")
	}
	if strings.Contains(err.Error(), "secretCRLF") || strings.Contains(err.Error(), key) {
		t.Errorf("error leaked the refused key: %q", err.Error())
	}
	if !strings.Contains(err.Error(), envName) {
		t.Errorf("error = %q, want it to name the variable", err.Error())
	}
	for _, m := range []string{"secretCRLF", key} {
		if strings.Contains(out, m) {
			t.Errorf("stdout leaked the refused key (%q): %q", m, out)
		}
	}
}
