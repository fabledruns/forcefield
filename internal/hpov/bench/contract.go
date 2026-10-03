package bench

import (
	"fmt"
	"strconv"
	"strings"
)

// SpawnAnchor names the process spawn instant as a segment endpoint.
// It is not a marker: nothing emits it, and a segment may reference it
// instead of a mark.
const SpawnAnchor = "\x00spawn"

// Contract is the subject-specific workload semantics a benchmark needs:
// how to invoke the executable, what output and exit status count as
// reaching the intended boundary, which instrumentation marks exist and
// when the subject is "ready", and which environment variables belong
// to the subject rather than to the harness.
//
// The contract is data. The generic engine never encodes a product's
// command syntax, output text, exit codes or marker names; benchmarks
// read them from here. A subject whose contract omits a section cannot
// be measured by the benchmarks that need it, and those benchmarks
// report an explicit unsupported state instead of guessing.
//
// Field semantics:
//
//   - Product is the subject identity recorded in the result document.
//   - Bin is the short command name used in workload descriptions.
//   - A section with no Args is undefined: the benchmarks that require
//     it report unsupported rather than inventing a command.
type Contract struct {
	// Product identifies the measured harness in results.
	Product string `json:"product"`
	// Bin is the command name as invoked, used in workload text.
	Bin string `json:"bin,omitempty"`
	// MarkerPrefix discriminates a subject instrumentation marker line,
	// e.g. "ff-perf ". Empty means the subject exposes no markers.
	MarkerPrefix string `json:"marker_prefix,omitempty"`
	// EnableEnv turns the subject's instrumentation on. It applies to
	// every pass that needs markers, headless and interactive alike, so
	// it belongs to the subject rather than to one of its workloads.
	EnableEnv map[string]string `json:"enable_env,omitempty"`
	Version   Probe             `json:"version"`
	Help      Probe             `json:"help"`
	Headless  Headless          `json:"headless"`
	TUI       TUI               `json:"tui"`
	Env       Env               `json:"env"`
}

// Probe is a one-shot command whose output identifies the subject: the
// version and help invocations.
type Probe struct {
	// Args is the argv after the executable. Empty means undefined.
	Args []string `json:"args,omitempty"`
	// ExitCode is the required exit status. Nil means 0.
	ExitCode *int `json:"exit_code,omitempty"`
	// StdoutPrefix must start the first stdout line.
	StdoutPrefix string `json:"stdout_prefix,omitempty"`
	// StdoutContains must appear anywhere in stdout.
	StdoutContains string `json:"stdout_contains,omitempty"`
}

// Defined reports whether the probe declares a command.
func (p Probe) Defined() bool { return len(p.Args) > 0 }

// WantExit returns the required exit status, defaulting to 0.
func (p Probe) WantExit() int {
	if p.ExitCode == nil {
		return 0
	}
	return *p.ExitCode
}

// Predicate is the human-readable validity rule recorded in results.
func (p Probe) Predicate() string {
	var b strings.Builder
	fmt.Fprintf(&b, "exit_code==%d", p.WantExit())
	switch {
	case p.StdoutPrefix != "":
		fmt.Fprintf(&b, " and stdout starts with '%s'", p.StdoutPrefix)
	case p.StdoutContains != "":
		fmt.Fprintf(&b, " and stdout contains '%s'", p.StdoutContains)
	}
	return b.String()
}

// Headless is the non-interactive workload: one command whose normal,
// successful completion is a deliberate non-zero exit at a validation
// boundary. It is the workload behind the launch and headless memory
// benchmarks.
type Headless struct {
	// Args is the argv after the executable. Empty means undefined.
	Args []string `json:"args,omitempty"`
	// ExitCode is the required exit status at the boundary.
	ExitCode int `json:"exit_code"`
	// Primed requests one untimed priming run per fixture so measured
	// iterations start from identical preconditions. Subjects whose
	// first invocation creates persistent configuration need it;
	// subjects that are stateless on every run do not.
	Primed bool `json:"primed"`
	// ProbeChecks are the named conditions the validity probe confirms
	// around the measured pass.
	ProbeChecks []ProbeCheck `json:"probe_checks,omitempty"`
}

// ProbeCheck is one named condition the headless probe confirms. It
// matches either captured stderr text or an instrumentation marker.
type ProbeCheck struct {
	// Name is the check key recorded in the result document.
	Name string `json:"name"`
	// Substring must appear in captured stderr.
	Substring string `json:"substring,omitempty"`
	// Marker must have been emitted, parsed with the headless contract's
	// marker prefix.
	Marker string `json:"marker,omitempty"`
}

// Defined reports whether the workload declares a command.
func (h Headless) Defined() bool { return len(h.Args) > 0 }

// Predicate is the human-readable validity rule recorded in results.
func (h Headless) Predicate() string {
	parts := make([]string, 0, len(h.ProbeChecks))
	for _, c := range h.ProbeChecks {
		parts = append(parts, c.Name)
	}
	p := fmt.Sprintf("exit_code==%d", h.ExitCode)
	if len(parts) > 0 {
		p += "; pre/post probe: " + strings.Join(parts, " and ")
	}
	return p
}

