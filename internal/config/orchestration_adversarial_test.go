package config

import (
	"strings"
	"testing"
)

// Orchestration-boundary tests. Every value asserted here is the exact
// documented bound (see config.Orchestration); the suite proves the load
// boundary accepts the bound itself and rejects the bound one step beyond.
// These tests run against the real Load path with an isolated home
// directory, so are integration-level for the config parser.

func orchestrationConfigWith(bounds string) string {
	return `model:
  provider: ollama
  endpoint: http://localhost:11434
  name: test
agent:
  name: default
permissions:
  default: ask
orchestration:
` + bounds
}

func loadOrchestrationBody(t *testing.T, bounds string) (*Config, error) {
	t.Helper()
	isolateHome(t)
	writeConfig(t, orchestrationConfigWith(bounds))
	return Load()
}

func TestAdversarialOrchestrationAcceptsExactBounds(t *testing.T) {
	tests := []struct {
		name   string
		bounds string
	}{
		{"all ceilings", "  enabled: true\n  max_depth: 3\n  max_concurrent_children: 8\n  max_children_per_run: 64\n  max_task_chars: 65536\n  max_total_child_turns: 8192"},
		{"all floors", "  enabled: true\n  max_depth: 1\n  max_concurrent_children: 1\n  max_children_per_run: 1\n  max_task_chars: 256\n  max_total_child_turns: 1"},
		{"all valid, disabled", "  enabled: false\n  max_depth: 1\n  max_concurrent_children: 4"},
		{"interactive mode", "  enabled: true\n  child_permissions: interactive"},
		{"downgraded implies child below ceiling", "  enabled: true\n  max_depth: 2\n  max_concurrent_children: 2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadOrchestrationBody(t, tc.bounds)
			if err != nil {
				t.Fatalf("Load() rejected %v: %v", tc.bounds, err)
			}
			if !cfg.Orchestration.Enabled && !strings.Contains(tc.bounds, "enabled: false") {
				t.Fatalf("orchestration not enabled (%v): %+v", tc.bounds, cfg.Orchestration)
			}
		})
	}
}

func TestAdversarialOrchestrationRejectsOneBeyondEveryBound(t *testing.T) {
	tests := []struct {
		name   string
		bounds string
		want   string // substring expected in the load error
	}{
		{"depth 4", "  enabled: true\n  max_depth: 4", "max_depth"},
		{"depth 0", "  enabled: true\n  max_depth: 0", "max_depth"},
		{"depth -2", "  enabled: true\n  max_depth: -2", "max_depth"},
		{"children 9", "  enabled: true\n  max_concurrent_children: 9", "max_concurrent_children"},
		{"children 0", "  enabled: true\n  max_concurrent_children: 0", "max_concurrent_children"},
		{"per_run 65", "  enabled: true\n  max_children_per_run: 65", "max_children_per_run"},
		{"per_run 0", "  enabled: true\n  max_children_per_run: 0", "max_children_per_run"},
		{"task_chars 65537", "  enabled: true\n  max_task_chars: 65537", "max_task_chars"},
		{"task_chars 255", "  enabled: true\n  max_task_chars: 255", "max_task_chars"},
		{"task_chars -100", "  enabled: true\n  max_task_chars: -100", "max_task_chars"},
		{"total_turns 8193", "  enabled: true\n  max_total_child_turns: 8193", "max_total_child_turns"},
		{"total_turns 0", "  enabled: true\n  max_total_child_turns: 0", "max_total_child_turns"},
		{"total_turns -1", "  enabled: true\n  max_total_child_turns: -1", "max_total_child_turns"},
		{"unknown child mode", "  enabled: true\n  child_permissions: always_ask", "child_permissions"},
		{"case sensitive mode", "  enabled: true\n  child_permissions: Interactive", "child_permissions"},
		{"depth beyond ceiling while disabled", "  enabled: false\n  max_depth: 99", "max_depth"},
		{"children beyond ceiling while disabled", "  enabled: false\n  max_concurrent_children: 99", "max_concurrent_children"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadOrchestrationBody(t, tc.bounds)
			if err == nil {
				t.Fatalf("Load() accepted %v, want rejection naming %q", tc.bounds, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() error %q does not name %q", err, tc.want)
			}
		})
	}
}

