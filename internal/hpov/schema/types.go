// Package schema defines the versioned hpov.result JSON document.
// Readers must ignore unknown fields; writers must not change a
// field's meaning inside a major schema version.
//
// Times are milliseconds as float64 (3 decimals), sizes are integer
// bytes, micro benchmarks use ns/op. Unavailable data is null plus a
// reason, never 0.
package schema

const (
	// SchemaName is the document discriminator.
	SchemaName = "hpov.result"
	// SchemaVersion follows semver: MAJOR for removed/repurposed
	// fields or changed meaning, MINOR for additive, PATCH for
	// clarifications.
	SchemaVersion = "1.0.0"
	// SuiteVersion is the hpov tool version that wrote the result.
	SuiteVersion = "0.1.0"
	// DefinitionSet pins the benchmark definition set.
	DefinitionSet = "2026.10"
	// QuantileMethod is the percentile rule shared with
	// bench/startup.ps1: nearest-rank, rank = ceil(p/100*n), 1-based.
	QuantileMethod = "nearest-rank"
	// BootstrapResamples is the percentile-bootstrap budget for p50
	// confidence intervals.
	BootstrapResamples = 5000
)

// Benchmark statuses.
const (
	StatusOK          = "ok"
	StatusSkipped     = "skipped"
	StatusUnsupported = "unsupported"
	StatusInvalid     = "invalid"
	StatusError       = "error"
)

// Result is the canonical hpov.result document.
type Result struct {
	Schema        string      `json:"schema"`
	SchemaVersion string      `json:"schema_version"`
	Suite         Suite       `json:"suite"`
	Run           Run         `json:"run"`
	Subjects      []Subject   `json:"subjects"`
	Host          Host        `json:"host"`
	Environment   Environment `json:"environment"`
	Benchmarks    []Benchmark `json:"benchmarks"`
	Warnings      []string    `json:"warnings,omitempty"`
}

// Suite identifies the tool and definition set.
type Suite struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Profile       string `json:"profile"`
	DefinitionSet string `json:"definition_set"`
}

// Run carries run-level provenance.
type Run struct {
	ID             string     `json:"id"`
	StartedAt      string     `json:"started_at"`
	FinishedAt     string     `json:"finished_at"`
	CommandLine    []string   `json:"command_line"`
	Seed           int64      `json:"seed"`
	QuantileMethod string     `json:"quantile_method"`
	Bootstrap      Bootstrap  `json:"bootstrap"`
	Quality        RunQuality `json:"quality"`
}

// Bootstrap records the CI method so intervals are reproducible.
type Bootstrap struct {
	Resamples int    `json:"resamples"`
	Seed      int64  `json:"seed"`
	Method    string `json:"method"` // "percentile" | "moving-block"
}

// RunQuality is the roll-up label: good (no flags), degraded (any
// noisy/drift/env warning), poor (spawn-floor drift >25% or >30% of
// benchmarks noisy). Poor runs are not eligible as baselines.
type RunQuality struct {
	Label string   `json:"label"` // "good" | "degraded" | "poor"
	Flags []string `json:"flags"`
}

// Subject provenances one measured binary.
type Subject struct {
	Label     string `json:"label"`
	Product   string `json:"product"`
	Version   string `json:"version,omitempty"`
	GitCommit string `json:"git_commit,omitempty"`
	GitDirty  bool   `json:"git_dirty,omitempty"`
	Binary    Binary `json:"binary"`
}

// Binary records exact artifact provenance.
type Binary struct {
	Basename    string `json:"basename"`
	SHA256      string `json:"sha256"`
	SizeBytes   int64  `json:"size_bytes"`
	Source      string `json:"source"` // "release" | "local-build"
	GoVersion   string `json:"go_version,omitempty"`
	GoOS        string `json:"goos,omitempty"`
	GoArch      string `json:"goarch,omitempty"`
	CGO         string `json:"cgo,omitempty"`
	Trimpath    *bool  `json:"trimpath,omitempty"`
	Ldflags     string `json:"ldflags,omitempty"`
	VCSRevision string `json:"vcs_revision,omitempty"`
}

// Host describes the measurement machine. results carry a salted
// host_id hash, never the hostname.
type Host struct {
	HostID           string `json:"host_id"`
	OS               string `json:"os"`
	OSVersion        string `json:"os_version,omitempty"`
	Arch             string `json:"arch"`
	Env              string `json:"env"` // "native" | "wsl2" | "vm" | "ci" | "unknown"
	CPU              CPU    `json:"cpu"`
	MemoryTotalBytes *int64 `json:"memory_total_bytes,omitempty"`
	Gomaxprocs       int    `json:"gomaxprocs"`
	Power            Power  `json:"power"`
	Clock            Clock  `json:"clock"`
	FS               FS     `json:"fs"`
	Tools            Tools  `json:"tools"`
	HPOVGoVersion    string `json:"hpov_go_version"`
}

// CPU topology.
type CPU struct {
	Model    string `json:"model,omitempty"`
	Logical  int    `json:"logical"`
	Physical *int   `json:"physical,omitempty"`
}

// Power state at run start.
type Power struct {
	Source string `json:"source,omitempty"` // "ac" | "battery" | "unknown"
	Plan   string `json:"plan,omitempty"`
}

// Clock calibration: Go's time.Now monotonic source.
type Clock struct {
	Source       string `json:"source"`
	ResolutionNs int64  `json:"resolution_ns"`
}

// FS records the workroot filesystem kind.
type FS struct {
	Workroot string `json:"workroot,omitempty"`
}

// Tools records optional tool availability; null means absent.
type Tools struct {
	Git *string `json:"git"`
	Rg  *string `json:"rg"`
	Go  *string `json:"go"`
}

