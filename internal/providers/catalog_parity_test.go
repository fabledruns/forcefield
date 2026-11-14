package providers

import (
	"reflect"
	"testing"
)

// TestCatalog_StaticCapabilitiesMatchDynamic verifies that the static
// Capabilities populated on all 18 presets in Catalog bit-for-bit match
// what the actual provider adapters report dynamically from Capabilities().
func TestCatalog_StaticCapabilitiesMatchDynamic(t *testing.T) {
	if len(Catalog) != 18 {
		t.Fatalf("expected 18 presets in Catalog, found %d", len(Catalog))
	}

	factories := DefaultFactories()

	for _, preset := range Catalog {
		t.Run(preset.ID, func(t *testing.T) {
			model := "dummy-model"
			if len(preset.Models) > 0 {
				model = preset.Models[0].ID
			}
			spec := Spec{
				ID:       preset.ID,
				Type:     preset.Type,
				BaseURL:  preset.BaseURL,
				Model:    model,
				Region:   "us-east-1",
				Project:  "dummy-project",
				Location: "us-central1",
			}
			if spec.BaseURL == "" {
				spec.BaseURL = "http://localhost:9999"
			}

			provider, err := factories.Create(spec)
			if err != nil {
				t.Fatalf("failed to create provider for preset %q (type %q): %v", preset.ID, preset.Type, err)
			}

			capsProvider, ok := provider.(CapabilitiesProvider)
			if !ok {
				t.Fatalf("provider %T for preset %q does not implement CapabilitiesProvider", provider, preset.ID)
			}

			dynamicCaps := capsProvider.Capabilities()
			staticCaps := preset.Capabilities

			if !reflect.DeepEqual(staticCaps, dynamicCaps) {
				t.Errorf("capability mismatch for preset %q (type %q):\nstatic:  %+v\ndynamic: %+v",
					preset.ID, preset.Type, staticCaps, dynamicCaps)
			}

			// Also verify that CapabilitiesFor(preset.Type) returns the same capabilities
			typeCaps := CapabilitiesFor(preset.Type)
			if !reflect.DeepEqual(typeCaps, dynamicCaps) {
				t.Errorf("CapabilitiesFor(%q) mismatch:\ntypeCaps: %+v\ndynamic:  %+v",
					preset.Type, typeCaps, dynamicCaps)
			}
		})
	}
}

// TestCatalog_AllPresetsHaveCapabilities confirms no preset in Catalog has empty capabilities.
func TestCatalog_AllPresetsHaveCapabilities(t *testing.T) {
	for _, p := range Catalog {
		if p.Capabilities == (Capabilities{}) {
			t.Errorf("preset %q has empty Capabilities", p.ID)
		}
	}
}
