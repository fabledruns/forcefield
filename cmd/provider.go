package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/redact"
)

var providerCmd = &cobra.Command{
	Use:   "provider",
	Short: "Manage custom model providers",
	Long: `List, add, test, and remove model providers in config.yaml.

Custom providers are OpenAI-compatible endpoints you run yourself or
subscribe to separately. Secrets never live in config.yaml: api_key_env
names an environment variable or .env key instead.`,
}

var providerListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured and known providers",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		resolved, failures := cfg.ResolveAll(cfg.Model.Name)
		failed := map[string]error{}
		for _, f := range failures {
			failed[f.ID] = f.Err
		}
		for _, r := range resolved {
			marker := " "
			if r.ID == cfg.Model.Provider {
				marker = "*"
			}
			auth := authModeDisplay(r)
			models := strings.Join(r.Models, ", ")
			if models == "" {
				models = "(discovery)"
			}
			// Never print keys or header values.
			fmt.Printf("%s %-16s %-18s %s  auth=%s  models: %s\n", marker, r.ID, r.Type, redact.Scrub(r.BaseURL), auth, redact.Scrub(models))
		}
		for id, err := range failed {
			fmt.Printf("! %-16s unavailable: %s\n", id, redact.Scrub(err.Error()))
		}
		return nil
	},
}

var (
	providerAddName       string
	providerAddType       string
	providerAddBaseURL    string
	providerAddModel      string
	providerAddModels     []string
	providerAddAPIKeyEnv  string
	providerAddAuthMode   string
	providerAddAuthHdr    string
	providerAddAuthSchm   string
	providerAddAPIPath    string
	providerAddTimeout    int
	providerAddForce      bool
	providerAddProject    string
	providerAddLocation   string
	providerAddCredsFile  string
	providerAddRegion     string
	providerAddProfile    string
	providerAddAPIVersion string
)

var providerAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a custom OpenAI-compatible provider",
	RunE: func(cmd *cobra.Command, _ []string) error {
		name := strings.TrimSpace(providerAddName)
		if name == "" {
			return fmt.Errorf("provider name is required (--name)")
		}
		if strings.ContainsAny(name, " \t\r\n") {
			return fmt.Errorf("provider name %q must not contain whitespace", name)
		}
		if len(name) > 64 {
			return fmt.Errorf("provider name %q is too long (max 64 characters)", name)
		}
		baseURL := strings.TrimSpace(providerAddBaseURL)
		typ := strings.TrimSpace(providerAddType)
		if typ == "" {
			typ = "openai-compatible"
		}
		if !providers.IsKnownType(typ) {
			return fmt.Errorf("type %q is not supported (supported: %s)", typ, strings.Join(providers.KnownTypes(), ", "))
		}
		if baseURL == "" && typ != "vertex" && typ != "bedrock" {
			return fmt.Errorf("base URL is required (--base-url)")
		}

		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if cfg.Providers == nil {
			cfg.Providers = map[string]config.ProviderConfig{}
		}
		if _, exists := cfg.Providers[name]; exists {
			if _, preset := providers.PresetByID(name); preset {
				// Overriding a built-in preset id is allowed (it shadows
				// defaults); require --force so it never happens by typo.
				if !providerAddForce {
					return fmt.Errorf("provider %q is a built-in service - pass --force to shadow it with custom settings", name)
				}
			} else if !providerAddForce {
				return fmt.Errorf("provider %q already exists - pass --force to overwrite", name)
			}
		} else if _, preset := providers.PresetByID(name); preset && !providerAddForce {
			// Adding an entry that shadows a preset the user never
			// configured is still a shadow; keep it explicit.
			_ = preset
		}

		entry := config.ProviderConfig{
			Type:            typ,
			BaseURL:         baseURL,
			APIKeyEnv:       strings.TrimSpace(providerAddAPIKeyEnv),
			Model:           strings.TrimSpace(providerAddModel),
			Headers:         nil,
			Models:          append([]string(nil), providerAddModels...),
			APIPath:         strings.TrimSpace(providerAddAPIPath),
			AuthMode:        strings.ToLower(strings.TrimSpace(providerAddAuthMode)),
			AuthHeader:      strings.TrimSpace(providerAddAuthHdr),
			AuthScheme:      strings.TrimSpace(providerAddAuthSchm),
			TimeoutSeconds:  providerAddTimeout,
			Project:         strings.TrimSpace(providerAddProject),
			Location:        strings.TrimSpace(providerAddLocation),
			CredentialsFile: strings.TrimSpace(providerAddCredsFile),
			Region:          strings.TrimSpace(providerAddRegion),
			Profile:         strings.TrimSpace(providerAddProfile),
			APIVersion:      strings.TrimSpace(providerAddAPIVersion),
		}
		// Validate by resolving and building the adapter spec without
		// touching the network. ResolveProvider reads env/.env for the
		// key name but never requires the value to be set.
		probe := *cfg
		if probe.Providers == nil {
			probe.Providers = map[string]config.ProviderConfig{}
		}
		probe.Providers[name] = entry
		resolved, err := probe.ResolveProvider(name, entry.Model)
		if err != nil {
			return fmt.Errorf("invalid provider %q: %w", name, err)
		}
		if err := resolved.Spec(entry.Model).Validate(); err != nil {
			return fmt.Errorf("invalid provider %q: %w", name, err)
		}

		cfg.Providers[name] = entry
		if err := cfg.Save(); err != nil {
			return fmt.Errorf("save provider %q: %w", name, err)
		}
		if typ == "vertex" {
			fmt.Printf("Added provider %q (Vertex AI project %s location %s). Configure ADC (`gcloud auth application-default login` or GOOGLE_APPLICATION_CREDENTIALS), then run `ff provider test %s`.\n",
				name, resolved.Project, resolved.Location, name)
			return nil
		}
		if typ == "bedrock" {
			fmt.Printf("Added provider %q (Bedrock region %s). Configure AWS credentials (environment, shared profile, or IAM role) and grant model access, then run `ff provider test %s`.\n",
				name, resolved.Region, name)
			return nil
		}
		if typ == "azure" {
			fmt.Printf("Added provider %q (Azure resource %s, api-version %s). Model IDs are deployment names; set %s, then run `ff provider test %s`.\n",
				name, baseURL, azureAPIVersionDisplay(resolved.APIVersion), displayKeyName(resolved), name)
			return nil
		}
		fmt.Printf("Added provider %q (%s at %s). Set %s if it needs a key, then run `ff provider test %s`.\n",
			name, typ, baseURL, displayKeyName(resolved), name)
		return nil
	},
}

var (
	providerTestChat    bool
	providerTestTimeout int
)

var providerTestCmd = &cobra.Command{
	Use:   "test [name]",
	Short: "Test a provider connection without starting a session",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		id := cfg.Model.Provider
		if len(args) == 1 {
			id = args[0]
		}
		return runProviderTest(cfg, id, providerTestChat, providerTestTimeout)
	},
}

var providerRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a custom provider entry",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if _, ok := cfg.Providers[name]; !ok {
			return fmt.Errorf("provider %q is not in providers: (built-in services need no removal)", name)
		}
		if name == cfg.Model.Provider {
			return fmt.Errorf("provider %q is active - switch first with `ff provider` selection or /provider, then remove", name)
		}
		delete(cfg.Providers, name)
		if err := cfg.Save(); err != nil {
			return fmt.Errorf("save after remove: %w", err)
		}
		fmt.Printf("Removed provider %q.\n", name)
		return nil
	},
}

