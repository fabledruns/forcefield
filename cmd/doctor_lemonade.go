package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"forcefield/internal/config"
	"forcefield/internal/providers"
)

const maxLemonadeProbeBytes = 2 << 20

type lemonadeHealthPayload struct {
	Status          string `json:"status"`
	AllModelsLoaded []struct {
		ModelName        string         `json:"model_name"`
		Checkpoint       string         `json:"checkpoint"`
		Type             string         `json:"type"`
		Device           string         `json:"device"`
		Pinned           bool           `json:"pinned"`
		IsBusy           bool           `json:"is_busy"`
		IsStreaming      bool           `json:"is_streaming"`
		Status           string         `json:"status"`
		Recipe           string         `json:"recipe"`
		MaxContextWindow int            `json:"max_context_window"`
		LaunchCommand    []string       `json:"launch_command"`
		RecipeOptions    map[string]any `json:"recipe_options"`
	} `json:"all_models_loaded"`
	PinnedModels map[string]int `json:"pinned_models"`
	MaxModels    map[string]int `json:"max_models"`
}

func lemonadeDoGET(client *http.Client, targetURL string, auth func(*http.Request)) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, nil, err
	}
	if auth != nil {
		auth(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxLemonadeProbeBytes))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func extractLemonadeParallel(launchCmd []string, recipeOpts map[string]any) int {
	for i := 0; i < len(launchCmd); i++ {
		arg := strings.TrimSpace(launchCmd[i])
		if arg == "-np" || arg == "--parallel" {
			if i+1 < len(launchCmd) {
				if n, err := strconv.Atoi(strings.TrimSpace(launchCmd[i+1])); err == nil && n > 0 {
					return n
				}
			}
		}
		for _, prefix := range []string{"-np=", "--parallel="} {
			if strings.HasPrefix(arg, prefix) {
				if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(arg, prefix))); err == nil && n > 0 {
					return n
				}
			}
		}
	}
	if raw, ok := recipeOpts["llamacpp_args"].(string); ok && raw != "" {
		fields := strings.Fields(raw)
		for i := 0; i < len(fields); i++ {
			if (fields[i] == "-np" || fields[i] == "--parallel") && i+1 < len(fields) {
				if n, err := strconv.Atoi(fields[i+1]); err == nil && n > 0 {
					return n
				}
			}
		}
	}
	return 1
}

var wsl2LoopbackHint = providers.WSL2LoopbackConnectionHint

