package permissions

import "sync"

// Store persists permission rules.
type Store interface {
	Load() (Rules, error)
	Save(Rules) error
}

// Manager owns permission rules and is safe for concurrent use.
type Manager struct {
	mu    sync.RWMutex
	rules Rules
	store Store

	// saveMu serializes writes to the store independently of mu, so a
	// slow disk write never blocks concurrent Check calls (which only
	// need the read lock).
	saveMu sync.Mutex
}

// NewManager loads rules from store.
func NewManager(store Store) (*Manager, error) {
	rules, err := store.Load()
	if err != nil {
		return nil, err
	}
	if rules.Tools == nil {
		rules.Tools = make(map[string]Decision)
	}
	return &Manager{rules: rules, store: store}, nil
}

// Check returns the effective decision for toolName.
func (m *Manager) Check(toolName string) Decision {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if d, ok := m.rules.Tools[toolName]; ok {
		return d
	}
	return m.rules.Default
}

// Update sets a per-tool override and persists it immediately, so
// "always allow" / "always deny" answers survive process restarts.
func (m *Manager) Update(toolName string, decision Decision) error {
	m.mu.Lock()
	if m.rules.Tools == nil {
		m.rules.Tools = make(map[string]Decision)
	}
	m.rules.Tools[toolName] = decision
	snapshot := m.rules.clone()
	m.mu.Unlock()

	return m.persist(snapshot)
}

// Save persists the current rule set.
func (m *Manager) Save() error {
	m.mu.RLock()
	snapshot := m.rules.clone()
	m.mu.RUnlock()

	return m.persist(snapshot)
}

// Rules returns a copy of the current rule set.
func (m *Manager) Rules() Rules {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rules.clone()
}

func (m *Manager) persist(rules Rules) error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	return m.store.Save(rules)
}