func authModeDisplay(r config.ResolvedProvider) string {
	if r.Type == "vertex" {
		return "adc"
	}
	if r.Type == "bedrock" {
		return "aws-chain"
	}
	if r.Type == "azure" {
		if strings.ToLower(strings.TrimSpace(r.AuthMode)) == "bearer" {
			return "entra"
		}
		return "api-key"
	}
	mode := strings.ToLower(strings.TrimSpace(r.AuthMode))
	if mode == "" {
		if r.APIKey != "" || r.AuthRequired || r.AuthEnvVar != "" {
			return "bearer"
		}
		return "none"
	}
	if mode == "header" && r.AuthHeader != "" {
		return "header:" + r.AuthHeader
	}
	return mode
}

func displayKeyName(r config.ResolvedProvider) string {
	if r.AuthEnvVar != "" {
		return r.AuthEnvVar
	}
	return "the API key variable"
}

// azureAPIVersionDisplay renders the configured api-version, naming the
// adapter default when unset.
func azureAPIVersionDisplay(v string) string {
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return "2024-06-01 (adapter default)"
}

// runProviderTest validates configuration, prints redacted diagnostics,
// warns about private/metadata endpoints, and probes GET /models. It never
// prints secrets and never starts an agent session.
func runProviderTest(cfg *config.Config, id string, withChat bool, timeoutOverride int) error {
	resolved, err := cfg.ResolveProvider(id, cfg.Model.Name)
	if err != nil {
		return fmt.Errorf("provider %q: %s", id, redact.Scrub(err.Error()))
	}
	spec := resolved.Spec(cfg.Model.Name)
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("provider %q: %s", id, redact.Scrub(err.Error()))
	}

	hasAuth := resolved.APIKey != ""
	fmt.Printf("Provider: %s (%s)\n", resolved.Label, resolved.ID)
	fmt.Printf("  type: %s\n", resolved.Type)
	if endpoint := strings.TrimRight(resolved.BaseURL, "/"); endpoint != "" {
		fmt.Printf("  endpoint: %s\n", redact.Scrub(endpoint))
	} else if resolved.Type == "bedrock" {
		fmt.Printf("  endpoint: (resolved by the AWS SDK from region)\n")
	}
	fmt.Printf("  auth: %s", authModeDisplay(resolved))
	if resolved.AuthEnvVar != "" {
		if hasAuth {
			fmt.Printf(" (%s is set, value hidden)", resolved.AuthEnvVar)
		} else {
			fmt.Printf(" (%s is not set)", resolved.AuthEnvVar)
		}
	}
	fmt.Printf("\n")
	if spec.ChatPath != "" && spec.ChatPath != providers.DefaultChatPath {
		fmt.Printf("  chat path: %s\n", redact.Scrub(spec.ChatPath))
	}
	if spec.TimeoutSeconds > 0 {
		fmt.Printf("  timeout: %ds\n", spec.TimeoutSeconds)
	}

	for _, w := range providers.EndpointSecurityWarnings(resolved.BaseURL, hasAuth) {
		fmt.Printf("  note: %s\n", redact.Scrub(w))
	}
	if providers.IsMetadataHost(providers.EndpointHost(resolved.BaseURL)) && !resolved.AllowMetadataEndpoint {
		return fmt.Errorf("refusing to probe a cloud metadata endpoint (set allow_metadata_endpoint: true only if you intentionally run a model server there)")
	}
	if providers.IsMetadataHost(providers.EndpointHost(resolved.BaseURL)) && resolved.AllowMetadataEndpoint {
		fmt.Printf("  warning: metadata endpoint explicitly allowed - credentials and prompt content will be sent there\n")
	}
	if !providers.IsValidAPIKey(resolved.APIKey) {
		name := resolved.AuthEnvVar
		if name == "" {
			name = "the API key variable"
		}
		return fmt.Errorf("value in %s contains invalid control characters (check for stray line breaks) and will not be sent", name)
	}

	// Vertex AI has no models-listing API in this integration: validate
	// configuration and describe the ADC chain without billing a call.
	// --chat stays unsupported for vertex (probeChat says so explicitly).
	if resolved.Type == "vertex" {
		fmt.Printf("  project: %s\n", redact.Scrub(resolved.Project))
		fmt.Printf("  location: %s\n", redact.Scrub(resolved.Location))
		if resolved.CredentialsFile != "" {
			fmt.Printf("  credentials: file %s\n", redact.Scrub(resolved.CredentialsFile))
		} else {
			fmt.Printf("  credentials: Application Default Credentials (GOOGLE_APPLICATION_CREDENTIALS, gcloud well-known file, or GCE metadata)\n")
		}
		fmt.Printf("Connection test passed (configuration valid; no billing call made).\n")
		return nil
	}
	// Bedrock likewise has no models-listing probe here (ListFoundationModels
	// needs separate permissions and paginates the whole catalog): validate
	// configuration and describe the credential chain without billing a call.
	if resolved.Type == "bedrock" {
		fmt.Printf("  region: %s\n", redact.Scrub(resolved.Region))
		if resolved.Profile != "" {
			fmt.Printf("  profile: %s\n", redact.Scrub(resolved.Profile))
		} else {
			fmt.Printf("  credentials: standard AWS chain (environment, shared config, SSO, or IAM role)\n")
		}
		if len(resolved.Models) == 0 {
			fmt.Printf("  warning: no models configured under providers.%s.models - set model IDs for this region/account\n", id)
		}
		fmt.Printf("Connection test passed (configuration valid; no billing call made).\n")
		return nil
	}
	// Azure likewise has no models-listing probe here: deployments are
	// user-named and enumerating them would need separate data-plane
	// permissions. Validate configuration and describe the auth path
	// without billing a call; the first model turn names any
	// deployment-not-found issue.
	if resolved.Type == "azure" {
		fmt.Printf("  api-version: %s\n", redact.Scrub(azureAPIVersionDisplay(resolved.APIVersion)))
		if strings.ToLower(strings.TrimSpace(resolved.AuthMode)) == "bearer" {
			if hasAuth {
				fmt.Printf("  credentials: Entra bearer token from %s (value hidden; scope https://cognitiveservices.azure.com/.default)\n", resolved.AuthEnvVar)
			} else {
				fmt.Printf("  warning: Entra mode - %s is not set; run `az account get-access-token --resource https://cognitiveservices.azure.com` and store the token there\n", resolved.AuthEnvVar)
			}
		} else if !hasAuth {
			fmt.Printf("  warning: %s is not set - Azure authenticates keys via the api-key header, or use auth_mode: bearer with an Entra token\n", resolved.AuthEnvVar)
		}
		if spec.Model != "" {
			fmt.Printf("  deployment: %s\n", redact.Scrub(spec.Model))
		}
		if len(resolved.Models) == 0 {
			fmt.Printf("  warning: no models configured under providers.%s.models - list your deployment names for the picker\n", id)
		}
		fmt.Printf("Connection test passed (configuration valid; no billing call made).\n")
		return nil
	}

	if resolved.AuthRequired && !hasAuth {
		return fmt.Errorf("%s requires an API key - set %s, then test again", resolved.Label, displayKeyName(resolved))
	}

	probeTimeout := 10 * time.Second
	if timeoutOverride > 0 {
		if timeoutOverride < 1 || timeoutOverride > 300 {
			return fmt.Errorf("timeout %d is out of range (1-300)", timeoutOverride)
		}
		probeTimeout = time.Duration(timeoutOverride) * time.Second
	} else if spec.TimeoutSeconds > 0 {
		probeTimeout = time.Duration(spec.TimeoutSeconds) * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	client := &http.Client{
		Timeout:       probeTimeout,
		CheckRedirect: nil,
	}
	// Use the same redirect policy as the transport so the test observes
	// what a real turn would do.
	client.CheckRedirect = redirectCheckForTest()

	models, err := probeModels(ctx, client, resolved)
	if err != nil {
		return fmt.Errorf("probe failed: %s", redact.Scrub(err.Error()))
	}
	fmt.Printf("  models visible: %d (%s)\n", len(models), redact.Scrub(orNoneTest(models)))
	if cfg.Model.Provider == id && cfg.Model.Name != "" {
		found := false
		for _, m := range models {
			if m == cfg.Model.Name {
				found = true
				break
			}
		}
		if !found && len(resolved.Models) > 0 {
			for _, m := range resolved.Models {
				if m == cfg.Model.Name {
					found = true
					break
				}
			}
		}
		if !found {
			fmt.Printf("  warning: active model %q was not in the listing - it stays selectable, but verify the ID\n", cfg.Model.Name)
		}
	}

	if withChat {
		fmt.Printf("  note: --chat sends one billable completion (max_tokens=1); skipping by default would avoid cost\n")
		if err := probeChat(ctx, client, resolved); err != nil {
			return fmt.Errorf("chat probe failed: %s", redact.Scrub(err.Error()))
		}
		fmt.Printf("  chat probe: ok\n")
	}

	fmt.Printf("Connection test passed.\n")
	return nil
}