func doctorLemonade(cfg *config.Config, client *http.Client, resolved config.ResolvedProvider, auth func(*http.Request), report func(verdict, string, ...any)) {
	apiBase, rootBase := providers.NormalizeLemonadeEndpoints(resolved.BaseURL)

	// 1. Root /live liveness check (mounted at Prefixes::Root).
	liveURL := rootBase + "/live"
	status, _, err := lemonadeDoGET(client, liveURL, auth)
	if err != nil {
		report(vFail, "%s: could not reach %s (%v)", resolved.Label, liveURL, err)
		if hint := wsl2LoopbackHint(rootBase, err); hint != "" {
			report(vWarn, "%s: %s", resolved.Label, hint)
		}
		return
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		report(vFail, "%s: authentication failed at %s (HTTP %d) - check your API key", resolved.Label, liveURL, status)
		return
	case status == http.StatusNotFound:
		report(vWarn, "%s: %s returned HTTP 404; continuing diagnostics against %s", resolved.Label, liveURL, apiBase)
	case status != http.StatusOK:
		report(vFail, "%s: %s answered HTTP %d", resolved.Label, liveURL, status)
		return
	default:
		report(vOK, "%s: server is live (%s)", resolved.Label, liveURL)
	}

	// 2. /health residency, device offload, parallel slots, and pinned capacity.
	var health lemonadeHealthPayload
	var activeLoaded bool
	var activeCPUFallback bool
	if probeJSON(client, apiBase+"/health", auth, &health, report, resolved.Label) {
		if len(health.AllModelsLoaded) == 0 {
			report(vOK, "%s: health %s, no model currently loaded (auto-loads on first turn)", resolved.Label, health.Status)
		} else {
			for _, m := range health.AllModelsLoaded {
				np := extractLemonadeParallel(m.LaunchCommand, m.RecipeOptions)
				ctxStr := "auto"
				if m.MaxContextWindow > 0 {
					ctxStr = strconv.Itoa(m.MaxContextWindow)
				}
				report(vOK, "%s: loaded %q (recipe=%s, device=%s, status=%s, np=%d, ctx=%s, pinned=%v)",
					resolved.Label, m.ModelName, m.Recipe, m.Device, m.Status, np, ctxStr, m.Pinned)
				if m.ModelName == cfg.Model.Name {
					activeLoaded = true
					if strings.EqualFold(m.Device, "cpu") {
						activeCPUFallback = true
						report(vWarn, "%s: active model %q is running on CPU instead of GPU/NPU - verify CUDA/Vulkan backend", resolved.Label, m.ModelName)
					}
				}
			}
		}

		maxLLM := health.MaxModels["llm"]
		pinnedLLM := health.PinnedModels["llm"]
		if maxLLM > 0 && pinnedLLM >= maxLLM && cfg.Model.Name != "" && !activeLoaded {
			report(vWarn, "%s: pinned LLM slots saturated (%d/%d) while active model %q is unloaded - requests may fail with 409 Conflict",
				resolved.Label, pinnedLLM, maxLLM, cfg.Model.Name)
		}
	}

	// 3. /models?show_all=true downloaded vs un-downloaded verification.
	var modelsResp struct {
		Data []struct {
			ID         string `json:"id"`
			Recipe     string `json:"recipe"`
			Downloaded *bool  `json:"downloaded"`
		} `json:"data"`
	}
	if !probeJSON(client, apiBase+"/models?show_all=true", auth, &modelsResp, report, resolved.Label) {
		return
	}
	downloadedIDs := make([]string, 0, len(modelsResp.Data))
	foundTarget := false
	targetDownloaded := false
	for _, m := range modelsResp.Data {
		if m.ID == "" {
			continue
		}
		isDown := m.Downloaded == nil || *m.Downloaded
		if isDown {
			downloadedIDs = append(downloadedIDs, m.ID)
		}
		if m.ID == cfg.Model.Name {
			foundTarget = true
			targetDownloaded = isDown
		}
	}
	if cfg.Model.Name != "" {
		switch {
		case foundTarget && !targetDownloaded:
			report(vFail, "%s: model %q is in Lemonade catalog but not downloaded locally (downloaded: %s)",
				resolved.Label, cfg.Model.Name, orNone(downloadedIDs))
		case !foundTarget:
			report(vFail, "%s: model %q was not found on Lemonade (downloaded: %s)",
				resolved.Label, cfg.Model.Name, orNone(downloadedIDs))
		default:
			report(vOK, "%s: model %q is available (%d downloaded)", resolved.Label, cfg.Model.Name, len(downloadedIDs))
		}
	} else {
		report(vOK, "%s: %d downloaded models visible (%s)", resolved.Label, len(downloadedIDs), orNone(downloadedIDs))
	}

	// 4. /models/{id}/options resolved_ctx_size check against Forcefield reply reserve.
	if cfg.Model.Name != "" && foundTarget && targetDownloaded {
		optURL := apiBase + "/models/" + url.PathEscape(cfg.Model.Name) + "/options"
		if st, raw, err := lemonadeDoGET(client, optURL, auth); err == nil && st == http.StatusOK {
			var opts struct {
				ResolvedCtxSize int `json:"resolved_ctx_size"`
				Effective       struct {
					CtxSize int `json:"ctx_size"`
				} `json:"effective"`
			}
			if json.Unmarshal(raw, &opts) == nil {
				ctxSize := opts.ResolvedCtxSize
				if ctxSize <= 0 {
					ctxSize = opts.Effective.CtxSize
				}
				if ctxSize > 0 {
					report(vOK, "%s: resolved context window for %q is %d tokens", resolved.Label, cfg.Model.Name, ctxSize)
					reserve := cfg.Agent.ContextReserve
					if reserve <= 0 {
						reserve = providers.DefaultReserveTokens
					}
					if ctxSize <= reserve {
						report(vWarn, "%s: resolved context window (%d tokens) does not exceed reply reserve (%d tokens) - raise ctx_size or lower context_reserve",
							resolved.Label, ctxSize, reserve)
					}
				}
			}
		}
	}

	// 5. Best-effort /system-info and /system-stats hardware diagnostics.
	if st, raw, err := lemonadeDoGET(client, apiBase+"/system-info", auth); err == nil && st == http.StatusOK {
		var sysInfo struct {
			OS      string         `json:"os"`
			Devices map[string]any `json:"devices"`
			Recipes map[string]struct {
				Backends map[string]any `json:"backends"`
			} `json:"recipes"`
		}
		if json.Unmarshal(raw, &sysInfo) == nil {
			var gpuBackends []string
			if llama, ok := sysInfo.Recipes["llamacpp"]; ok {
				for name := range llama.Backends {
					lower := strings.ToLower(name)
					if lower != "cpu" {
						gpuBackends = append(gpuBackends, name)
					}
				}
			}
			if activeCPUFallback && len(gpuBackends) > 0 {
				report(vWarn, "%s: GPU backends detected (%s) while active model uses CPU - switch backend for hardware acceleration",
					resolved.Label, strings.Join(gpuBackends, ", "))
			}
		}
	}

	if st, raw, err := lemonadeDoGET(client, apiBase+"/system-stats", auth); err == nil && st == http.StatusOK {
		var stats struct {
			MemoryGB *float64 `json:"memory_gb"`
			VRAMGB   *float64 `json:"vram_gb"`
			GPUs     []struct {
				Name          string   `json:"name"`
				MemoryTotalGB *float64 `json:"memory_total_gb"`
				MemoryUsedGB  *float64 `json:"memory_used_gb"`
			} `json:"gpus"`
		}
		if json.Unmarshal(raw, &stats) == nil {
			for _, g := range stats.GPUs {
				if g.MemoryTotalGB != nil && *g.MemoryTotalGB > 0 && g.MemoryUsedGB != nil {
					pct := (*g.MemoryUsedGB / *g.MemoryTotalGB) * 100
					report(vOK, "%s: GPU %q VRAM %.2f / %.2f GB (%.0f%% used)",
						resolved.Label, g.Name, *g.MemoryUsedGB, *g.MemoryTotalGB, pct)
					if pct >= 85 {
						report(vWarn, "%s: GPU %q VRAM usage is high (%.0f%%) - keep single-model residency and avoid raising -np",
							resolved.Label, g.Name, pct)
					}
				}
			}
			if len(stats.GPUs) == 0 && stats.VRAMGB != nil {
				report(vOK, "%s: reported VRAM %.2f GB", resolved.Label, *stats.VRAMGB)
			}
		}
	}
}