// TestAdversarialOrchestrationRejectsConcurrencyBelowDepth is the regression
// test for defect O2: a config that guarantees nested-delegation self-starvation
// (every spawn above the concurrency cap stalls on the child semaphore until the
// delegate timeout) must fail at load, never silently at runtime.
func TestAdversarialOrchestrationRejectsConcurrencyBelowDepth(t *testing.T) {
	tests := []struct {
		name   string
		bounds string
	}{
		{"cap 1 depth 2", "  enabled: true\n  max_depth: 2\n  max_concurrent_children: 1"},
		{"cap 2 depth 3", "  enabled: true\n  max_depth: 3\n  max_concurrent_children: 2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadOrchestrationBody(t, tc.bounds)
			if err == nil {
				t.Fatalf("Load() accepted config that forces nested-delegation stall: %v", tc.bounds)
			}
			if !strings.Contains(err.Error(), "max_concurrent_children") ||
				!strings.Contains(err.Error(), "max_depth") {
				t.Fatalf("load error must name both fields, got: %v", err)
			}
		})
	}
}

// TestAdversarialOrchestrationTreatsNullAsUnset pins the missing-field
// semantics: an explicit YAML null must behave exactly like an omitted key.
func TestAdversarialOrchestrationTreatsNullAsUnset(t *testing.T) {
	cfg, err := loadOrchestrationBody(t, "  enabled: true\n  max_depth: null\n  max_task_chars: null\n  max_total_child_turns: null")
	if err != nil {
		t.Fatalf("Load() rejected null fields: %v", err)
	}
	if cfg.Orchestration.MaxDepth != nil || cfg.Orchestration.MaxTaskChars != nil || cfg.Orchestration.MaxTotalChildTurns != nil {
		t.Fatalf("null fields must resolve to nil pointers, got %+v", cfg.Orchestration)
	}
}

func TestAdversarialOrchestrationRejectsMalformedTypes(t *testing.T) {
	// The yaml boundary must reject wrong types; whatever leaks past yaml
	// cannot silently pass validation either. Each body reloads config from
	// scratch in an isolated home.
	bodies := map[string]string{
		"string section":    "  nope",
		"bool section":      "  true",
		"deep overflow int": "  enabled: true\n  max_task_chars: 9223372036854775808",
		"int underflow int": "  enabled: true\n  max_depth: -9223372036854775809",
	}
	for name, bounds := range bodies {
		t.Run(name, func(t *testing.T) {
			_, err := loadOrchestrationBody(t, bounds)
			if err == nil {
				t.Fatalf("Load() accepted malformed orchestration input %q (%v)", name, bounds)
			}
		})
	}
}

// TestAdversarialDisabledOrchestrationRejectsDelegateInAgentTools pins
// defect O1's resolved behavior: `delegate` is a known tool name, but with
// orchestration disabled the capability cannot exist. The config must be
// rejected at load with a truthful message, never silently honored and
// never misreported as "unknown tool" by the runtime tool-universe build.
// Fail-closed: no path can construct a delegate capability here.
func TestAdversarialDisabledOrchestrationRejectsDelegateInAgentTools(t *testing.T) {
	body := `model:
  provider: ollama
  endpoint: http://localhost:11434
  name: test
agent:
  name: default
permissions:
  default: ask
orchestration:
  enabled: false
agents:
  coding:
    tools: [read_file, delegate]
`
	isolateHome(t)
	writeConfig(t, body)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() accepted an agent override naming delegate with orchestration disabled")
	}
	if !strings.Contains(err.Error(), "orchestration.enabled is false") {
		t.Fatalf("error must name the exact constraint, got: %v", err)
	}
}

