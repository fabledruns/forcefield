package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	slotGuardHealthTTL        = 2 * time.Second
	slotGuardDegradedCooldown = 5 * time.Second
	slotGuardSlotsTimeout     = 500 * time.Millisecond
	slotGuardPollInterval     = 100 * time.Millisecond
)

// SlotStatsProvider exposes numeric SlotGuard telemetry for a provider and its
// shared run-scoped coordinator without leaking request or response content.
type SlotStatsProvider interface {
	SlotStats() SlotGuardStats
}

// RunSlotStatsProvider exposes numeric SlotGuard telemetry scoped strictly to a
// single cloned run instance (parent or child agent) across its own turns.
type RunSlotStatsProvider interface {
	RunSlotStats() SlotGuardStats
}

// SlotGuardStats holds purely numeric counters summarizing SlotGuard admission
// and queue behavior across a LemonadeProvider and its run-scoped clones.
type SlotGuardStats struct {
	Admitted          uint64
	Waits             uint64
	TotalWaitMS       int64
	DegradedFallbacks uint64
	InFlight          int
}

type slotGuardHealthModel struct {
	ModelName     string
	Recipe        string
	Status        string
	IsBusy        bool
	IsStreaming   bool
	Pinned        bool
	ParallelSlots int
}

type slotGuardHealthSnapshot struct {
	Loaded      map[string]slotGuardHealthModel
	LoadedOrder []slotGuardHealthModel
	MaxLLM      int
	PinnedLLM   int
	PinnedNames []string
	FetchedAt   time.Time
	FetchedGen  uint64
}

type slotLease struct {
	guard  *lemonadeSlotGuard
	model  string
	waitMS int64
	once   sync.Once
}

func (l *slotLease) WaitMS() int64 {
	if l == nil {
		return 0
	}
	return l.waitMS
}

func (l *slotLease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.guard != nil {
			l.guard.release(l.model)
		}
	})
}

// lemonadeSlotGuard coordinates concurrent Forcefield inference turns against a
// single Lemonade server endpoint. It is shared across all CloneForRun() child
// adapters so multi-agent fan-out respects Lemonade's active slot count (-np),
// serializes conflicting model switches safely without deadlocking on Forcefield's
// own in-flight turns, fails fast on pinned residency saturation, and degrades
// fail-open when /health is unavailable behind a proxy.
type lemonadeSlotGuard struct {
	apiBase       string
	providerLabel string
	client        *http.Client
	slotsTimeout  time.Duration
	probeTimeout  time.Duration
	pollInterval  time.Duration
	newReq        func(ctx context.Context, method, fullURL, accept string, body io.Reader) (*http.Request, error)
	now           func() time.Time

	mu              sync.Mutex
	notifyCh        chan struct{}
	localInFlight   map[string]int
	releaseGen      uint64
	waitQueue       []uint64
	nextTicket      uint64
	cachedHealth    *slotGuardHealthSnapshot
	degradedUntil   time.Time
	healthFetching  bool
	healthFetchDone chan struct{}
	stats           SlotGuardStats
}

func newLemonadeSlotGuard(
	apiBase, providerLabel string,
	client *http.Client,
	newReq func(ctx context.Context, method, fullURL, accept string, body io.Reader) (*http.Request, error),
	now func() time.Time,
) *lemonadeSlotGuard {
	if client == nil {
		client = &http.Client{Timeout: lemonadeMetaTimeout, CheckRedirect: safeRedirectCheck}
	}
	probeTimeout := client.Timeout
	if probeTimeout <= 0 {
		probeTimeout = lemonadeMetaTimeout
	}
	slotsTimeout := slotGuardSlotsTimeout
	if probeTimeout < slotsTimeout {
		slotsTimeout = probeTimeout
	}
	if now == nil {
		now = time.Now
	}
	return &lemonadeSlotGuard{
		apiBase:       strings.TrimRight(apiBase, "/"),
		providerLabel: providerLabel,
		client:        client,
		slotsTimeout:  slotsTimeout,
		probeTimeout:  probeTimeout,
		pollInterval:  slotGuardPollInterval,
		newReq:        newReq,
		now:           now,
		notifyCh:      make(chan struct{}),
		localInFlight: make(map[string]int),
	}
}

