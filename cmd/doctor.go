package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"forcefield/internal/config"
	"forcefield/internal/memory"
	"forcefield/internal/sandbox"
	"forcefield/internal/session"
	"forcefield/internal/skills"
	"forcefield/internal/tools/shell"
)

// probeTimeout bounds each network reachability check in ff doctor so a
// dead provider fails fast instead of hanging the whole diagnosis.
const probeTimeout = 3 * time.Second

type verdict int

const (
	vOK verdict = iota
	vWarn
	vFail
)

func (v verdict) String() string {
	switch v {
	case vWarn:
		return "[warn]"
	case vFail:
		return "[FAIL]"
	default:
		return "[ ok ]"
	}
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose common local problems",
	Long: `Doctor checks the pieces Forcefield depends on - configuration,
model providers, session storage, skills, project memory, and the shell
backend - and reports anything that would break a session.

It never prints secret values such as API keys.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		failed := false

		report := func(v verdict, format string, args ...any) {
			if v == vFail {
				failed = true
			}
			fmt.Printf("%s %s\n", v, fmt.Sprintf(format, args...))
		}

		cfg := doctorConfig(report)
		doctorAPIKey(cfg, report)
		doctorProvider(cfg, report)
		doctorSessions(report)
		doctorSkills(report)
		doctorMemory(report)
		doctorShell(report)
		doctorSandbox(cfg, report)

		if failed {
			fmt.Println("\nProblems found. Fix the [FAIL] items above and run `ff doctor` again.")
			os.Exit(1)
		}
		fmt.Println("\nAll checks passed.")
		return nil
	},
}

// doctorConfig loads and validates config.yaml, reporting its path and
// the active model settings on success.
func doctorConfig(report func(verdict, string, ...any)) *config.Config {
	path, err := config.Path()
	if err != nil {
		report(vFail, "config: resolve home directory: %v", err)
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		report(vFail, "config at %s is invalid: %v", path, err)
		return nil
	}

	resolved, err := cfg.ResolveProvider(cfg.Model.Provider, cfg.Model.Name)
	endpoint := ""
	if err == nil {
		endpoint = resolved.BaseURL
	}
	if endpoint == "" {
		endpoint = cfg.Model.Endpoint
	}
	report(vOK, "config: %s (%s provider, model %q)", path, cfg.Model.Provider, cfg.Model.Name)
	if endpoint == "" {
		report(vFail, "config: no endpoint resolves for provider %q", cfg.Model.Provider)
	}
	return cfg
}

// doctorAPIKey reports where the active provider's API key would come
// from when it needs one - naming only its presence, never its value.
func doctorAPIKey(cfg *config.Config, report func(verdict, string, ...any)) {
	if cfg == nil {
		return
	}
	resolved, err := cfg.ResolveProvider(cfg.Model.Provider, cfg.Model.Name)
	if err != nil {
		return // already reported by doctorConfig/doctorProvider
	}
	if !resolved.AuthRequired || resolved.AuthEnvVar == "" {
		return
	}

	switch {
	case resolved.APIKeySource == "":
		report(vWarn, "%s: %s is not set (environment or .env); requests will fail until you provide it",
			resolved.Label, resolved.AuthEnvVar)
	case resolved.APIKeySource == "environment":
		report(vOK, "%s: %s is set via the environment (value hidden)",
			resolved.Label, resolved.AuthEnvVar)
	default:
		report(vOK, "%s: %s found in %s (value hidden)", resolved.Label, resolved.AuthEnvVar, resolved.APIKeySource)
	}
}

// doctorProvider probes the configured provider's endpoint for
// reachability and, where the protocol allows, confirms the configured
// model actually exists there. The probe is chosen by the provider's wire
// protocol, so every OpenAI-compatible service gets the same check.
func doctorProvider(cfg *config.Config, report func(verdict, string, ...any)) {
	if cfg == nil {
		return
	}

	resolved, err := cfg.ResolveProvider(cfg.Model.Provider, cfg.Model.Name)
	if err != nil {
		report(vFail, "provider %q: %v", cfg.Model.Provider, err)
		return
	}

	// A required key that is absent turns every probe into a bare 401;
	// say that once instead of probing.
	if resolved.AuthRequired && resolved.APIKey == "" {
		report(vWarn, "%s: skipping reachability probe while %s is unset", resolved.Label, resolved.AuthEnvVar)
		return
	}

	client := &http.Client{Timeout: probeTimeout}
	base := strings.TrimRight(resolved.BaseURL, "/")

	auth := func(req *http.Request) {}
	switch resolved.Type {
	case "ollama":
		auth = func(req *http.Request) {}
	case "openai-compatible":
		key := resolved.APIKey
		auth = func(req *http.Request) {
			if key != "" {
				req.Header.Set("Authorization", "Bearer "+key)
			}
		}
	case "anthropic":
		key := resolved.APIKey
		auth = func(req *http.Request) {
			if key != "" {
				req.Header.Set("x-api-key", key)
			}
			req.Header.Set("anthropic-version", "2023-06-01")
		}
	case "gemini":
		key := resolved.APIKey
		auth = func(req *http.Request) {
			if key != "" {
				req.Header.Set("x-goog-api-key", key)
			}
		}
	default:
		report(vWarn, "provider %q has no reachability check; Forcefield will error clearly if it cannot connect", resolved.ID)
		return
	}

	switch resolved.Type {
	case "ollama":
		var body struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if !probeJSON(client, base+"/api/tags", auth, &body, report, resolved.Label) {
			return
		}
		names := make([]string, 0, len(body.Models))
		found := false
		for _, m := range body.Models {
			names = append(names, m.Name)
			if m.Name == cfg.Model.Name {
				found = true
			}
		}
		if !found {
			report(vFail, "%s: model %q is not installed - run `ollama pull %s` (installed: %s)",
				resolved.Label, cfg.Model.Name, cfg.Model.Name, orNone(names))
			return
		}
		report(vOK, "%s: model %q is available (%d installed)", resolved.Label, cfg.Model.Name, len(names))

	case "openai-compatible":
		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if !probeJSON(client, base+"/models", auth, &body, report, resolved.Label) {
			return
		}
		ids := make([]string, 0, len(body.Data))
		for _, m := range body.Data {
			ids = append(ids, m.ID)
		}
		report(vOK, "%s: server is up (%d models visible: %s)", resolved.Label, len(ids), orNone(ids))

	case "anthropic":
		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if !probeJSON(client, base+"/v1/models", auth, &body, report, resolved.Label) {
			return
		}
		report(vOK, "%s: API accepted the request (%d models visible)", resolved.Label, len(body.Data))

	case "gemini":
		var body struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if !probeJSON(client, base+"/v1beta/models", auth, &body, report, resolved.Label) {
			return
		}
		report(vOK, "%s: API accepted the request (%d models visible)", resolved.Label, len(body.Models))
	}
}

// probeJSON performs one authenticated-optional GET and decodes a JSON
// body, reporting a FAIL line with an actionable message when any step
// breaks. The auth hook stamps provider-specific credentials onto the
// request. It reports whether the full check succeeded.
func probeJSON(client *http.Client, url string, auth func(*http.Request), into any, report func(verdict, string, ...any), service string) bool {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		report(vFail, "%s: bad endpoint URL %s: %v", service, url, err)
		return false
	}
	auth(req)

	resp, err := client.Do(req)
	if err != nil {
		if strings.Contains(service, "Ollama") {
			report(vFail, "%s: could not reach %s - is `ollama serve` running?", service, url)
		} else if strings.Contains(service, "LM Studio") {
			report(vFail, "%s: could not reach %s - is the LM Studio local server running?", service, url)
		} else {
			report(vFail, "%s: could not reach %s (%v)", service, url, err)
		}
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		report(vFail, "%s: authentication failed (HTTP %d) - check your API key", service, resp.StatusCode)
		return false
	}
	if resp.StatusCode != http.StatusOK {
		report(vFail, "%s: %s answered HTTP %d", service, url, resp.StatusCode)
		return false
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		report(vFail, "%s: %s returned a malformed JSON body (%v)", service, url, err)
		return false
	}
	return true
}

func orNone(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	return strings.Join(items, ", ")
}

// doctorSessions verifies session storage is readable and calls out any
// corrupted files by name.
func doctorSessions(report func(verdict, string, ...any)) {
	sessions, corrupt, err := session.ListCorrupt()
	if err != nil {
		report(vFail, "sessions: cannot read the sessions directory: %v", err)
		return
	}
	for _, c := range corrupt {
		report(vWarn, "sessions: unreadable file will be skipped by /sessions: %s", c.Error())
	}
	report(vOK, "sessions: %d readable, %d unreadable", len(sessions), len(corrupt))
}

// doctorSkills verifies the skill directory loads and reports how many
// skills are available.
func doctorSkills(report func(verdict, string, ...any)) {
	home, err := config.Dir()
	if err != nil {
		report(vFail, "skills: resolve home directory: %v", err)
		return
	}

	store, err := skills.New(home)
	if err != nil {
		report(vFail, "skills: loading skill store from %s failed: %v", filepath.Join(home, "skills"), err)
		return
	}
	catalog := store.Catalog()
	report(vOK, "skills: %d loaded from %s", len(catalog), filepath.Join(home, "skills"))
}

// doctorMemory verifies the current project's memory file parses.
func doctorMemory(report func(verdict, string, ...any)) {
	home, err := config.Dir()
	if err != nil {
		report(vFail, "memory: resolve home directory: %v", err)
		return
	}

	store, err := memory.CurrentProjectStore(home)
	if err != nil {
		report(vFail, "memory: %v", err)
		return
	}
	entries, err := store.Load()
	if err != nil {
		report(vFail, "memory: %v", err)
		return
	}
	report(vOK, "memory: %d entries in %s", len(entries), store.Path())
}

// doctorShell verifies the Bash execution backend (WSL on Windows).
func doctorShell(report func(verdict, string, ...any)) {
	sh := shell.NewShell()
	if err := sh.CheckBackend(context.Background()); err != nil {
		report(vFail, "shell backend: %v", err)
		return
	}
	report(vOK, "shell backend: ready")
}

// doctorSandbox reports the configured execution boundary and whether its
// backend can actually deliver it. Every fact comes from the executor's
// own Enforcement report, so doctor can never advertise more isolation
// than exists. WSL mode that is configured but unusable is a hard failure:
// Forcefield would refuse to run commands, and doctor must say why.
func doctorSandbox(cfg *config.Config, report func(verdict, string, ...any)) {
	if cfg == nil {
		// Config already reported as [FAIL]; nothing further to diagnose,
		// and defaulting would mask the real problem.
		return
	}

	mode := cfg.Sandbox.Mode
	if mode == "" {
		mode = string(sandbox.ModeNative)
	}

	executor, err := newSandboxExecutor(cfg)
	if err != nil {
		report(vFail, "sandbox: %v", err)
		return
	}

	if err := executor.Probe(context.Background()); err != nil {
		report(vFail, "sandbox: %s backend unavailable: %v (commands will refuse to run; no fallback exists)",
			sandbox.Mode(mode).DisplayName(), err)
		return
	}

	// Limitations the executor itself declares ("NOT enforced",
	// "NOT blocked") must not read as all-clear: surface them as warnings.
	for _, line := range executor.Describe(context.Background()).SummaryLines() {
		v := vOK
		if strings.Contains(line, "NOT ") {
			v = vWarn
		}
		report(v, "sandbox %s", line)
	}
}

// newSandboxExecutor builds the same executor runtime.New uses, from the
// loaded config alone. Kept separate from runtime so doctor can diagnose a
// broken sandbox configuration without booting a full Runtime.
func newSandboxExecutor(cfg *config.Config) (sandbox.Executor, error) {
	mode, err := sandbox.ParseMode(cfg.Sandbox.Mode)
	if err != nil {
		return nil, fmt.Errorf("invalid sandbox.mode: %w", err)
	}
	network, err := sandbox.ParseNetwork(cfg.Sandbox.WSL.Network)
	if err != nil {
		return nil, fmt.Errorf("invalid sandbox.wsl.network: %w", err)
	}
	workspace := ""
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		if root, rootErr := memory.ProjectRoot(cwd); rootErr == nil {
			workspace = root
		} else {
			workspace = cwd
		}
	}
	return sandbox.NewExecutor(sandbox.Policy{
		Mode:      mode,
		Workspace: workspace,
		Distro:    cfg.Sandbox.WSL.Distribution,
		Network:   network,
	})
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}
