package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Stdio transport: newline-delimited JSON-RPC 2.0 over arbitrary streams.
//
// The transport owns no process: it reads frames from an io.Reader and
// writes frames to an io.Writer supplied by the caller, so deterministic
// tests use in-memory streams and a later Host layer can attach a real
// subprocess's pipes. Framing, size enforcement, request correlation,
// cancellation, failure propagation, and shutdown live here; MCP method
// semantics (initialize, tools/list, tools/call) belong to later layers.
//
// Trust posture: every inbound byte is untrusted. Frames are size-capped
// before parsing, parsed through ParseMessage (never duplicated here),
// and correlated by canonical NormalizeID keys. Malformed lines,
// server-initiated requests, and unknown response IDs are dropped without
// touching pending state; oversize frames, stream errors, and EOF are
// terminal failures that fail every pending request exactly once.

// outcome is one pending request's terminal delivery. Exactly one outcome
// is ever sent per request; the channel is buffered so delivery never
// blocks on a caller that already left via cancellation.
type outcome struct {
	msg Message
	err error
}

// TransportStats counts safely ignored inbound traffic. Counters exist so
// tests and future diagnostics can prove Massage-handling discipline;
// they never influence execution.
type TransportStats struct {
	// SkippedLines counts malformed, batch, or otherwise unparsable lines
	// dropped without touching pending state.
	SkippedLines uint64
	// DroppedNotifications counts inbound notifications identified and
	// ignored (v1 implements no notification semantics).
	DroppedNotifications uint64
	// UnknownResponses counts responses with no matching pending request
	// (unknown IDs and late arrivals after cancel/timeout/shutdown).
	UnknownResponses uint64
}

// Transport is one bidirectional JSON-RPC stream. It is safe for
// concurrent use: Request and Notify may run from many goroutines (the
// scheduler fans tool calls out) while a single owned reader goroutine
// dispatches inbound frames.
type Transport struct {
	r io.Reader
	w io.Writer

	// mu guards pending, terminal, and closed. The pending map is keyed
	// by canonical NormalizeID strings and holds exactly the requests
	// with no terminal outcome yet. terminal is the first terminal
	// error (peer failure or shutdown); once set, the transport refuses
	// new work and every later operation fails fast with it.
	mu       sync.Mutex
	pending  map[string]chan outcome
	terminal error
	closed   bool

	// writeMu serializes outbound frames so concurrent writers can never
	// interleave bytes on the wire. It is separate from mu so a blocked
	// stream write cannot stall shutdown bookkeeping; shutdown races are
	// resolved by rechecking terminal under mu after acquiring writeMu.
	writeMu sync.Mutex

	// nextID allocates monotonic request IDs starting at 1. IDs are never
	// recycled; exhaustion (past MaxInt64) fails safely instead of
	// wrapping into a live pending key.
	nextID atomic.Int64

	// done closes when the reader goroutine exits. The transport never
	// closes the underlying streams: their lifetime belongs to the owner
	// (a later Host layer, or the test harness).
	done chan struct{}

	skippedLines         atomic.Uint64
	droppedNotifications atomic.Uint64
	unknownResponses     atomic.Uint64
}

// NewTransport attaches a transport to r and w and starts its single
// reader goroutine. r and w must be non-nil; the transport never closes
// them. Use Done to observe reader termination and Close for deterministic
// shutdown.
func NewTransport(r io.Reader, w io.Writer) (*Transport, error) {
	if r == nil || w == nil {
		return nil, fmt.Errorf("mcp: transport requires non-nil reader and writer")
	}
	t := &Transport{
		r:       r,
		w:       w,
		pending: make(map[string]chan outcome),
		done:    make(chan struct{}),
	}
	go t.readLoop()
	return t, nil
}

// Request sends one JSON-RPC request and waits for its correlated
// response. The terminal outcome is exactly one of: the peer's response,
// the request context's error, or the transport's terminal error. Late
// responses after cancellation are dropped by ID and never delivered.
// Server error-responses surface as (*RPCError); use errors.As to read
// the code. The transport stays usable after a canceled request.
func (t *Transport) Request(ctx context.Context, method string, params any) (Message, error) {
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}
	id, err := t.allocID()
	if err != nil {
		return Message{}, err
	}
	frame, err := MarshalRequest(id, method, params)
	if err != nil {
		return Message{}, err
	}
	key, err := canonicalRequestKey(id)
	if err != nil {
		return Message{}, err
	}
	ch := make(chan outcome, 1)
	t.mu.Lock()
	if t.terminal != nil {
		terr := t.terminal
		t.mu.Unlock()
		return Message{}, terr
	}
	t.pending[key] = ch
	t.mu.Unlock()

	if t.writeFrame(frame) {
		// Shutdown or a write failure settled our outcome already.
		res := <-ch
		return deliver(res)
	}

	select {
	case res := <-ch:
		return deliver(res)
	case <-ctx.Done():
		t.mu.Lock()
		if _, ok := t.pending[key]; ok {
			delete(t.pending, key)
			t.mu.Unlock()
			return Message{}, ctx.Err()
		}
		// The response won the race: it was already deleted and sent.
		t.mu.Unlock()
		res := <-ch
		return deliver(res)
	}
}