func redirectCheckForTest() func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("too many redirects (max 3)")
		}
		if len(via) == 0 {
			return nil
		}
		if req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("cross-host redirect from %q to %q is not allowed", via[0].URL.Host, req.URL.Host)
		}
		return nil
	}
}

func probeAuthHeader(resolved config.ResolvedProvider, req *http.Request) error {
	mode := strings.ToLower(strings.TrimSpace(resolved.AuthMode))
	key := resolved.APIKey
	if key != "" && !providers.IsValidAPIKey(key) {
		name := resolved.AuthEnvVar
		if name == "" {
			name = "the API key variable"
		}
		return fmt.Errorf("value in %s contains invalid control characters (check for stray line breaks) and will not be sent", name)
	}
	switch mode {
	case "none":
	case "header":
		if key != "" && resolved.AuthHeader != "" {
			value := key
			if resolved.AuthScheme != "" {
				value = resolved.AuthScheme + " " + key
			}
			req.Header.Set(resolved.AuthHeader, value)
		}
	default:
		if key != "" {
			scheme := "Bearer"
			if resolved.AuthScheme != "" {
				scheme = resolved.AuthScheme
			}
			req.Header.Set("Authorization", scheme+" "+key)
		}
	}
	for name, value := range resolved.Headers {
		if !providers.IsValidHeaderValue(value) {
			continue
		}
		req.Header.Set(name, value)
	}
	return nil
}

