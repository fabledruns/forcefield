package landlock

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Spike policy transport: 8-byte magic, uint32 big-endian body length,
// JSON body. The cap bounds how much the helper reads before parsing
// so a hostile or corrupted pipe cannot exhaust memory.
var policyMagic = []byte("FFSBXLP1")

// PolicyVersion is the only policy schema the spike helper accepts.
const PolicyVersion = 1

// MaxPolicyBytes caps the JSON body the helper will read.
const MaxPolicyBytes = 1 << 20

// ExitSetupFailed is the helper exit code for setup failures
// (malformed policy, unavailable syscalls, ruleset errors). It is
// deliberately distinct from target-command failures so parents can
// tell "the boundary was not established" from "the command failed".
const ExitSetupFailed = 126

// SentinelPrefix marks helper setup-failure diagnostics on stderr:
// "FFSBX:<kind>:<detail>", where kind is POLICY, NNP, UNAVAILABLE, or
// SETUP. Target-command failures never carry this prefix.
const SentinelPrefix = "FFSBX:"

// Rule grants filesystem access beneath Path. ReadOnly limits the
// grant to read/execute; otherwise the rule grants the full ABI-masked
// right set the ruleset handles. Access, when nonzero, overrides both:
// the rule grants exactly those rights masked to the handled set. It
// exists for narrow file grants (e.g. /dev/null read+write) that must
// not widen to a whole directory.
type Rule struct {
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only,omitempty"`
	Access   uint64 `json:"access,omitempty"`
}

// Policy is the versioned helper input contract.
type Policy struct {
	Version    int    `json:"version"`
	NoNewPrivs bool   `json:"no_new_privs"`
	Allowed    []Rule `json:"allowed"`
}

// WritePolicy encodes p for transport to the helper.
func WritePolicy(w io.Writer, p Policy) error {
	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("landlock spike: encode policy: %w", err)
	}
	header := make([]byte, 8+4)
	copy(header, policyMagic)
	binary.BigEndian.PutUint32(header[8:], uint32(len(body)))
	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("landlock spike: write policy header: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("landlock spike: write policy body: %w", err)
	}
	return nil
}

// ReadPolicy decodes and validates a transported policy. Every
// malformed or hostile input is a descriptive error, never a partial
// policy: bad magic, oversized length, truncated reads, malformed
// JSON, trailing data, unknown fields, wrong version, empty rule set,
// or empty rule paths all fail before anything is applied.
//
// The contract intentionally stops at syntactic validity: it does not
// require absolute or canonicalized rule paths. That check belongs to
// the production caller (trusted-harness-only here); a relative rule
// path would resolve against the helper's inherited working directory.
func ReadPolicy(r io.Reader) (Policy, error) {
	header := make([]byte, 8+4)
	if _, err := io.ReadFull(r, header); err != nil {
		return Policy{}, fmt.Errorf("landlock spike: truncated policy header: %w", err)
	}
	if !bytes.Equal(header[:8], policyMagic) {
		return Policy{}, fmt.Errorf("landlock spike: bad policy magic %q", header[:8])
	}
	n := binary.BigEndian.Uint32(header[8:])
	if n > MaxPolicyBytes {
		return Policy{}, fmt.Errorf("landlock spike: policy body %d bytes exceeds %d cap", n, MaxPolicyBytes)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return Policy{}, fmt.Errorf("landlock spike: truncated policy body: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var p Policy
	if err := dec.Decode(&p); err != nil {
		return Policy{}, fmt.Errorf("landlock spike: malformed policy JSON: %w", err)
	}
	// A second value (suffix garbage, concatenated objects) must not
	// silently pass: the body must be exactly one JSON value.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return Policy{}, fmt.Errorf("landlock spike: trailing data after policy JSON")
	}
	if p.Version != PolicyVersion {
		return Policy{}, fmt.Errorf("landlock spike: unsupported policy version %d (want %d)", p.Version, PolicyVersion)
	}
	if len(p.Allowed) == 0 {
		return Policy{}, fmt.Errorf("landlock spike: policy names no allowed paths")
	}
	for i, rule := range p.Allowed {
		if rule.Path == "" {
			return Policy{}, fmt.Errorf("landlock spike: rule %d has an empty path", i)
		}
	}
	return p, nil
}
