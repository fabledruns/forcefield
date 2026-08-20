package permissions

import (
	"fmt"

	"forcefield/internal/config"
)

// configStore persists Rules as the "permissions" section of
// ~/.forcefield/config.yaml, leaving the rest of the file untouched.
type configStore struct{}

// NewConfigStore returns a config-backed Store.
func NewConfigStore() Store {
	return configStore{}
}

func (configStore) Load() (Rules, error) {
	cfg, err := config.Load()
	if err != nil {
		return Rules{}, fmt.Errorf("permissions: load config: %w", err)
	}
	return rulesFromConfig(cfg.Permissions)
}

// Save reloads config.yaml, overwrites just its permissions section, and
// writes it back. Reloading first (rather than caching the Config from
// Load) means concurrent edits to other sections of config.yaml - e.g. a
// model switch via SetModel - are never clobbered by a permissions save
// racing it.
func (configStore) Save(rules Rules) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("permissions: load config: %w", err)
	}
	cfg.Permissions = configFromRules(rules)
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("permissions: save config: %w", err)
	}
	return nil
}

func rulesFromConfig(p config.Permissions) (Rules, error) {
	def, err := ParseDecision(p.Default)
	if err != nil {
		return Rules{}, fmt.Errorf("permissions.default: %w", err)
	}

	tools := make(map[string]Decision, len(p.Tools))
	for name, raw := range p.Tools {
		d, err := ParseDecision(raw)
		if err != nil {
			return Rules{}, fmt.Errorf("permissions.tools.%s: %w", name, err)
		}
		tools[name] = d
	}

	return Rules{Default: def, Tools: tools}, nil
}

func configFromRules(r Rules) config.Permissions {
	tools := make(map[string]string, len(r.Tools))
	for name, d := range r.Tools {
		tools[name] = d.String()
	}
	return config.Permissions{Default: r.Default.String(), Tools: tools}
}
