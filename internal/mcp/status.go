package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Persisted MCP status (Phase 7).
//
// The status file lets `ff doctor` report the last known MCP state
// without starting servers. It is written by the runtime at lifecycle
// boundaries (startup, shutdown) and read by doctor. Contents are
// strictly bounded diagnostics: server keys, readiness flags, tool
// names, scrubbed error tails. It never contains environment values,
// secrets, credentials, tool arguments, tool results, or unscrubbed
// stderr. The config fingerprint covers the full server configuration
// (values hashed, never stored) so doctor can label stale status.

const (
	// StatusVersion is the status file schema version. Readers reject
	// anything else as corrupt rather than guessing.
	StatusVersion = 1
	// StatusFileName is the status file name under .forcefield/.
	StatusFileName = "mcp-status.json"
	// MaxStatusStderrChars bounds the stderr tail kept per server in the
	// status file. Snapshot tails are already bounded (32 KiB ring); the
	// status keeps only the most recent slice so one chatty server cannot
	// bloat the file.
	MaxStatusStderrChars = 4096
	// MaxStatusTools bounds tool names kept per server. Discovery already
	// caps tools per server below this; the bound is defense in depth.
	MaxStatusTools = 256
	// MaxStatusServers bounds servers accepted from a status file. Real
	// files hold at most MaxServers entries; anything larger is corrupt.
	MaxStatusServers = 1024
	// MaxStatusFileBytes bounds the status file read. Legitimate files
	// hold at most MaxServers servers of bounded diagnostics each, so
	// anything larger is corrupt rather than a large-but-valid state.
	// The cap keeps a hostile or damaged file from forcing unbounded
	// allocation before parsing even begins.
	MaxStatusFileBytes = 1 << 20
	// MaxStatusKeyRunes bounds one server key read back from a status
	// file. Keys are 1-32 runes when written by NewStatusFile; longer is
	// corrupt input, and doctor prints keys verbatim.
	MaxStatusKeyRunes = 128
)

// ServerStatus is one server's persisted last-known state.
type ServerStatus struct {
	Key      string   `json:"key"`
	Enabled  bool     `json:"enabled"`
	Ready    bool     `json:"ready"`
	Healthy  bool     `json:"healthy"`
	Started  bool     `json:"started"`
	Exited   bool     `json:"exited"`
	ExitCode int      `json:"exit_code"`
	Tools    []string `json:"tools,omitempty"`
	// LastError is the bounded, scrubbed diagnostic from the snapshot.
	LastError string `json:"last_error,omitempty"`
	// StderrTail is the bounded, scrubbed stderr tail from the snapshot.
	StderrTail string `json:"stderr_tail,omitempty"`
}

// StatusFile is the persisted document. Servers are sorted by key so
// serialization is deterministic.
type StatusFile struct {
	Version      int            `json:"version"`
	ConfigSHA256 string         `json:"config_sha256"`
	UpdatedUnix  int64          `json:"updated_unix"`
	Servers      []ServerStatus `json:"servers,omitempty"`
}

