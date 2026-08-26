package providers

// ModelInfo describes one selectable model: the friendly name shown in
// the UI and the real model ID sent to the provider's API. The UI must
// only ever display Name; ID is what actually gets stored in config and
// sent over the wire.
type ModelInfo struct {
	Name        string
	ID          string
	Description string
}

// ProviderInfo describes one selectable provider and the models known
// to be available on it. It is the display-oriented view of a catalog
// preset; configuration can add further providers and models at runtime.
type ProviderInfo struct {
	Name         string
	ID           string
	Description  string
	Capabilities Capabilities
	Scope        Scope
	Auth         AuthRequirement
	// Endpoint is the default endpoint used when switching to this
	// provider, so picking a provider doesn't require also knowing its
	// URL.
	Endpoint string
	Models   []ModelInfo
}

// Registry lists every built-in provider, in display order, derived from
// the service catalog. Adding a provider or model means editing the
// catalog only — nothing else in the command or TUI layers hardcodes
// provider or model names.
var Registry = buildRegistry()

func buildRegistry() []ProviderInfo {
	out := make([]ProviderInfo, 0, len(Catalog))
	capsByType := make(map[string]Capabilities)
	for _, preset := range Catalog {
		caps, cached := capsByType[preset.Type]
		if !cached {
			caps = CapabilitiesFor(preset.Type)
			capsByType[preset.Type] = caps
		}
		models := make([]ModelInfo, len(preset.Models))
		copy(models, preset.Models)
		out = append(out, ProviderInfo{
			Name:         preset.Name,
			ID:           preset.ID,
			Description:  preset.Description,
			Capabilities: caps,
			Scope:        preset.Scope,
			Auth:         preset.Auth,
			Endpoint:     preset.BaseURL,
			Models:       models,
		})
	}
	return out
}

// ByID looks up a built-in provider by ID.
func ByID(id string) (ProviderInfo, bool) {
	for _, p := range Registry {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderInfo{}, false
}

// ModelByID looks up a model within a provider by its real model ID.
func (p ProviderInfo) ModelByID(id string) (ModelInfo, bool) {
	for _, m := range p.Models {
		if m.ID == id {
			return m, true
		}
	}
	return ModelInfo{}, false
}

// DisplayName returns a provider's friendly name, or its ID if unknown.
func DisplayName(providerID string) string {
	if p, ok := ByID(providerID); ok {
		return p.Name
	}
	return providerID
}

// ModelDisplayName returns a model's friendly name, or its ID if unknown.
func ModelDisplayName(providerID, modelID string) string {
	if p, ok := ByID(providerID); ok {
		if m, ok := p.ModelByID(modelID); ok {
			return m.Name
		}
	}
	return modelID
}