// TestAdversarialEnabledOrchestrationAcceptsDelegateInAgentTools records the
// valid counterpart: with orchestration enabled the override may name delegate.
func TestAdversarialEnabledOrchestrationAcceptsDelegateInAgentTools(t *testing.T) {
	body := `model:
  provider: ollama
  endpoint: http://localhost:11434
  name: test
agent:
  name: default
permissions:
  default: ask
orchestration:
  enabled: true
agents:
  coding:
    tools: [read_file, delegate]
`
	isolateHome(t)
	writeConfig(t, body)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() rejected a valid delegate override: %v", err)
	}
	if len(cfg.Agents["coding"].Tools) != 2 {
		t.Fatalf("agent tools must pass through verbatim, got %+v", cfg.Agents["coding"].Tools)
	}
}

// TestAdversarialDisabledOrchestrationStillValidates is covered in the bounds
// table above; this pin records the rule explicitly: disabling never relaxes
// validation, so enabling later cannot surprise.
func TestAdversarialDisabledOrchestrationStillValidates(t *testing.T) {
	_, err := loadOrchestrationBody(t, "  enabled: false\n  child_permissions: interactive-with-bugs")
	if err == nil || !strings.Contains(err.Error(), "child_permissions") {
		t.Fatalf("disabled orchestration must still validate child_permissions, got err=%v", err)
	}
}