// FingerprintConfig hashes the MCP configuration canonically (sorted
// server keys, sorted env keys, Enabled normalized) so doctor can tell
// whether a status file still describes the current configuration.
// Values contribute to the hash only; they are never stored or printed.
func FingerprintConfig(cfg Config) string {
	type serverFP struct {
		Command        string            `json:"command"`
		Args           []string          `json:"args"`
		Cwd            string            `json:"cwd"`
		Env            map[string]string `json:"env"`
		EnvPassthrough []string          `json:"env_passthrough"`
		TimeoutSeconds float64           `json:"timeout_seconds"`
		Enabled        bool              `json:"enabled"`
	}
	keys := make([]string, 0, len(cfg.Servers))
	for k := range cfg.Servers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make(map[string]serverFP, len(keys))
	for _, k := range keys {
		s := cfg.Servers[k]
		env := make(map[string]string, len(s.Env))
		for ek, ev := range s.Env {
			env[ek] = ev
		}
		ordered[k] = serverFP{
			Command:        s.Command,
			Args:           append([]string(nil), s.Args...),
			Cwd:            s.Cwd,
			Env:            env,
			EnvPassthrough: append([]string(nil), s.EnvPassthrough...),
			TimeoutSeconds: s.TimeoutSeconds,
			Enabled:        s.IsEnabled(),
		}
	}
	raw, _ := json.Marshal(map[string]any{"servers": ordered})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// NewStatusFile builds a persistable snapshot from a Host snapshot. now
// is seconds since the epoch (time.Now().Unix at the call site, kept out
// of this package's signature for testability).
func NewStatusFile(snap HostSnapshot, cfg Config, now int64) StatusFile {
	st := StatusFile{Version: StatusVersion, ConfigSHA256: FingerprintConfig(cfg), UpdatedUnix: now}
	for _, ss := range snap.Servers {
		tools := make([]string, 0, len(ss.Tools))
		for _, t := range ss.Tools {
			if t == nil {
				continue
			}
			tools = append(tools, t.Name())
			if len(tools) >= MaxStatusTools {
				break
			}
		}
		sort.Strings(tools)
		st.Servers = append(st.Servers, ServerStatus{
			Key:        ss.Key,
			Enabled:    ss.Enabled,
			Ready:      ss.Ready,
			Healthy:    ss.Healthy,
			Started:    ss.Started,
			Exited:     ss.Exited,
			ExitCode:   ss.ExitCode,
			Tools:      tools,
			LastError:  truncateRunes(ss.LastError, MaxErrorDetailRunes),
			StderrTail: truncateRunes(ss.Stderr, MaxStatusStderrChars),
		})
	}
	sort.Slice(st.Servers, func(i, j int) bool { return st.Servers[i].Key < st.Servers[j].Key })
	return st
}

// CurrentFor reports whether the status still describes cfg. Any config
// change (including env values, which only affect the hash) makes prior
// status stale. Doctor must label stale status, never present it as
// current, and never delete it for being stale.
func (s *StatusFile) CurrentFor(cfg Config) bool {
	if s == nil {
		return false
	}
	return s.ConfigSHA256 != "" && s.ConfigSHA256 == FingerprintConfig(cfg)
}

// StatusFilePath returns the status path under root/.forcefield,
// mirroring session storage layout.
func StatusFilePath(root string) string {
	return filepath.Join(root, ".forcefield", StatusFileName)
}

// WriteStatusFile persists st atomically (temp file + flush + rename)
// with mode 0600. It creates the parent directory (0700) when missing.
// Errors are returned for the caller to warn-and-continue on; writing
// status must never fail a healthy runtime.
func WriteStatusFile(root string, st StatusFile) error {
	dir := filepath.Join(root, ".forcefield")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mcp status: create dir: %w", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("mcp status: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, StatusFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("mcp status: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("mcp status: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("mcp status: flush: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("mcp status: close: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("mcp status: chmod: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, StatusFileName)); err != nil {
		return fmt.Errorf("mcp status: replace: %w", err)
	}
	return nil
}

// ErrStatusNotFound marks a missing status file: no run has recorded MCP
// state here yet. It is not an error condition for doctor.
var ErrStatusNotFound = fmt.Errorf("mcp status: no status file")

// ReadStatusFile loads the status file. A missing file returns
// ErrStatusNotFound. Garbage, wrong versions, oversize files, or absurd
// shapes return a corrupt error naming the file (never panicking);
// doctor reports those as warnings since the file is rewritten on the
// next run. Fields that exceed their documented bounds are clamped, so a
// hand-edited file can neither bloat memory nor leak unbounded text
// into doctor output.
func ReadStatusFile(root string) (*StatusFile, error) {
	path := StatusFilePath(root)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrStatusNotFound
		}
		return nil, fmt.Errorf("mcp status: read %s: %w", path, err)
	}
	defer f.Close()
	// One byte past the cap distinguishes "exactly at cap" from "over":
	// only the latter is corrupt.
	raw, err := io.ReadAll(io.LimitReader(f, MaxStatusFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("mcp status: read %s: %w", path, err)
	}
	if len(raw) > MaxStatusFileBytes {
		return nil, fmt.Errorf("mcp status: corrupt file %s: exceeds %d bytes", path, MaxStatusFileBytes)
	}
	var st StatusFile
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("mcp status: corrupt file %s: %w", path, err)
	}
	if st.Version != StatusVersion {
		return nil, fmt.Errorf("mcp status: corrupt file %s: unsupported version %d", path, st.Version)
	}
	if len(st.Servers) > MaxStatusServers {
		return nil, fmt.Errorf("mcp status: corrupt file %s: %d servers", path, len(st.Servers))
	}
	for i := range st.Servers {
		ss := &st.Servers[i]
		ss.Key = truncateRunes(ss.Key, MaxStatusKeyRunes)
		if len(ss.Tools) > MaxStatusTools {
			ss.Tools = append([]string(nil), ss.Tools[:MaxStatusTools]...)
		}
		ss.LastError = truncateRunes(ss.LastError, MaxErrorDetailRunes)
		ss.StderrTail = truncateRunes(ss.StderrTail, MaxStatusStderrChars)
	}
	return &st, nil
}

// truncateRunes cuts s to at most n runes. Non-positive n keeps s
// unchanged (callers pass positive bounds).
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