// Environment records isolation and calibration.
type Environment struct {
	HomeIsolated    bool         `json:"home_isolated"`
	Stdin           string       `json:"stdin"`
	MarkersWallPass string       `json:"markers_wall_pass"`
	EnvOverrides    EnvOverrides `json:"env_overrides"`
	Calibration     Calibration  `json:"calibration"`
	Quality         EnvQuality   `json:"quality"`
}

// EnvOverrides records allowlist construction.
type EnvOverrides struct {
	Removed []string          `json:"removed,omitempty"`
	Set     map[string]string `json:"set,omitempty"`
}

// Calibration quantifies runner overhead and drift. It is reported,
// never subtracted.
type Calibration struct {
	SpawnFloorMs CalibrationPair `json:"spawn_floor_ms"`
}

// CalibrationPair brackets the run.
type CalibrationPair struct {
	Start CalibrationPoint `json:"start"`
	End   CalibrationPoint `json:"end"`
}

// CalibrationPoint summarizes the __noop distribution.
type CalibrationPoint struct {
	P50 float64  `json:"p50"`
	P95 *float64 `json:"p95,omitempty"`
	N   int      `json:"n"`
}

// EnvQuality records noise detection.
type EnvQuality struct {
	IdleCPUPct         *float64 `json:"idle_cpu_pct,omitempty"`
	DriftSpawnFloorPct *float64 `json:"drift_spawn_floor_pct,omitempty"`
	Flags              []string `json:"flags,omitempty"`
}

// Benchmark is one benchmark's raw samples plus derived statistics.
type Benchmark struct {
	ID                string            `json:"id"`
	DefinitionVersion int               `json:"definition_version"`
	Tier              int               `json:"tier"`
	Kind              string            `json:"kind"`
	Status            string            `json:"status"`
	StatusDetail      *string           `json:"status_detail,omitempty"`
	Subject           string            `json:"subject,omitempty"`
	Params            map[string]string `json:"params,omitempty"`
	Plan              PlanOut           `json:"plan,omitempty"`
	Validity          *Validity         `json:"validity,omitempty"`
	Iterations        []Iteration       `json:"iterations,omitempty"`
	Metrics           []Metric          `json:"metrics,omitempty"`
	Flags             []string          `json:"flags,omitempty"`
	Warnings          []string          `json:"warnings,omitempty"`
	Error             *BenchError       `json:"error,omitempty"`
}

// PlanOut records the resolved iteration plan so a run is
// reproducible from its own output.
type PlanOut struct {
	Warmup      int  `json:"warmup"`
	N           int  `json:"n"`
	Interleaved bool `json:"interleaved"`
}

// Validity carries the predicate and pre/post marker probes.
type Validity struct {
	Predicate string `json:"predicate"`
	ProbePre  *Probe `json:"probe_pre,omitempty"`
	ProbePost *Probe `json:"probe_post,omitempty"`
}

// Probe is one marker-pass confirmation run. Checks holds
// benchmark-specific confirmations (e.g. "stage_agents_seen").
type Probe struct {
	OK     bool            `json:"ok"`
	Checks map[string]bool `json:"checks,omitempty"`
}

// Iteration is one stored sample. Warm-up samples are stored with
// phase "warmup" and never enter statistics. Values carries the
// observed metric values so warm-up drift stays inspectable;
// metrics[].values repeats the measure-phase values for analysis.
type Iteration struct {
	I         int                `json:"i"`
	Phase     string             `json:"phase"`
	Valid     bool               `json:"valid"`
	TOffsetMs float64            `json:"t_offset_ms"`
	Attrs     map[string]string  `json:"attrs,omitempty"`
	Values    map[string]float64 `json:"values,omitempty"`
}

// Metric owns one distribution.
type Metric struct {
	Name              string      `json:"name"`
	Unit              string      `json:"unit"`
	Direction         string      `json:"direction"`
	PlatformSemantics string      `json:"platform_semantics,omitempty"`
	Values            []float64   `json:"values"`
	Statistics        *Statistics `json:"statistics,omitempty"`
}

// Statistics are derived and recomputable from Values. Decisions use
// median/MAD/quantiles; mean/stdev are reported but never decide.
// Percentiles below their minimum n are null with a Nulls reason:
// p90 needs n>=10, p95 needs n>=20, p99 needs n>=100.
type Statistics struct {
	N        int               `json:"n"`
	ValidN   int               `json:"valid_n"`
	Min      float64           `json:"min"`
	P50      float64           `json:"p50"`
	P90      *float64          `json:"p90"`
	P95      *float64          `json:"p95"`
	P99      *float64          `json:"p99"`
	Max      float64           `json:"max"`
	Mean     float64           `json:"mean"`
	Stdev    float64           `json:"stdev"`
	MAD      float64           `json:"mad"`
	IQR      float64           `json:"iqr"`
	RobustCV float64           `json:"robust_cv"`
	CI95P50  [2]float64        `json:"ci95_p50"`
	Nulls    map[string]string `json:"nulls,omitempty"`
}

// BenchError classifies failures as data.
type BenchError struct {
	Code    string `json:"code"`
	Phase   string `json:"phase"`
	Message string `json:"message"`
}

// Error codes.
const (
	ErrRequirementUnmet    = "requirement_unmet"
	ErrUnsupportedPlatform = "unsupported_platform"
	ErrSetupFailed         = "setup_failed"
	ErrSpawnFailed         = "spawn_failed"
	ErrTimeout             = "timeout"
	ErrInvalidWorkload     = "invalid_workload"
	ErrMarkerProtocol      = "marker_protocol_error"
	ErrInternal            = "internal"
)