// TUI is the interactive contract: how to start the subject under a pty,
// which instrumentation marks it emits, which mark means "ready", and
// how to end the session cleanly.
type TUI struct {
	// Args is the argv after the executable. Empty means a bare launch.
	Args []string `json:"args,omitempty"`
	// PrimaryMark is the readiness boundary: the first mark at which the
	// session can accept a task. Its absence is the only condition that
	// invalidates a sample.
	PrimaryMark string `json:"primary_mark,omitempty"`
	// Marks is the full reported mark set, in report order.
	Marks []string `json:"marks,omitempty"`
	// Segments are reported deltas between marks, or from spawn.
	Segments []Segment `json:"segments,omitempty"`
	// ReadinessTailMark bounds collection after readiness so a subject
	// that never emits it cannot stretch every iteration. Empty means
	// collection stops at readiness.
	ReadinessTailMark string `json:"readiness_tail_mark,omitempty"`
	// QuitInput ends the session cleanly once readiness is observed.
	QuitInput string `json:"quit_input,omitempty"`
	// QuitKey is the submit key sent after QuitInput as a separate
	// event. Empty means "\r".
	QuitKey string `json:"quit_key,omitempty"`
	// RequireExitZero requires a clean exit after the quit input.
	RequireExitZero bool `json:"require_exit_zero"`
	// HeapFields declares that the primary mark carries alloc=/sys=
	// fields describing the subject's language runtime. It gates the
	// implementation-specific heap metrics, which no other subject can
	// satisfy.
	HeapFields bool `json:"heap_fields"`
}

// Defined reports whether the contract declares any interactive
// instrumentation. An undefined TUI contract makes the pty benchmarks
// unsupported for this subject.
func (t TUI) Defined() bool { return t.PrimaryMark != "" }

// Missing lists what a TUI contract still needs before the pty
// benchmarks can run. Empty means the contract is complete.
func (t TUI) Missing() []string {
	var out []string
	if t.PrimaryMark == "" {
		out = append(out, "primary_mark")
	}
	if len(t.Marks) == 0 {
		out = append(out, "marks")
	}
	if t.QuitInput == "" {
		out = append(out, "quit_input")
	}
	return out
}

// Submit returns the quit text and the key that submits it.
func (t TUI) Submit() (text, key string) {
	key = t.QuitKey
	if key == "" {
		key = "\r"
	}
	return t.QuitInput, key
}

// ReadinessPhrase names the readiness boundary in prose for the
// recorded validity predicate.
func (t TUI) ReadinessPhrase() string {
	if t.PrimaryMark == "" {
		return "readiness boundary"
	}
	return "readiness mark " + t.PrimaryMark
}

// Metrics derives the benchmark's owned metrics from the contract:
// one per reported mark, then one per segment. Two subjects with
// different marker sets therefore report different metrics and are
// comparable only where they coincide.
func (t TUI) Metrics() []MetricSpec {
	out := make([]MetricSpec, 0, len(t.Marks)+len(t.Segments))
	for _, ev := range t.Marks {
		out = append(out, MetricSpec{Name: MarkMetric(ev), Unit: "ms", Direction: LowerIsBetter})
	}
	for _, s := range t.Segments {
		out = append(out, MetricSpec{Name: s.Name, Unit: "ms", Direction: LowerIsBetter})
	}
	return out
}

// Segment is one reported delta between two marks, or between a mark and
// the spawn instant named by SpawnAnchor.
type Segment struct {
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

// Env is the subject's environment contract: which inherited variables
// must not reach a measured process, and which the harness itself sets.
type Env struct {
	// ScrubPrefixes are inherited variables dropped from every measured
	// child, matched case-insensitively on the name prefix.
	ScrubPrefixes []string `json:"scrub_prefixes,omitempty"`
	// ScrubExact are inherited variable names dropped from every
	// measured child.
	ScrubExact []string `json:"scrub_exact,omitempty"`
	// Passthrough is forwarded into the scrubbed child environment on
	// top of the harness set. Production profiles leave it empty; it
	// exists so a hermetic test can steer a re-exec fake subject.
	Passthrough map[string]string `json:"passthrough,omitempty"`
}

// MarkMetric is the metric name for a mark's process-relative time.
func MarkMetric(event string) string {
	return "t_" + strings.ReplaceAll(event, "-", "_")
}

// Method renders a workload description such as "ff --version".
func (c Contract) Method(args []string) string {
	if c.Bin == "" {
		return strings.Join(args, " ")
	}
	if len(args) == 0 {
		return c.Bin
	}
	return c.Bin + " " + strings.Join(args, " ")
}

// Validate reports why a contract cannot drive a measurement. It is
// called when a profile is loaded, so a malformed contract fails at the
// command line instead of silently invalidating every sample.
func (c Contract) Validate() error {
	if strings.TrimSpace(c.Product) == "" {
		return fmt.Errorf("contract: product is required")
	}
	if c.Version.Defined() && c.Help.Defined() && c.Bin == "" {
		return fmt.Errorf("contract: bin is required when a probe command is declared")
	}
	if c.TUI.Defined() {
		if missing := c.TUI.Missing(); len(missing) > 0 {
			return fmt.Errorf("contract: tui is missing %s", strings.Join(missing, ", "))
		}
		if c.MarkerPrefix == "" {
			return fmt.Errorf("contract: tui requires marker_prefix")
		}
		for _, s := range c.TUI.Segments {
			if s.Name == "" || s.From == "" || s.To == "" {
				return fmt.Errorf("contract: tui segment %q needs name, from and to", s.Name)
			}
		}
	}
	for _, c2 := range c.Headless.ProbeChecks {
		if c2.Name == "" || (c2.Substring == "" && c2.Marker == "") {
			return fmt.Errorf("contract: probe check %q needs a name and a substring or marker", c2.Name)
		}
		if c2.Marker != "" && c.MarkerPrefix == "" {
			return fmt.Errorf("contract: probe check %q needs marker_prefix", c2.Name)
		}
	}
	return nil
}

// ExitCode is a small helper for profiles that declare a required exit
// status in JSON.
func ExitCode(n int) *int { return &n }

// Itoa renders an exit status for prose and check keys.
func Itoa(n int) string { return strconv.Itoa(n) }