func (g *lemonadeSlotGuard) Stats() SlotGuardStats {
	if g == nil {
		return SlotGuardStats{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.stats
	out.InFlight = g.totalLocalInFlightLocked()
	return out
}

func (g *lemonadeSlotGuard) broadcastLocked() {
	close(g.notifyCh)
	g.notifyCh = make(chan struct{})
}

func (g *lemonadeSlotGuard) totalLocalInFlightLocked() int {
	total := 0
	for _, n := range g.localInFlight {
		total += n
	}
	return total
}

func (g *lemonadeSlotGuard) otherLocalInFlightLocked(target string) (totalTurns int, distinctModels int) {
	for mdl, n := range g.localInFlight {
		if mdl == target || n <= 0 {
			continue
		}
		totalTurns += n
		distinctModels++
	}
	return totalTurns, distinctModels
}

func (g *lemonadeSlotGuard) enqueueWaiter() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nextTicket++
	ticket := g.nextTicket
	g.waitQueue = append(g.waitQueue, ticket)
	return ticket
}

func (g *lemonadeSlotGuard) removeWaiter(ticket uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	idx := -1
	for i, id := range g.waitQueue {
		if id == ticket {
			idx = i
			break
		}
	}
	if idx == -1 {
		return
	}
	wasHead := idx == 0
	g.waitQueue = append(g.waitQueue[:idx], g.waitQueue[idx+1:]...)
	if wasHead {
		g.broadcastLocked()
	}
}

func (g *lemonadeSlotGuard) isHeadLocked(ticket uint64) bool {
	return len(g.waitQueue) > 0 && g.waitQueue[0] == ticket
}

func (g *lemonadeSlotGuard) release(model string) {
	g.mu.Lock()
	if n := g.localInFlight[model]; n <= 1 {
		delete(g.localInFlight, model)
	} else {
		g.localInFlight[model] = n - 1
	}
	g.releaseGen++
	g.broadcastLocked()
	g.mu.Unlock()
}

func (g *lemonadeSlotGuard) Acquire(ctx context.Context, modelID string) (*slotLease, error) {
	if g == nil {
		return &slotLease{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	modelID = strings.TrimSpace(modelID)
	ticket := g.enqueueWaiter()
	defer g.removeWaiter(ticket)

	var waited bool
	var waitStart time.Time
	recordWait := func() {
		if !waited {
			waited = true
			waitStart = g.now()
		}
	}

	forceRefresh := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Preserve strict FIFO ordering so competing turns do not starve switches.
		g.mu.Lock()
		if !g.isHeadLocked(ticket) {
			ch := g.notifyCh
			g.mu.Unlock()
			recordWait()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		g.mu.Unlock()

		snap, degraded, err := g.getHealthSnapshot(ctx, forceRefresh)
		forceRefresh = false
		if err != nil {
			return nil, err
		}

		g.mu.Lock()
		if !g.isHeadLocked(ticket) {
			// Another waiter check shouldn't overtake us, but re-verify defensively.
			ch := g.notifyCh
			g.mu.Unlock()
			recordWait()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		if degraded || snap == nil {
			waitMS := g.finishAdmitLocked(modelID, waited, waitStart, true)
			g.mu.Unlock()
			return &slotLease{guard: g, model: modelID, waitMS: waitMS}, nil
		}

		maxLLM := snap.MaxLLM
		if maxLLM <= 0 {
			maxLLM = 1
		}
		loadedTarget, targetLoaded := snap.Loaded[modelID]

		// Fail fast before queueing or issuing chat if pinned LLM slots saturate
		// max_models and the target model is not currently loaded. Clear cachedHealth
		// so unpinning the model takes effect immediately on the next user retry.
		if !targetLoaded && snap.PinnedLLM >= maxLLM {
			pinnedList := "pinned models"
			if len(snap.PinnedNames) > 0 {
				pinnedList = strings.Join(snap.PinnedNames, ", ")
			}
			g.cachedHealth = nil
			g.mu.Unlock()
			return nil, &statusError{
				Provider:     g.providerLabel,
				Model:        modelID,
				Status:       http.StatusConflict,
				Kind:         ErrKindInvalidRequest,
				NonRetryable: true,
				Body:         fmt.Sprintf("cannot load %q: pinned LLM residency saturated (%d/%d slots pinned: %s)", modelID, snap.PinnedLLM, maxLLM, pinnedList),
				Hint:         "unpin a currently loaded model in Lemonade or increase max_loaded_models before retrying",
			}
		}

		targetCap := 1
		if targetLoaded && loadedTarget.ParallelSlots > 0 {
			targetCap = loadedTarget.ParallelSlots
		}

		// 1. Check same-model parallel slot capacity against local Forcefield turns.
		if g.localInFlight[modelID] >= targetCap {
			ch := g.notifyCh
			g.mu.Unlock()
			recordWait()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		// 2. Check cross-model residency / eviction safety.
		// Distinguish Forcefield's own local turns from external is_busy activity:
		// never deadlock waiting for external is_busy when Forcefield itself holds
		// localInFlight on that model, and never block same-model concurrency on is_busy.
		otherLocalTurns, otherLocalDistinct := g.otherLocalInFlightLocked(modelID)

		// Compute distinct other models currently occupying LLM residency slots
		// (union of other loaded models from /health and other unloaded models
		// already admitted locally by Forcefield).
		occupiedOtherModels := make(map[string]struct{}, len(snap.LoadedOrder)+otherLocalDistinct)
		externalOtherBusy := false
		for _, m := range snap.LoadedOrder {
			if m.ModelName == "" || m.ModelName == modelID {
				continue
			}
			occupiedOtherModels[m.ModelName] = struct{}{}
			if (m.IsBusy || m.IsStreaming) && g.localInFlight[m.ModelName] == 0 {
				externalOtherBusy = true
			}
		}
		for mdl, n := range g.localInFlight {
			if mdl != "" && mdl != modelID && n > 0 {
				occupiedOtherModels[mdl] = struct{}{}
			}
		}

		needsEvictionOrSingleSlotSwitch := len(occupiedOtherModels) >= maxLLM || (!targetLoaded && len(occupiedOtherModels) >= maxLLM)
		if (maxLLM == 1 && otherLocalTurns > 0) || (needsEvictionOrSingleSlotSwitch && otherLocalTurns > 0) {
			ch := g.notifyCh
			g.mu.Unlock()
			recordWait()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		if needsEvictionOrSingleSlotSwitch && externalOtherBusy {
			// If our health snapshot predates a local release, refresh immediately
			// instead of sleeping on potentially stale is_busy state.
			if snap.FetchedGen != g.releaseGen {
				g.mu.Unlock()
				forceRefresh = true
				continue
			}
			pollEvery := g.pollInterval
			if pollEvery <= 0 {
				pollEvery = slotGuardPollInterval
			}
			ch := g.notifyCh
			g.mu.Unlock()
			recordWait()
			timer := time.NewTimer(pollEvery)
			select {
			case <-ch:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				forceRefresh = true
				continue
			case <-timer.C:
				forceRefresh = true
				continue
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return nil, ctx.Err()
			}
		}

		waitMS := g.finishAdmitLocked(modelID, waited, waitStart, false)
		g.mu.Unlock()
		return &slotLease{guard: g, model: modelID, waitMS: waitMS}, nil
	}
}

func (g *lemonadeSlotGuard) finishAdmitLocked(modelID string, waited bool, waitStart time.Time, degraded bool) int64 {
	g.localInFlight[modelID]++
	g.stats.Admitted++
	if degraded {
		g.stats.DegradedFallbacks++
	}
	var waitMS int64
	if waited {
		waitMS = g.now().Sub(waitStart).Milliseconds()
		if waitMS < 0 {
			waitMS = 0
		}
		g.stats.Waits++
		g.stats.TotalWaitMS += waitMS
	}
	return waitMS
}

func (g *lemonadeSlotGuard) getHealthSnapshot(ctx context.Context, forceRefresh bool) (*slotGuardHealthSnapshot, bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}

		now := g.now()
		g.mu.Lock()
		if g.healthFetching {
			done := g.healthFetchDone
			g.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, false, ctx.Err()
			}
		}

		if !forceRefresh && now.Before(g.degradedUntil) {
			g.mu.Unlock()
			return nil, true, nil
		}

		if !forceRefresh && g.cachedHealth != nil && g.cachedHealth.FetchedGen == g.releaseGen && now.Sub(g.cachedHealth.FetchedAt) < slotGuardHealthTTL {
			snap := g.cachedHealth
			g.mu.Unlock()
			return snap, false, nil
		}

		genBefore := g.releaseGen
		g.healthFetching = true
		done := make(chan struct{})
		g.healthFetchDone = done
		g.mu.Unlock()

		snap, ok, err := g.probeHealthAndSlots(ctx, genBefore)

		g.mu.Lock()
		g.healthFetching = false
		close(done)
		g.healthFetchDone = nil
		if err != nil {
			// Context cancellation must not poison shared health state into degraded mode.
			g.mu.Unlock()
			return nil, false, err
		}
		if !ok {
			g.cachedHealth = nil
			g.degradedUntil = g.now().Add(slotGuardDegradedCooldown)
			g.mu.Unlock()
			return nil, true, nil
		}
		g.degradedUntil = time.Time{}
		g.cachedHealth = snap
		g.mu.Unlock()
		return snap, false, nil
	}
}

func (g *lemonadeSlotGuard) probeHealthAndSlots(ctx context.Context, genBefore uint64) (*slotGuardHealthSnapshot, bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, g.probeTimeout)
	defer cancel()

	status, body, err := g.probeGET(probeCtx, g.apiBase+"/health")
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, false, nil
	}
	if status != http.StatusOK {
		return nil, false, nil
	}

	var payload struct {
		Status          string `json:"status"`
		AllModelsLoaded []struct {
			ModelName     string         `json:"model_name"`
			Recipe        string         `json:"recipe"`
			Status        string         `json:"status"`
			IsBusy        bool           `json:"is_busy"`
			IsStreaming   bool           `json:"is_streaming"`
			Pinned        bool           `json:"pinned"`
			LaunchCommand []string       `json:"launch_command"`
			RecipeOptions map[string]any `json:"recipe_options"`
		} `json:"all_models_loaded"`
		MaxModels    map[string]int `json:"max_models"`
		PinnedModels map[string]int `json:"pinned_models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false, nil
	}

	snap := &slotGuardHealthSnapshot{
		Loaded:      make(map[string]slotGuardHealthModel, len(payload.AllModelsLoaded)),
		LoadedOrder: make([]slotGuardHealthModel, 0, len(payload.AllModelsLoaded)),
		MaxLLM:      payload.MaxModels["llm"],
		PinnedLLM:   payload.PinnedModels["llm"],
		FetchedAt:   g.now(),
		FetchedGen:  genBefore,
	}

	countedPinned := 0
	for _, entry := range payload.AllModelsLoaded {
		np := extractLemonadeParallelSlots(entry.LaunchCommand, entry.RecipeOptions)
		mdl := slotGuardHealthModel{
			ModelName:     entry.ModelName,
			Recipe:        entry.Recipe,
			Status:        entry.Status,
			IsBusy:        entry.IsBusy,
			IsStreaming:   entry.IsStreaming,
			Pinned:        entry.Pinned,
			ParallelSlots: np,
		}
		snap.LoadedOrder = append(snap.LoadedOrder, mdl)
		if entry.ModelName != "" {
			snap.Loaded[entry.ModelName] = mdl
		}
		if entry.Pinned {
			countedPinned++
			if entry.ModelName != "" {
				snap.PinnedNames = append(snap.PinnedNames, entry.ModelName)
			}
		}
	}
	if snap.PinnedLLM == 0 && countedPinned > 0 {
		snap.PinnedLLM = countedPinned
	}

	// /slots is optional and ONLY valid for the llama.cpp MRU backend.
	// Query it solely when a single loaded model uses recipe == "llamacpp" so we
	// know get_most_recent_server() corresponds to that exact model; ignore 400/timeouts.
	if len(snap.LoadedOrder) == 1 && strings.EqualFold(snap.LoadedOrder[0].Recipe, "llamacpp") {
		if slotCount := g.probeLlamaCppSlotCount(ctx); slotCount > 0 {
			snap.LoadedOrder[0].ParallelSlots = slotCount
			if name := snap.LoadedOrder[0].ModelName; name != "" {
				snap.Loaded[name] = snap.LoadedOrder[0]
			}
		}
	}

	return snap, true, nil
}

func (g *lemonadeSlotGuard) probeLlamaCppSlotCount(ctx context.Context) int {
	slotsCtx, cancel := context.WithTimeout(ctx, g.slotsTimeout)
	defer cancel()

	status, body, err := g.probeGET(slotsCtx, g.apiBase+"/slots")
	if err != nil || status != http.StatusOK {
		return 0
	}
	var slots []json.RawMessage
	if err := json.Unmarshal(body, &slots); err != nil {
		return 0
	}
	return len(slots)
}

func (g *lemonadeSlotGuard) probeGET(ctx context.Context, fullURL string) (int, []byte, error) {
	req, err := g.newReq(ctx, http.MethodGet, fullURL, "application/json", nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func extractLemonadeParallelSlots(launchCmd []string, recipeOpts map[string]any) int {
	for i := 0; i < len(launchCmd); i++ {
		arg := launchCmd[i]
		if (arg == "-np" || arg == "--parallel") && i+1 < len(launchCmd) {
			if n, err := strconv.Atoi(strings.TrimSpace(launchCmd[i+1])); err == nil && n > 0 {
				return n
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
	if recipeOpts != nil {
		if raw, ok := recipeOpts["llamacpp_args"].(string); ok && raw != "" {
			fields := strings.Fields(raw)
			for i := 0; i < len(fields); i++ {
				if (fields[i] == "-np" || fields[i] == "--parallel") && i+1 < len(fields) {
					if n, err := strconv.Atoi(fields[i+1]); err == nil && n > 0 {
						return n
					}
				}
				for _, prefix := range []string{"-np=", "--parallel="} {
					if strings.HasPrefix(fields[i], prefix) {
						if n, err := strconv.Atoi(strings.TrimPrefix(fields[i], prefix)); err == nil && n > 0 {
							return n
						}
					}
				}
			}
		}
	}
	return 1
}
