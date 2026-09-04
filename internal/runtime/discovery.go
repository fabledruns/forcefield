package runtime

import (
	"context"
	"fmt"
	"sort"
	"time"

	"forcefield/internal/config"
	"forcefield/internal/providers"
)

// ModelListState describes what the caller should do with the model list
// returned by ModelCatalog: show it as-is, keep showing it while a
// background refresh runs, or stop offering refresh entirely because the
// transport cannot enumerate models.
type ModelListState int

const (
	// ModelsStale means no fresh listing is cached; callers may kick off
	// DiscoverModels asynchronously while showing the returned fallback
	// models.
	ModelsStale ModelListState = iota
	// ModelsFresh means the cached listing was fetched within the
	// discovery TTL and can be shown immediately.
	ModelsFresh
	// ModelsUnsupported means the provider's transport has no ListModels
	// implementation; refreshing would always fail.
	ModelsUnsupported
)

// discoveryTimeout bounds one background discovery request so a hung
// endpoint can never leave a picker stuck in its loading state.
const discoveryTimeout = 20 * time.Second

// ModelCatalog returns the models to present for providerID, without any
// network I/O, in deterministic order:
//
//  1. the explicitly active model (when providerID is the active one) -
//     a manually configured model stays selectable even if discovery
//     never heard of it;
//  2. the provider entry's own default model, if configured;
//  3. freshly cached discovered models, sorted by ID;
//  4. otherwise (nothing discovered yet): configured/catalog fallback
//     models, sorted, so offline use still offers sensible choices.
//
// Entries are de-duplicated by ID preserving this priority. The state
// tells the caller whether triggering DiscoverModels is worthwhile.
func (r *Runtime) ModelCatalog(providerID string) ([]providers.Model, ModelListState) {
	if r == nil {
		return nil, ModelsUnsupported
	}
	r.mu.RLock()
	cfg := r.cfg
	discovery := r.discovery
	r.mu.RUnlock()
	if cfg == nil {
		return nil, ModelsUnsupported
	}
	resolved, err := cfg.ResolveProvider(providerID, cfg.Model.Name)
	if err != nil {
		return nil, ModelsUnsupported
	}
	spec := resolved.Spec(cfg.Model.Name)

	supportsDiscovery := false
	var cached []providers.Model
	var fresh bool
	if discovery != nil {
		supportsDiscovery = discovery.Supports(spec)
		cached, fresh = discovery.Cached(spec)
	}
	static := make([]providers.Model, 0, len(resolved.Models))
	for _, id := range sortedCopy(resolved.Models) {
		// Catalog/configured entries carry friendly display names where
		// the built-in catalog knows them.
		static = append(static, providers.Model{
			ID:       id,
			Name:     providers.ModelDisplayName(providerID, id),
			Provider: providerID,
		})
	}

	models, fresh := cached, fresh
	switch {
	case fresh:
		return dedupeModels(priorityModelsWithCfg(cfg, resolved), models), ModelsFresh
	case supportsDiscovery:
		// Nothing usable cached yet: fall back to configured/catalog
		// models until the async fetch lands.
		return dedupeModels(priorityModelsWithCfg(cfg, resolved), static), ModelsStale
	default:
		// No discovery at all: the static list IS the list.
		return dedupeModels(priorityModelsWithCfg(cfg, resolved), static), ModelsUnsupported
	}
}

// priorityModels lists the models that must lead the picker: the active
// selection and the entry's configured default.
func (r *Runtime) priorityModels(resolved config.ResolvedProvider) []providers.Model {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()
	return priorityModelsWithCfg(cfg, resolved)
}

func priorityModelsWithCfg(cfg *config.Config, resolved config.ResolvedProvider) []providers.Model {
	var out []providers.Model
	add := func(id string) {
		if id != "" {
			out = append(out, providers.Model{
				ID:       id,
				Name:     providers.ModelDisplayName(resolved.ID, id),
				Provider: resolved.ID,
			})
		}
	}
	if cfg != nil && cfg.Model.Provider == resolved.ID {
		add(cfg.Model.Name)
	}
	add(resolved.Model)
	return out
}

// DiscoverModels performs live discovery for providerID through the
// shared discovery service: resolve the configured spec, build that
// transport's adapter, call ListModels once (single-flight), cache the
// result. The request is bounded so a hung endpoint cannot stall callers.
// Failures are returned, never fatal: callers keep whatever ModelCatalog
// last provided.
func (r *Runtime) DiscoverModels(ctx context.Context, providerID string) ([]providers.Model, error) {
	if r == nil {
		return nil, fmt.Errorf("runtime not available")
	}
	r.mu.RLock()
	cfg := r.cfg
	discovery := r.discovery
	r.mu.RUnlock()
	if cfg == nil || discovery == nil {
		return nil, fmt.Errorf("model discovery not available")
	}
	resolved, err := cfg.ResolveProvider(providerID, cfg.Model.Name)
	if err != nil {
		return nil, err
	}
	if resolved.AuthRequired && resolved.APIKey == "" {
		return nil, fmt.Errorf(
			"%s requires an API key - set %s in your environment or .env file",
			resolved.Label, resolved.AuthEnvVar,
		)
	}
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	return discovery.Fetch(ctx, resolved.Spec(cfg.Model.Name))
}

// dedupeModels appends b onto a dropping IDs already seen, preserving a's
// order first. The result is always a fresh slice, so callers may mutate
// it even when an input came from the shared cache.
func dedupeModels(a, b []providers.Model) []providers.Model {
	out := make([]providers.Model, 0, len(a)+len(b))
	seen := make(map[string]struct{}, len(a)+len(b))
	for _, group := range [][]providers.Model{a, b} {
		for _, m := range group {
			if m.ID == "" {
				continue
			}
			if _, dup := seen[m.ID]; dup {
				continue
			}
			seen[m.ID] = struct{}{}
			out = append(out, m)
		}
	}
	return out
}

func sortedCopy(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	sort.Strings(out)
	return out
}
