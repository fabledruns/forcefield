package config

import (
	"strings"
	"testing"
)

const orchestrationBase = "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\n"

func TestLoadOrchestrationDefaults(t *testing.T) {
	isolateHome(t)
	writeConfig(t, orchestrationBase)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Orchestration.Enabled {
		t.Errorf("orchestration.enabled default = true, want false (off by default; the feature must never activate implicitly)")
	}
	if cfg.Orchestration.MaxDepth != nil {
		t.Errorf("orchestration.max_depth default = %v, want omitted (nil)", *cfg.Orchestration.MaxDepth)
	}
	if cfg.Orchestration.ChildPermissions != "" {
		t.Errorf("orchestration.child_permissions default = %q, want empty (deny-equivalent)", cfg.Orchestration.ChildPermissions)
	}
}

func TestLoadOrchestrationSettings(t *testing.T) {
	isolateHome(t)
	writeConfig(t, orchestrationBase+`orchestration:
  enabled: true
  max_depth: 2
  max_concurrent_children: 2
  max_children_per_run: 8
  max_task_chars: 4000
  child_permissions: interactive
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Orchestration.Enabled {
		t.Fatal("orchestration.enabled = false, want true")
	}
	if cfg.Orchestration.MaxDepth == nil || *cfg.Orchestration.MaxDepth != 2 {
		t.Errorf("orchestration.max_depth = %v, want 2", cfg.Orchestration.MaxDepth)
	}
	if cfg.Orchestration.MaxConcurrentChildren == nil || *cfg.Orchestration.MaxConcurrentChildren != 2 {
		t.Errorf("orchestration.max_concurrent_children = %v, want 2", cfg.Orchestration.MaxConcurrentChildren)
	}
	if cfg.Orchestration.MaxChildrenPerRun == nil || *cfg.Orchestration.MaxChildrenPerRun != 8 {
		t.Errorf("orchestration.max_children_per_run = %v, want 8", cfg.Orchestration.MaxChildrenPerRun)
	}
	if cfg.Orchestration.MaxTaskChars == nil || *cfg.Orchestration.MaxTaskChars != 4000 {
		t.Errorf("orchestration.max_task_chars = %v, want 4000", cfg.Orchestration.MaxTaskChars)
	}
	if cfg.Orchestration.ChildPermissions != "interactive" {
		t.Errorf("orchestration.child_permissions = %q, want interactive", cfg.Orchestration.ChildPermissions)
	}
}

func TestLoadRejectsInvalidOrchestration(t *testing.T) {
	cases := map[string]string{
		"depth zero":     "orchestration:\n  enabled: true\n  max_depth: 0\n",
		"depth over":     "orchestration:\n  max_depth: 4\n",
		"depth negative": "orchestration:\n  max_depth: -1\n",
		"conc zero":      "orchestration:\n  max_concurrent_children: 0\n",
		"conc over":      "orchestration:\n  max_concurrent_children: 9\n",
		"per-run over":   "orchestration:\n  max_children_per_run: 65\n",
		"task small":     "orchestration:\n  max_task_chars: 100\n",
		"task over":      "orchestration:\n  max_task_chars: 100000\n",
		"child perms":    "orchestration:\n  child_permissions: anyone\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			isolateHome(t)
			writeConfig(t, orchestrationBase+body)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() accepted invalid orchestration %s", name)
			}
			if !strings.Contains(err.Error(), "orchestration") {
				t.Errorf("error %q does not name the orchestration field", err)
			}
		})
	}
}