// deliver maps a terminal outcome onto Request's return contract:
// transport errors pass through, server error-responses surface as
// (*RPCError), and result responses return the message.
func deliver(res outcome) (Message, error) {
	if res.err != nil {
		return Message{}, res.err
	}
	if res.msg.Error != nil {
		return Message{}, res.msg.Error
	}
	return res.msg, nil
}

// Notify sends one JSON-RPC notification (no reply expected). It never
// creates pending state, never retries, and fails fast after shutdown.
func (t *Transport) Notify(method string, params any) error {
	frame, err := MarshalNotification(method, params)
	if err != nil {
		return err
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	t.mu.Lock()
	term := t.terminal
	t.mu.Unlock()
	if term != nil {
		return term
	}
	if err := writeAll(t.w, appendNewline(frame)); err != nil {
		terr := fmt.Errorf("mcp: write failed (%s): %w", quoteBounded(err.Error()), ErrTransport)
		t.failTerminal(terr)
		return terr
	}
	return nil
}

// Close shuts the transport down deterministically: new requests and
// writes fail fast, every pending request receives the shutdown error
// exactly once, and repeated calls are no-ops. It does not close the
// underlying streams and does not wait for the reader goroutine; observe
// Done for reader termination after the owner closes the stream.
func (t *Transport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	if t.terminal == nil {
		t.terminal = fmt.Errorf("mcp: transport closed: %w", ErrClosed)
	}
	pending := t.pending
	t.pending = nil
	terr := t.terminal
	t.mu.Unlock()
	for _, ch := range pending {
		ch <- outcome{err: terr}
	}
	return nil
}

// Done closes when the reader goroutine exits (stream EOF/error after
// shutdown, or terminal stream failure).
func (t *Transport) Done() <-chan struct{} {
	return t.done
}

// Pending reports how many requests currently await a terminal outcome.
// It exists for tests and diagnostics; scheduling decisions never read it.
func (t *Transport) Pending() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pending)
}

// Healthy reports whether the transport still accepts work.
func (t *Transport) Healthy() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.terminal == nil
}

// Stats returns a copy of the ignored-traffic counters.
func (t *Transport) Stats() TransportStats {
	return TransportStats{
		SkippedLines:         t.skippedLines.Load(),
		DroppedNotifications: t.droppedNotifications.Load(),
		UnknownResponses:     t.unknownResponses.Load(),
	}
}

// allocID returns the next monotonic request ID. IDs start at 1 and are
// never recycled; exhaustion fails safely instead of wrapping into a
// live pending key.
func (t *Transport) allocID() (int64, error) {
	id := t.nextID.Add(1)
	if id <= 0 {
		return 0, fmt.Errorf("mcp: request id space exhausted: %w", ErrProtocol)
	}
	return id, nil
}

// canonicalRequestKey computes a pending-map key through the same
// NormalizeID path the reader uses for responses, so equivalent JSON
// numeric forms (1, 1.0, 1e0) resolve to one key by construction rather
// than by parallel implementations.
func canonicalRequestKey(id int64) (string, error) {
	raw, err := json.Marshal(id)
	if err != nil {
		return "", fmt.Errorf("mcp: encode request id: %w", err)
	}
	key, err := NormalizeID(raw)
	if err != nil {
		return "", err
	}
	return key, nil
}

// writeFrame writes one frame plus its newline terminator. It rechecks
// terminal state after acquiring the write lock so no bytes are written
// after shutdown won the race. It returns true when the caller's outcome
// was already settled (shutdown collected it, or the write failed and
// failed the whole transport): the caller must then receive from its
// channel instead of waiting.
func (t *Transport) writeFrame(frame []byte) (settled bool) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	t.mu.Lock()
	term := t.terminal
	t.mu.Unlock()
	if term != nil {
		return true
	}
	if err := writeAll(t.w, appendNewline(frame)); err != nil {
		t.failTerminal(fmt.Errorf("mcp: write failed (%s): %w", quoteBounded(err.Error()), ErrTransport))
		return true
	}
	return false
}

// appendNewline returns frame terminated for the wire. frame is always
// freshly marshalled by the caller, so extending it cannot alias shared
// state.
func appendNewline(frame []byte) []byte {
	return append(frame, '\n')
}

// writeAll writes every byte, tolerating short writes. Writer errors and
// contract violations (negative/overlong counts, zero progress without an
// error) abort the write with no retry: the caller fails the transport.
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if n < 0 || n > len(b) {
			return fmt.Errorf("mcp: writer returned invalid count %d for %d bytes: %w", n, len(b), ErrTransport)
		}
		b = b[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("mcp: writer made no progress: %w", ErrTransport)
		}
	}
	return nil
}

