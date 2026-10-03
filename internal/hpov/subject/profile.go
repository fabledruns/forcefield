package subject

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"forcefield/internal/hpov/bench"
)

// Profile is a named subject contract: the workload semantics HPOV needs
// to measure one harness. A profile is data about a subject, not code
// linked to it, so describing a harness never makes HPOV depend on that
// harness.
type Profile struct {
	// Name identifies the profile in results and error messages.
	Name string `json:"name"`
	// Source records where the profile came from: "builtin:<name>" or
	// the file path it was read from.
	Source   string         `json:"source"`
	Contract bench.Contract `json:"contract"`
}

// BuiltinForcefieldName is the built-in Forcefield profile id.
const BuiltinForcefieldName = "forcefield"

// DefaultProfileRef is the profile used when none is named. Forcefield
// is the built-in subject, so an unqualified run keeps the behavior HPOV
// has always had; measuring another harness means naming its profile,
// which is also how the result document stops claiming the subject is
// Forcefield.
const DefaultProfileRef = "builtin:" + BuiltinForcefieldName

// forcefieldSentinelAgent exercises a harness's full runtime
// construction and then fails local agent validation, giving a
// deliberate non-zero exit at a well-defined boundary. It is an
// argument, not a real agent.
const forcefieldSentinelAgent = "__bench_bogus__"

// forcefieldMarkerPrefix is the marker line discriminator Forcefield
// writes to stderr. It is declared here as data rather than imported
// from the emitter so HPOV keeps no runtime dependency on Forcefield.
const forcefieldMarkerPrefix = "ff-perf "

// Builtin returns the built-in Forcefield profile.
func Builtin() Profile {
	return Profile{
		Name:   BuiltinForcefieldName,
		Source: DefaultProfileRef,
		Contract: bench.Contract{
			Product: "forcefield",
			Bin:     "ff",
			// Forcefield's marker line discriminator, declared as data
			// rather than imported from the emitter so HPOV keeps no
			// runtime dependency on Forcefield.
			MarkerPrefix: forcefieldMarkerPrefix,
			EnableEnv:    map[string]string{"FF_PERF_MARKERS": "1"},
			Version: bench.Probe{
				Args:         []string{"--version"},
				StdoutPrefix: "ff version",
			},
			Help: bench.Probe{
				Args:           []string{"--help"},
				StdoutContains: "Usage:",
			},
			Headless: bench.Headless{
				Args:     []string{"run", "--agent", forcefieldSentinelAgent, "x"},
				ExitCode: 1,
				// The home is primed once, untimed, so measured
				// iterations start from a written config.yaml rather
				// than creating one inside the timed region.
				Primed: true,
				ProbeChecks: []bench.ProbeCheck{
					{Name: "unknown_agent_error", Substring: `unknown agent "` + forcefieldSentinelAgent + `"`},
					{Name: "stage_agents_seen", Marker: "stage-agents"},
				},
			},
			TUI: bench.TUI{
				PrimaryMark:       "first-useful-frame",
				ReadinessTailMark: "runtime-ready",
				Marks: []string{
					"main-entry", "config-loaded", "runtime-init-start",
					"stage-skills", "stage-memory", "stage-provider", "stage-tools",
					"stage-agents", "first-frame", "first-useful-frame", "runtime-ready",
				},
				Segments: []bench.Segment{
					{Name: "seg_process_to_main_entry", From: bench.SpawnAnchor, To: "main-entry"},
					{Name: "seg_main_entry_to_config_loaded", From: "main-entry", To: "config-loaded"},
					{Name: "seg_config_loaded_to_runtime_init_start", From: "config-loaded", To: "runtime-init-start"},
					{Name: "seg_runtime_init_start_to_first_useful_frame", From: "runtime-init-start", To: "first-useful-frame"},
					{Name: "seg_process_to_first_useful_frame", From: bench.SpawnAnchor, To: "first-useful-frame"},
					{Name: "seg_stage_agents_to_runtime_ready", From: "stage-agents", To: "runtime-ready"},
					{Name: "seg_first_frame_to_runtime_ready", From: "first-frame", To: "runtime-ready"},
				},
				QuitInput:       "/exit",
				RequireExitZero: true,
				// perfmark.EventMem writes alloc=/sys= at these marks, so
				// the Go heap metrics have a source. Only a Go subject
				// with that instrumentation can satisfy them.
				HeapFields: true,
			},
			Env: bench.Env{
				// FF_* belongs to Forcefield: never let a developer's
				// own variables reach a measured process.
				ScrubPrefixes: []string{"FF_"},
			},
		},
	}
}

// LoadProfile resolves a profile reference:
//
//	""                   the default profile (builtin:forcefield)
//	"builtin:forcefield" the built-in Forcefield profile
//	"<path>"             a JSON profile document
//
// A profile document is {"name": ..., "contract": {...}} and carries
// exactly the same contract shape as the built-in, so a new harness is
// described without touching Go code.
func LoadProfile(ref string) (Profile, error) {
	ref = strings.TrimSpace(ref)
	switch {
	case ref == "":
		return Builtin(), nil
	case ref == "builtin:"+BuiltinForcefieldName:
		return Builtin(), nil
	case strings.HasPrefix(ref, "builtin:"):
		return Profile{}, fmt.Errorf("unknown built-in profile %q: only %q exists",
			strings.TrimPrefix(ref, "builtin:"), BuiltinForcefieldName)
	}
	raw, err := os.ReadFile(ref)
	if err != nil {
		return Profile{}, fmt.Errorf("read subject profile: %w", err)
	}
	var p Profile
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Profile{}, fmt.Errorf("parse subject profile %s: %w", ref, err)
	}
	if p.Name == "" {
		return Profile{}, fmt.Errorf("subject profile %s: name is required", ref)
	}
	if err := p.Contract.Validate(); err != nil {
		return Profile{}, fmt.Errorf("subject profile %s: %w", ref, err)
	}
	p.Source = ref
	return p, nil
}

// BuiltinProfiles lists the built-in profile ids, sorted.
func BuiltinProfiles() []string { return []string{BuiltinForcefieldName} }

// applyTo stamps the contract onto a subject. Every subject in one
// invocation is measured under one profile: a run is a comparison within
// one workload contract, and comparing two harnesses means two runs fed
// to `hpov compare`.
func (p Profile) applyTo(s bench.Subject) bench.Subject {
	s.Contract = p.Contract
	return s
}

// ApplyTo stamps this profile's contract onto each subject.
func (p Profile) ApplyTo(subjects []bench.Subject) []bench.Subject {
	out := make([]bench.Subject, 0, len(subjects))
	for _, s := range subjects {
		out = append(out, p.applyTo(s))
	}
	return out
}

// MarkerEnvNote describes the marker environment a contract turns on,
// for the result document's provenance. A contract with no
// instrumentation records nothing, so a generic subject's provenance
// never mentions another product's variables.
func MarkerEnvNote(c bench.Contract) map[string]string {
	if len(c.EnableEnv) == 0 {
		return nil
	}
	out := make(map[string]string, len(c.EnableEnv))
	for k, v := range c.EnableEnv {
		out[k] = v + " (marker pass only)"
	}
	return out
}