func probeModels(ctx context.Context, client *http.Client, resolved config.ResolvedProvider) ([]string, error) {
	base := strings.TrimRight(resolved.BaseURL, "/")
	var path string
	switch resolved.Type {
	case "ollama":
		path = "/api/tags"
	case "anthropic":
		path = "/v1/models"
	case "gemini":
		path = "/v1beta/models"
	default:
		path = "/models"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, fmt.Errorf("bad endpoint URL: %w", err)
	}
	if err := probeAuthHeader(resolved, req); err != nil {
		return nil, err
	}
	if resolved.Type == "anthropic" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s (%v)", base+path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("authentication failed (HTTP %d) - check your API key", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("endpoint answered HTTP %d", resp.StatusCode)
	}
	// Decode the common shapes; unknown shapes still count as reachable
	// with zero enumerated models rather than a failure.
	var openaiShape struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&openaiShape); err != nil {
		return nil, fmt.Errorf("endpoint returned malformed JSON (%v)", err)
	}
	out := make([]string, 0, len(openaiShape.Data))
	for _, m := range openaiShape.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out, nil
}

func probeChat(ctx context.Context, client *http.Client, resolved config.ResolvedProvider) error {
	if resolved.Type != "openai-compatible" {
		return fmt.Errorf("chat probe is only supported for openai-compatible providers")
	}
	base := strings.TrimRight(resolved.BaseURL, "/")
	chatPath := resolved.ChatPath
	if chatPath == "" {
		chatPath = providers.DefaultChatPath
	}
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"max_tokens":1,"stream":false}`, resolved.Model)
	if body == "" {
		return fmt.Errorf("no model configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+chatPath, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := probeAuthHeader(resolved, req); err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("chat endpoint answered HTTP %d", resp.StatusCode)
	}
	return nil
}

func orNoneTest(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	if len(items) > 10 {
		return strings.Join(items[:10], ", ") + fmt.Sprintf(", … (%d total)", len(items))
	}
	return strings.Join(items, ", ")
}

func init() {
	providerAddCmd.Flags().StringVar(&providerAddName, "name", "", "provider key used in config (e.g. my-local)")
	providerAddCmd.Flags().StringVar(&providerAddType, "type", "openai-compatible", "wire protocol (default openai-compatible)")
	providerAddCmd.Flags().StringVar(&providerAddBaseURL, "base-url", "", "API root (e.g. http://localhost:1234/v1)")
	providerAddCmd.Flags().StringVar(&providerAddModel, "model", "", "default model ID for this provider")
	providerAddCmd.Flags().StringSliceVar(&providerAddModels, "models", nil, "model IDs offered without discovery (repeatable or comma-separated)")
	providerAddCmd.Flags().StringVar(&providerAddAPIKeyEnv, "api-key-env", "", "env/.env variable holding the API key (empty = none)")
	providerAddCmd.Flags().StringVar(&providerAddAuthMode, "auth-mode", "", "bearer|none|header (empty = auto)")
	providerAddCmd.Flags().StringVar(&providerAddAuthHdr, "auth-header", "", "custom header name for --auth-mode header")
	providerAddCmd.Flags().StringVar(&providerAddAuthSchm, "auth-scheme", "", "credential prefix override (default Bearer/raw)")
	providerAddCmd.Flags().StringVar(&providerAddAPIPath, "api-path", "", "chat path override (default /chat/completions)")
	providerAddCmd.Flags().IntVar(&providerAddTimeout, "timeout", 0, "response-header timeout in seconds (0 = default 120)")
	providerAddCmd.Flags().StringVar(&providerAddProject, "project", "", "Google Cloud project ID (--type vertex)")
	providerAddCmd.Flags().StringVar(&providerAddLocation, "location", "", "Vertex AI location, e.g. us-central1 (--type vertex)")
	providerAddCmd.Flags().StringVar(&providerAddCredsFile, "credentials-file", "", "service-account JSON key for this provider only (--type vertex)")
	providerAddCmd.Flags().StringVar(&providerAddRegion, "region", "", "AWS region, e.g. us-east-1 (--type bedrock)")
	providerAddCmd.Flags().StringVar(&providerAddProfile, "profile", "", "AWS shared-config profile (--type bedrock)")
	providerAddCmd.Flags().StringVar(&providerAddAPIVersion, "api-version", "", "Azure data-plane api-version, e.g. 2024-06-01 (--type azure)")
	providerAddCmd.Flags().BoolVar(&providerAddForce, "force", false, "overwrite an existing provider entry")
	providerTestCmd.Flags().BoolVar(&providerTestChat, "chat", false, "also send one max_tokens=1 chat ping (may bill)")
	providerTestCmd.Flags().IntVar(&providerTestTimeout, "timeout", 0, "probe timeout in seconds (0 = provider timeout or 10s)")
	providerCmd.AddCommand(providerListCmd)
	providerCmd.AddCommand(providerAddCmd)
	providerCmd.AddCommand(providerTestCmd)
	providerCmd.AddCommand(providerRemoveCmd)
	rootCmd.AddCommand(providerCmd)
}
