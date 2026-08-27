package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Model is one model as discovered (or configured) for a provider, with
// whatever metadata the provider's listing endpoint actually exposed.
//
// Metadata is never fabricated: ContextWindow stays 0 unless the provider
// reported one, and there is deliberately no per-model Capabilities field -
// no supported list endpoint reports per-model capability data, and
// guessing from the transport would overpromise. Provider-level
// capabilities live on CapabilitiesProvider; model-level facts appear here
// only when the wire protocol states them.
type Model struct {
	ID       string
	Name     string
	Provider string
	// ContextWindow is the context size in tokens when the provider
	// reports it; 0 means unknown.
	ContextWindow int64
}

// DefaultDiscoveryTTL bounds how long a successful discovery result is
// reused before the next picker open triggers a refresh. It is a process
// lifetime, in-memory cache: nothing about discovery needs persistence,
// and config/session storage must never hold provider account state.
const DefaultDiscoveryTTL = 10 * time.Minute

// ErrNoDiscovery reports that the provider's adapter has no ListModels
// implementation. Callers treat it as "show fallback models", never as a
// failure of the provider itself.
var ErrNoDiscovery = errors.New("provider does not support model discovery")

// Discovery orchestrates ListModels across all registered transports:
// it builds the right adapter from a Spec, performs the fetch with
// single-flight de-duplication, caches results in memory for the process
// lifetime, and leaves every failure to the caller to present. It knows
// no specific provider - new transports gain discovery by implementing
// ModelLister.
type Discovery struct {
	factories *FactoryRegistry

	ttl time.Duration
	now func() time.Time // swappable for tests

	mu       sync.Mutex
	entries  map[discoveryKey]discoveryEntry
	inflight map[discoveryKey]*inflightDiscovery
}

// NewDiscovery returns a discovery service over the given factories.
func NewDiscovery(factories *FactoryRegistry) *Discovery {
	return &Discovery{
		factories: factories,
		ttl:       DefaultDiscoveryTTL,
		now:       time.Now,
		entries:   make(map[discoveryKey]discoveryEntry),
		inflight:  make(map[discoveryKey]*inflightDiscovery),
	}
}

// SetTTL overrides how long cached listings stay fresh. Non-positive
// values are ignored.
func (d *Discovery) SetTTL(ttl time.Duration) {
	if ttl > 0 {
		d.mu.Lock()
		d.ttl = ttl
		d.mu.Unlock()
	}
}

// discoveryKey identifies one cache entry: which configured provider, at
// which endpoint, speaking which protocol, authenticated as which
// credential fingerprint. The API key itself is never part of the key -
// only an irreversible SHA-256 fingerprint, so distinct accounts on the
// same endpoint keep separate listings while no key material is ever
// stored, logged, or displayable.
type discoveryKey string

func discoveryKeyFor(spec Spec) discoveryKey {
	sum := sha256.Sum256([]byte(
		spec.ID + "\x00" + spec.Type + "\x00" + spec.BaseURL + "\x00" + spec.APIKey))
	return discoveryKey(hex.EncodeToString(sum[:8]))
}

type discoveryEntry struct {
	models    []Model
	fetchedAt time.Time
}

type inflightDiscovery struct {
	done   chan struct{}
	models []Model
	err    error
}

// Supports reports whether spec's transport can enumerate models, so
// callers can avoid offering a refresh action that could never succeed.
// It never touches the network: the adapter is constructed and inspected,
// not used.
func (d *Discovery) Supports(spec Spec) bool {
	provider, err := d.factories.Create(spec)
	if err != nil {
		return false
	}
	_, ok := provider.(ModelLister)
	return ok
}

// Cached returns fresh cached models for spec without touching the
// network. The second result reports whether a fresh entry existed.
func (d *Discovery) Cached(spec Spec) ([]Model, bool) {
	key := discoveryKeyFor(spec)
	d.mu.Lock()
	defer d.mu.Unlock()
	entry, ok := d.entries[key]
	if !ok || d.now().Sub(entry.fetchedAt) >= d.ttl {
		return nil, false
	}
	return cloneModels(entry.models), true
}

// Fetch performs a live discovery for spec, bypassing and then refreshing
// the cache. Concurrent calls for the same key share one request; the
// first caller runs it while the rest block until its result is ready.
// A failed fetch leaves any previously cached entry untouched.
func (d *Discovery) Fetch(ctx context.Context, spec Spec) ([]Model, error) {
	key := discoveryKeyFor(spec)

	d.mu.Lock()
	if call, ok := d.inflight[key]; ok {
		d.mu.Unlock()
		select {
		case <-call.done:
			return call.models, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &inflightDiscovery{done: make(chan struct{})}
	d.inflight[key] = call
	d.mu.Unlock()

	models, err := d.fetch(ctx, spec)

	call.models, call.err = models, err
	close(call.done)

	d.mu.Lock()
	delete(d.inflight, key)
	if err == nil {
		d.entries[key] = discoveryEntry{models: models, fetchedAt: d.now()}
	}
	d.mu.Unlock()

	return models, err
}

// fetch builds the adapter and performs exactly one network round trip.
func (d *Discovery) fetch(ctx context.Context, spec Spec) ([]Model, error) {
	provider, err := d.factories.Create(spec)
	if err != nil {
		return nil, err
	}
	lister, ok := provider.(ModelLister)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoDiscovery, spec.Type)
	}

	listed, err := lister.ListModels(ctx)
	if err != nil {
		return nil, err
	}

	models := make([]Model, 0, len(listed))
	for _, m := range listed {
		if m.ID == "" {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		models = append(models, Model{ID: m.ID, Name: name, Provider: spec.ID})
	}

	// Deterministic order regardless of what the server sent.
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func cloneModels(in []Model) []Model {
	out := make([]Model, len(in))
	copy(out, in)
	return out
}
