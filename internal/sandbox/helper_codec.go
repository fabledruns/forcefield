package sandbox

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"

	"forcefield/internal/sandbox/landlock"
)

// HelperArg is the argv sentinel that selects the isolated-execution
// helper in main.go: `ff __sandbox-exec` reads one policy message
// from fd 3, confines itself, and execs the shell. It never
// constructs the runtime.
const HelperArg = "__sandbox-exec"

// helperMagic frames helper requests; helperVersion is the only
// schema the helper accepts; maxHelperBytes bounds the read.
var helperMagic = []byte("FFSBXEX1")

const helperVersion = 1

const maxHelperBytes = 1 << 20

// helperRequest is the versioned helper input contract. Rules reuse
// landlock.Rule (same allow semantics as the spike); Command/Dir/Env
// describe the shell invocation; Bash is the absolute interpreter
// path resolved by the parent.
type helperRequest struct {
	Version    int             `json:"version"`
	Command    string          `json:"command"`
	Dir        string          `json:"dir"`
	Env        []string        `json:"env"`
	Bash       string          `json:"bash"`
	Rules      []landlock.Rule `json:"rules"`
	NoNewPrivs bool            `json:"no_new_privs"`
}

// writeHelperRequest encodes req for transport to the helper. The
// framing mirrors the spike policy transport (magic, big-endian
// length, strict JSON) without sharing code: the spike contract is
// frozen, and this one carries the shell invocation with it.
func writeHelperRequest(w io.Writer, req helperRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode helper request: %w", err)
	}
	header := make([]byte, 8+4)
	copy(header, helperMagic)
	binary.BigEndian.PutUint32(header[8:], uint32(len(body)))
	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("write helper request header: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("write helper request body: %w", err)
	}
	return nil
}

// readHelperRequest decodes and validates a transported request.
// Every malformed input is a descriptive error, never a partial
// request: bad magic, oversized length, truncated reads, malformed
// or trailing JSON, unknown fields, wrong version, empty command,
// bash, dir, or rule set all fail before anything is applied.
func readHelperRequest(r io.Reader) (helperRequest, error) {
	header := make([]byte, 8+4)
	if _, err := io.ReadFull(r, header); err != nil {
		return helperRequest{}, fmt.Errorf("helper: truncated request header: %w", err)
	}
	if !bytes.Equal(header[:8], helperMagic) {
		return helperRequest{}, fmt.Errorf("helper: bad request magic %q", header[:8])
	}
	n := binary.BigEndian.Uint32(header[8:])
	if n > maxHelperBytes {
		return helperRequest{}, fmt.Errorf("helper: request body %d bytes exceeds %d cap", n, maxHelperBytes)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return helperRequest{}, fmt.Errorf("helper: truncated request body: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var req helperRequest
	if err := dec.Decode(&req); err != nil {
		return helperRequest{}, fmt.Errorf("helper: malformed request JSON: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return helperRequest{}, fmt.Errorf("helper: trailing data after request JSON")
	}
	if req.Version != helperVersion {
		return helperRequest{}, fmt.Errorf("helper: unsupported request version %d (want %d)", req.Version, helperVersion)
	}
	if req.Command == "" {
		return helperRequest{}, fmt.Errorf("helper: empty command")
	}
	if req.Bash == "" {
		return helperRequest{}, fmt.Errorf("helper: empty interpreter path")
	}
	if req.Dir == "" {
		return helperRequest{}, fmt.Errorf("helper: empty working directory")
	}
	if len(req.Rules) == 0 {
		return helperRequest{}, fmt.Errorf("helper: request names no allow rules")
	}
	return req, nil
}