// failTerminal records the first terminal error, fails every pending
// request with it exactly once, and refuses all future work. Later calls
// (including concurrent shutdown) are no-ops: the first failure wins so
// diagnostics name the original cause, not its fallout.
func (t *Transport) failTerminal(err error) {
	t.mu.Lock()
	if t.terminal != nil {
		t.mu.Unlock()
		return
	}
	t.terminal = err
	pending := t.pending
	t.pending = nil
	t.mu.Unlock()
	for _, ch := range pending {
		ch <- outcome{err: err}
	}
}

// isTerminal reports whether shutdown or failure already settled.
func (t *Transport) isTerminal() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.terminal != nil
}

// readLoop is the transport's single reader goroutine. It owns no pending
// state directly: every dispatch goes through the mutex-guarded map, and
// it exits on stream end, stream error, oversize frames, or (after Close)
// when the owner's stream returns. Done closes on exit.
func (t *Transport) readLoop() {
	defer close(t.done)
	const maxLine = MaxMessageBytes + 1 // payload budget plus terminator
	tmp := make([]byte, 32*1024)
	var line []byte
	for {
		n, rerr := t.r.Read(tmp)
		if n > 0 {
			var terminal bool
			line, terminal = t.feed(tmp[:n], line, maxLine)
			if terminal {
				return
			}
		}
		if rerr != nil {
			t.onReadError(rerr, len(line) > 0)
			return
		}
	}
}

// feed consumes one read chunk, dispatching each complete line and
// accumulating the trailing partial line. It returns the carried-over
// partial line and whether the loop must exit (oversize frame, which is a
// terminal failure). Allocation stays bounded: the carry never exceeds
// maxLine bytes plus one parse copy per dispatched line.
func (t *Transport) feed(chunk, line []byte, maxLine int) ([]byte, bool) {
	for len(chunk) > 0 {
		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			if len(line)+len(chunk) > maxLine {
				t.failTerminal(fmt.Errorf("mcp: line exceeds %d byte limit: %w", MaxMessageBytes, ErrTooLarge))
				return nil, true
			}
			line = append(line, chunk...)
			break
		}
		if len(line)+i > MaxMessageBytes {
			t.failTerminal(fmt.Errorf("mcp: line exceeds %d byte limit: %w", MaxMessageBytes, ErrTooLarge))
			return nil, true
		}
		// Copy the frame: ParseMessage retains subslices (params,
		// result), so dispatching over the reusable carry buffer would
		// let the next line corrupt retained messages.
		frame := make([]byte, 0, len(line)+i)
		frame = append(frame, line...)
		frame = append(frame, chunk[:i]...)
		t.dispatch(frame)
		line = line[:0]
		chunk = chunk[i+1:]
	}
	return line, false
}

// dispatch classifies one complete line. Malformed lines, batch arrays,
// server-initiated requests, and notifications are dropped with counters
// and never touch pending state. Responses correlate by canonical ID:
// matches deliver exactly once; unknown or late IDs are dropped.
func (t *Transport) dispatch(frame []byte) {
	msg, err := ParseMessage(frame)
	if err != nil {
		t.skippedLines.Add(1)
		return
	}
	switch msg.Kind {
	case KindResponse:
		t.mu.Lock()
		ch, ok := t.pending[msg.ID]
		if ok {
			delete(t.pending, msg.ID)
		}
		t.mu.Unlock()
		if !ok {
			t.unknownResponses.Add(1)
			return
		}
		ch <- outcome{msg: msg}
	case KindNotification:
		t.droppedNotifications.Add(1)
	case KindRequest:
		// Server-initiated requests are never answered in v1.
		t.skippedLines.Add(1)
	}
}

// onReadError handles a stream read failure. A partial line means the
// peer died mid-frame (truncated JSON): always a terminal failure. A
// clean EOF after shutdown is a quiet exit; EOF with a live transport
// means no future response can arrive, so it fails terminally even with
// nothing pending (fail-fast beats hanging the next request on a dead
// stream). Other errors fail terminally with a bounded excerpt.
func (t *Transport) onReadError(rerr error, partial bool) {
	if partial {
		t.failTerminal(fmt.Errorf("mcp: stream ended mid-message: %w", ErrTransport))
		return
	}
	if rerr == io.EOF {
		if t.isTerminal() {
			return
		}
		t.failTerminal(fmt.Errorf("mcp: stream ended unexpectedly: %w", ErrTransport))
		return
	}
	if t.isTerminal() {
		return
	}
	t.failTerminal(fmt.Errorf("mcp: stream read failed (%s): %w", quoteBounded(rerr.Error()), ErrTransport))
}