// FuzzValidateOrchestration asserts the load boundary as a pure predicate:
// an error is returned exactly when at least one documented bound is
// violated (depth inside 1..3, children inside 1..8, perRun inside
// 1..64, taskChars inside 256..65536, totalChildTurns inside 1..8192,
// mode in the two supported values, and the O2 cross-field rule cap >=
// depth when both are set). No combination of inputs may panic, and
// identical inputs produce identical outcomes.
func FuzzValidateOrchestration(f *testing.F) {
	// Set/nil selectors plus each field's exact ceilings, floors, and
	// one-step-beyond values, plus int extremes' neighborhood.
	for _, seeds := range []struct {
		setDepth bool
		depth    int
		setCap   bool
		cap      int
		setPR    bool
		pr       int
		setChars bool
		chars    int
		setTurns bool
		turns    int
		mode     string
	}{
		{false, 0, false, 0, false, 0, false, 0, false, 0, ""},
		{true, 1, true, 1, true, 1, true, 256, true, 1, ""},
		{true, 3, true, 8, true, 64, true, 65536, true, 2048, ""},
		{true, 3, true, 8, true, 64, true, 65536, true, 8192, ""},
		{true, 3, true, 8, true, 64, true, 65536, true, 8192, "interactive"},
		{true, 3, true, 8, true, 64, true, 65536, true, 8192, "deny"},
		{true, 4, true, 8, true, 64, true, 65536, false, 0, ""},           // depth beyond ceiling
		{true, 0, true, 8, true, 64, true, 65536, false, 0, ""},           // depth below floor
		{true, 1, true, 9, true, 64, true, 65536, false, 0, ""},           // cap beyond ceiling
		{true, 1, true, 0, true, 64, true, 65536, false, 0, ""},           // cap below floor
		{true, 1, true, 8, true, 65, true, 65536, false, 0, ""},           // perRun beyond ceiling
		{true, 1, true, 8, true, 0, true, 65536, false, 0, ""},            // perRun below floor
		{true, 1, true, 8, true, 64, true, 65537, false, 0, ""},           // chars beyond ceiling
		{true, 1, true, 8, true, 64, true, 255, false, 0, ""},             // chars below floor
		{true, 1, true, 8, true, 64, true, 65536, true, 8193, ""},         // turns beyond ceiling
		{true, 1, true, 8, true, 64, true, 65536, true, 0, ""},            // turns below floor
		{true, 2, true, 1, true, 64, true, 65536, false, 0, ""},           // O2: cap < depth
		{true, 3, true, 3, true, 64, true, 65536, false, 0, ""},           // O2: cap == depth (legal)
		{true, 2, false, 0, true, 64, true, 65536, false, 0, ""},          // cross-field skipped when cap unset
		{false, 0, true, 4, false, 0, false, 0, false, 0, ""},             // unset fields never validate
		{false, 0, false, 0, false, 0, false, 0, false, 0, "INTERACTIVE"}, // case-sensitive
		{false, 0, false, 0, false, 0, false, 0, false, 0, "always_ask"},
		{true, 1, true, 8, true, 64, true, 9223372036854775807, false, 0, ""},
		{true, 1, true, 8, true, 64, true, -9223372036854775808, false, 0, ""},
	} {
		f.Add(seeds.setDepth, seeds.depth, seeds.setCap, seeds.cap, seeds.setPR, seeds.pr, seeds.setChars, seeds.chars, seeds.setTurns, seeds.turns, seeds.mode)
	}
	f.Fuzz(func(t *testing.T, setDepth bool, depth int, setCap bool, capN int, setPR bool, perRun int, setChars bool, chars int, setTurns bool, turns int, mode string) {
		cfg := configOrchestrationForFuzz(setDepth, depth, setCap, capN, setPR, perRun, setChars, chars, setTurns, turns, mode)
		err1 := validateOrchestration(cfg)
		err2 := validateOrchestration(cfg)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("validateOrchestration nondeterministic (nil-ness) for %+v: %v then %v", cfg, err1, err2)
		}
		if err1 != nil && err1.Error() != err2.Error() {
			t.Fatalf("validateOrchestration nondeterministic (text) for %+v: %q then %q", cfg, err1, err2)
		}

		// The documented predicate: error ⟺ at least one violated bound.
		wantErr := false
		if setDepth && (depth < 1 || depth > MaxOrchestrationDepth) {
			wantErr = true
		}
		if setCap && (capN < 1 || capN > MaxOrchestrationChildren) {
			wantErr = true
		}
		if setPR && (perRun < 1 || perRun > MaxOrchestrationPerRun) {
			wantErr = true
		}
		if setChars && (chars < MinOrchestrationTaskChars || chars > MaxOrchestrationTaskChars) {
			wantErr = true
		}
		if setTurns && (turns < 1 || turns > MaxOrchestrationTurns) {
			wantErr = true
		}
		if setDepth && setCap && capN < depth {
			// O2 cross-field rule: nested chains need one slot per level.
			wantErr = true
		}
		switch mode {
		case "", ChildPermissionsDeny, ChildPermissionsInteractive:
		default:
			wantErr = true
		}

		if wantErr && err1 == nil {
			t.Fatalf("validateOrchestration accepted a combination outside every documented bound: %+v (mode %q)", cfg, mode)
		}
		if !wantErr && err1 != nil {
			t.Fatalf("validateOrchestration rejected a documented-legal combination: %+v (mode %q): %v", cfg, mode, err1)
		}
	})
}

// configOrchestrationForFuzz builds the pointer-shaped config record from
// flat fuzz arguments (nil-ness is encoded in the set flags).
func configOrchestrationForFuzz(setDepth bool, depth int, capSet bool, capN int, setPR bool, perRun int, setChars bool, chars int, setTurns bool, turns int, mode string) Orchestration {
	cfg := Orchestration{ChildPermissions: mode}
	if setDepth {
		d := depth
		cfg.MaxDepth = &d
	}
	if capSet {
		c := capN
		cfg.MaxConcurrentChildren = &c
	}
	if setPR {
		p := perRun
		cfg.MaxChildrenPerRun = &p
	}
	if setChars {
		ch := chars
		cfg.MaxTaskChars = &ch
	}
	if setTurns {
		tr := turns
		cfg.MaxTotalChildTurns = &tr
	}
	return cfg
}
