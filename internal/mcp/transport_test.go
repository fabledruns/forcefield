package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

// Deterministic transport tests over in-memory streams: no subprocesses,
// no network, no sleeps on success paths. A 5s guard wraps any blocking
// harness read/write so a regression fails the suite instead of hanging
// it; the guarded paths always complete promptly when the code is right.

const harnessTimeout = 5 * time.Second

// pipeTransport wires a Transport to two io.Pipes: the test reads what the
// transport writes on toR and feeds the transport by writing fromW.
type pipeTransport struct {
	tr    *Transport
	toR   *bufio.Reader
	toW   *io.PipeWriter
	fromW *io.PipeWriter
}

func newPipeTransport(t *testing.T) *pipeTransport {
	t.Helper()
	toR, toW := io.Pipe()
	fromR, fromW := io.Pipe()
	tr, err := NewTransport(fromR, toW)
	if err != nil {
		t.Fatalf("NewTransport error = %v", err)
	}
	pt := &pipeTransport{tr: tr, toR: bufio.NewReader(toR), toW: toW, fromW: fromW}
	t.Cleanup(func() {
		toW.Close()
		fromW.Close()
		_ = tr.Close()
		select {
		case <-tr.Done():
		case <-time.After(harnessTimeout):
			t.Error("reader goroutine did not exit after stream close")
		}
	})
	return pt
}

// mustWriteLine feeds one raw line to the transport (newline appended).
func (p *pipeTransport) mustWriteLine(t *testing.T, s string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := p.fromW.Write([]byte(s + "\n"))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("write to transport: %v", err)
		}
	case <-time.After(harnessTimeout):
		t.Fatal("timed out writing to transport")
	}
}

// nextClientRequest reads one request frame the transport wrote and
// returns its parsed form plus the raw id bytes for echoing back.
func (p *pipeTransport) nextClientRequest(t *testing.T) (Message, json.RawMessage) {
	t.Helper()
	line := readLineGuard(t, p.toR)
	msg, err := ParseMessage(line)
	if err != nil {
		t.Fatalf("ParseMessage(client frame) error = %v", err)
	}
	if msg.Kind != KindRequest {
		t.Fatalf("client frame kind = %v, want request", msg.Kind)
	}
	var env struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(line, &env); err != nil {
		t.Fatalf("client frame id: %v", err)
	}
	return msg, env.ID
}

// respondResult answers rawID with a result payload.
func (p *pipeTransport) respondResult(t *testing.T, rawID json.RawMessage, result string) {
	t.Helper()
	p.mustWriteLine(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, string(rawID), result))
}

// readLineGuard reads one line with a hang guard (suite backstop only).
func readLineGuard(t *testing.T, r *bufio.Reader) []byte {
	t.Helper()
	type res struct {
		line []byte
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		line, err := r.ReadBytes('\n')
		ch <- res{line, err}
	}()
	select {
	case out := <-ch:
		if out.err != nil {
			t.Fatalf("read line: %v", out.err)
		}
		return out.line
	case <-time.After(harnessTimeout):
		t.Fatal("timed out waiting for line")
		return nil
	}
}

// roundTrip performs one full request/response exchange driven manually:
// it reads the transport's request, echoes wantID-safe result, and
// returns the Request outcome. In-order delivery makes this a
// deterministic flush: everything the test wrote earlier was consumed.
func roundTrip(t *testing.T, ctx context.Context, pt *pipeTransport, method string) (Message, error) {
	t.Helper()
	type outcome2 struct {
		msg Message
		err error
	}
	ch := make(chan outcome2, 1)
	go func() {
		msg, err := pt.tr.Request(ctx, method, nil)
		ch <- outcome2{msg, err}
	}()
	_, rawID := pt.nextClientRequest(t)
	pt.respondResult(t, rawID, `{"ok":true}`)
	select {
	case out := <-ch:
		return out.msg, out.err
	case <-time.After(harnessTimeout):
		t.Fatal("round trip timed out")
		return Message{}, nil
	}
}

func TestTransportConstructorRejectsNil(t *testing.T) {
	if _, err := NewTransport(nil, io.Discard); err == nil {
		t.Error("nil reader accepted")
	}
	if _, err := NewTransport(strings.NewReader(""), nil); err == nil {
		t.Error("nil writer accepted")
	}
}

func TestTransportValidResponseCorrelation(t *testing.T) {
	pt := newPipeTransport(t)
	ctx := context.Background()
	type res struct {
		msg Message
		err error
	}
	ch := make(chan res, 1)
	go func() {
		msg, err := pt.tr.Request(ctx, "tools/list", map[string]any{"cursor": "c"})
		ch <- res{msg, err}
	}()
	_, rawID := pt.nextClientRequest(t)
	pt.respondResult(t, rawID, `{"tools":[]}`)
	select {
	case out := <-ch:
		if out.err != nil {
			t.Fatalf("Request error = %v", out.err)
		}
		if out.msg.Kind != KindResponse || len(out.msg.Result) == 0 {
			t.Errorf("outcome = %+v, want result response", out.msg)
		}
	case <-time.After(harnessTimeout):
		t.Fatal("request timed out")
	}
	if pt.tr.Pending() != 0 {
		t.Errorf("Pending = %d, want 0", pt.tr.Pending())
	}
	if !pt.tr.Healthy() {
		t.Error("transport unhealthy after successful exchange")
	}
}

func TestTransportErrorResponseSurfacesRPCError(t *testing.T) {
	pt := newPipeTransport(t)
	type res struct {
		err error
	}
	ch := make(chan res, 1)
	go func() {
		_, err := pt.tr.Request(context.Background(), "tools/call", nil)
		ch <- res{err}
	}()
	_, rawID := pt.nextClientRequest(t)
	pt.mustWriteLine(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"Method not found"}}`, string(rawID)))
	select {
	case out := <-ch:
		var rpcErr *RPCError
		if !errors.As(out.err, &rpcErr) {
			t.Fatalf("err = %v, want *RPCError", out.err)
		}
		if rpcErr.Code != -32601 {
			t.Errorf("code = %d, want -32601", rpcErr.Code)
		}
	case <-time.After(harnessTimeout):
		t.Fatal("request timed out")
	}
}

func TestTransportErrorResponseMessageBounded(t *testing.T) {
	pt := newPipeTransport(t)
	ch := make(chan error, 1)
	go func() {
		_, err := pt.tr.Request(context.Background(), "m", nil)
		ch <- err
	}()
	_, rawID := pt.nextClientRequest(t)
	huge := strings.Repeat("e", MaxErrorDetailRunes*4)
	pt.mustWriteLine(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":%s}}`, string(rawID), quoteJSON(huge)))
	select {
	case err := <-ch:
		if err == nil {
			t.Fatal("nil error, want RPCError")
		}
		if len([]rune(err.Error())) > MaxErrorDetailRunes+128 {
			t.Errorf("error of %d runes is unbounded", len([]rune(err.Error())))
		}
	case <-time.After(harnessTimeout):
		t.Fatal("request timed out")
	}
}

func quoteJSON(s string) string {
	enc, _ := json.Marshal(s)
	return string(enc)
}

func TestTransportNotificationIgnored(t *testing.T) {
	pt := newPipeTransport(t)
	pt.mustWriteLine(t, `{"jsonrpc":"2.0","method":"notifications/progress","params":{}}`)
	pt.mustWriteLine(t, `{"jsonrpc":"2.0","method":"unknown/anything","params":{"x":1}}`)
	if _, err := roundTrip(t, context.Background(), pt, "tools/list"); err != nil {
		t.Fatalf("round trip after notifications: %v", err)
	}
	if got := pt.tr.Stats().DroppedNotifications; got != 2 {
		t.Errorf("dropped notifications = %d, want 2", got)
	}
	if !pt.tr.Healthy() {
		t.Error("transport unhealthy after notifications")
	}
}

func TestTransportServerRequestDropped(t *testing.T) {
	pt := newPipeTransport(t)
	pt.mustWriteLine(t, `{"jsonrpc":"2.0","id":99,"method":"sampling/createMessage","params":{}}`)
	if _, err := roundTrip(t, context.Background(), pt, "tools/list"); err != nil {
		t.Fatalf("round trip after server request: %v", err)
	}
	if got := pt.tr.Stats().SkippedLines; got != 1 {
		t.Errorf("skipped = %d, want 1", got)
	}
}

func TestTransportMalformedLineSkipped(t *testing.T) {
	pt := newPipeTransport(t)
	pt.mustWriteLine(t, `this is not json`)
	pt.mustWriteLine(t, `{"jsonrpc":"2.0","id":1,`)
	if _, err := roundTrip(t, context.Background(), pt, "tools/list"); err != nil {
		t.Fatalf("round trip after malformed lines: %v", err)
	}
	if got := pt.tr.Stats().SkippedLines; got != 2 {
		t.Errorf("skipped = %d, want 2", got)
	}
}

func TestTransportBatchRejectedAsLine(t *testing.T) {
	pt := newPipeTransport(t)
	pt.mustWriteLine(t, `[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`)
	if _, err := roundTrip(t, context.Background(), pt, "tools/list"); err != nil {
		t.Fatalf("round trip after batch: %v", err)
	}
	if got := pt.tr.Stats().SkippedLines; got != 1 {
		t.Errorf("skipped = %d, want 1", got)
	}
}

func TestTransportUnknownResponseDropped(t *testing.T) {
	pt := newPipeTransport(t)
	pt.mustWriteLine(t, `{"jsonrpc":"2.0","id":4242,"result":{}}`)
	if _, err := roundTrip(t, context.Background(), pt, "tools/list"); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if got := pt.tr.Stats().UnknownResponses; got != 1 {
		t.Errorf("unknown = %d, want 1", got)
	}
}

func TestTransportDuplicateResponseSecondIsUnknown(t *testing.T) {
	pt := newPipeTransport(t)
	ch := make(chan error, 1)
	go func() {
		_, err := pt.tr.Request(context.Background(), "tools/list", nil)
		ch <- err
	}()
	_, rawID := pt.nextClientRequest(t)
	line := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}`, string(rawID))
	pt.mustWriteLine(t, line)
	select {
	case err := <-ch:
		if err != nil {
			t.Fatalf("first response err = %v", err)
		}
	case <-time.After(harnessTimeout):
		t.Fatal("request timed out")
	}
	pt.mustWriteLine(t, line) // duplicate: no pending entry remains
	if _, err := roundTrip(t, context.Background(), pt, "tools/list"); err != nil {
		t.Fatalf("flush round trip: %v", err)
	}
	if got := pt.tr.Stats().UnknownResponses; got != 1 {
		t.Errorf("unknown = %d, want 1", got)
	}
}

func TestTransportEquivalentNumericIDs(t *testing.T) {
	pt := newPipeTransport(t)
	// First request (id 1) answered with the 1.0 echo form.
	ch1 := make(chan error, 1)
	go func() {
		_, err := pt.tr.Request(context.Background(), "a", nil)
		ch1 <- err
	}()
	_, raw1 := pt.nextClientRequest(t)
	if string(raw1) != "1" {
		t.Fatalf("first id = %s, want 1", string(raw1))
	}
	pt.mustWriteLine(t, `{"jsonrpc":"2.0","id":1.0,"result":{}}`)
	select {
	case err := <-ch1:
		if err != nil {
			t.Fatalf("1.0 echo err = %v, want correlation success", err)
		}
	case <-time.After(harnessTimeout):
		t.Fatal("1.0 echo timed out")
	}
	// Second request (id 2) answered with the 2e0 echo form.
	ch2 := make(chan error, 1)
	go func() {
		_, err := pt.tr.Request(context.Background(), "b", nil)
		ch2 <- err
	}()
	_, raw2 := pt.nextClientRequest(t)
	if string(raw2) != "2" {
		t.Fatalf("second id = %s, want 2", string(raw2))
	}
	pt.mustWriteLine(t, `{"jsonrpc":"2.0","id":2e0,"result":{}}`)
	select {
	case err := <-ch2:
		if err != nil {
			t.Fatalf("2e0 echo err = %v, want correlation success", err)
		}
	case <-time.After(harnessTimeout):
		t.Fatal("2e0 echo timed out")
	}
}

func TestTransportMonotonicIDs(t *testing.T) {
	pt := newPipeTransport(t)
	var ids []string
	for i := 0; i < 3; i++ {
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = pt.tr.Request(context.Background(), "m", nil)
		}()
		_, raw := pt.nextClientRequest(t)
		ids = append(ids, string(raw))
		pt.respondResult(t, raw, `{}`)
		<-done
	}
	for i, want := range []string{"1", "2", "3"} {
		if ids[i] != want {
			t.Errorf("id[%d] = %s, want %s", i, ids[i], want)
		}
	}
}

func TestTransportConcurrentCorrelation(t *testing.T) {
	pt := newPipeTransport(t)
	// Echo pump: answer every request with its own raw id.
	var pumpWG sync.WaitGroup
	pumpWG.Add(1)
	go func() {
		defer pumpWG.Done()
		for {
			line, err := pt.toR.ReadBytes('\n')
			if err != nil {
				return
			}
			var env struct {
				ID json.RawMessage `json:"id"`
			}
			if err := json.Unmarshal(line, &env); err != nil || len(env.ID) == 0 {
				continue
			}
			resp := fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"echo\":%s}}\n", string(env.ID), string(env.ID))
			if _, err := pt.fromW.Write([]byte(resp)); err != nil {
				return
			}
		}
	}()
	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	msgs := make([]Message, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			msg, err := pt.tr.Request(context.Background(), "tools/call", map[string]any{"n": i})
			msgs[i], errs[i] = msg, err
		}(i)
	}
	wg.Wait()
	_ = pt.toW.Close() // stop the pump
	pumpWG.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("request %d err = %v", i, errs[i])
			continue
		}
		if msgs[i].Kind != KindResponse || len(msgs[i].Result) == 0 {
			t.Errorf("request %d outcome = %+v, want result", i, msgs[i])
		}
	}
	if pt.tr.Pending() != 0 {
		t.Errorf("Pending = %d, want 0", pt.tr.Pending())
	}
}

func TestTransportIDOverflowFailsSafely(t *testing.T) {
	pt := newPipeTransport(t)
	pt.tr.nextID.Store(math.MaxInt64 - 1)
	ch := make(chan error, 1)
	go func() {
		_, err := pt.tr.Request(context.Background(), "m", nil)
		ch <- err
	}()
	_, raw := pt.nextClientRequest(t) // drains the write; id is MaxInt64
	if string(raw) != "9223372036854775807" {
		t.Fatalf("id = %s, want MaxInt64", string(raw))
	}
	pt.respondResult(t, raw, `{}`)
	select {
	case err := <-ch:
		if err != nil {
			t.Fatalf("id at MaxInt64 err = %v, want nil", err)
		}
	case <-time.After(harnessTimeout):
		t.Fatal("request timed out")
	}
	// The counter wrapped past MaxInt64: allocation fails safely instead
	// of recycling into a live pending key.
	if _, err := pt.tr.allocID(); !errors.Is(err, ErrProtocol) {
		t.Errorf("exhausted alloc err = %v, want ErrProtocol", err)
	}
	if _, err := pt.tr.Request(context.Background(), "m", nil); !errors.Is(err, ErrProtocol) {
		t.Errorf("exhausted request err = %v, want ErrProtocol without I/O", err)
	}
	if pt.tr.Pending() != 0 {
		t.Errorf("Pending = %d, want 0", pt.tr.Pending())
	}
	if !pt.tr.Healthy() {
		t.Error("transport unhealthy after safe exhaustion")
	}
}

// heldReader returns a pipe reader that stays open (blocking) until the
// test closes the writer, so transports under test never see a surprise
// EOF. Cleanup closes the writer; callers wait on Done explicitly where
// reader termination is asserted.
func heldReader(t *testing.T) (*io.PipeReader, *io.PipeWriter) {
	t.Helper()
	r, w := io.Pipe()
	t.Cleanup(func() {
		_ = w.Close()
	})
	return r, w
}

// waitDone waits for reader termination with a hang guard.
func waitDone(t *testing.T, tr *Transport) {
	t.Helper()
	select {
	case <-tr.Done():
	case <-time.After(harnessTimeout):
		t.Fatal("reader goroutine did not exit")
	}
}

func TestTransportPreCanceledContext(t *testing.T) {
	r, w := heldReader(t)
	defer func() { _ = w.Close() }()
	wc := &countWriter{}
	tr, err := NewTransport(r, wc)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	defer func() {
		_ = tr.Close()
		_ = w.Close()
		waitDone(t, tr)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tr.Request(ctx, "m", nil); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if wc.bytes != 0 {
		t.Errorf("wrote %d bytes for canceled request, want 0", wc.bytes)
	}
	if tr.Pending() != 0 {
		t.Errorf("Pending = %d, want 0", tr.Pending())
	}
}

func TestTransportCancellationKeepsUsable(t *testing.T) {
	pt := newPipeTransport(t)
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() {
		_, err := pt.tr.Request(ctx, "m", nil)
		ch <- err
	}()
	_, rawID := pt.nextClientRequest(t) // request is in flight
	cancel()
	select {
	case err := <-ch:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(harnessTimeout):
		t.Fatal("cancel did not settle request")
	}
	if pt.tr.Pending() != 0 {
		t.Fatalf("Pending = %d, want 0", pt.tr.Pending())
	}
	// Late response for the canceled request is dropped, transport lives.
	pt.respondResult(t, rawID, `{}`)
	if _, err := roundTrip(t, context.Background(), pt, "m"); err != nil {
		t.Fatalf("transport poisoned after cancel: %v", err)
	}
	if got := pt.tr.Stats().UnknownResponses; got != 1 {
		t.Errorf("unknown = %d, want 1 late response dropped", got)
	}
}

func TestTransportDeadline(t *testing.T) {
	pt := newPipeTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	ch := make(chan error, 1)
	go func() {
		_, err := pt.tr.Request(ctx, "m", nil) // no response is ever sent
		ch <- err
	}()
	// Drain the request write so the call reaches its wait state; the
	// deadline, not a blocked write, must settle it.
	_, rawID := pt.nextClientRequest(t)
	select {
	case err := <-ch:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want DeadlineExceeded", err)
		}
	case <-time.After(harnessTimeout):
		t.Fatal("deadline did not settle request")
	}
	if time.Since(start) > harnessTimeout {
		t.Error("deadline took longer than the harness guard")
	}
	pt.respondResult(t, rawID, `{"late":true}`) // late arrival drops cleanly
	if _, err := roundTrip(t, context.Background(), pt, "m"); err != nil {
		t.Fatalf("transport poisoned after deadline: %v", err)
	}
}

func TestTransportCancelRaceBothOutcomesLegal(t *testing.T) {
	for i := 0; i < 50; i++ {
		pt := newPipeTransport(t)
		ctx, cancel := context.WithCancel(context.Background())
		ch := make(chan error, 1)
		var gotMsg Message
		go func() {
			msg, err := pt.tr.Request(ctx, "m", map[string]any{"i": i})
			gotMsg = msg
			ch <- err
		}()
		_, rawID := pt.nextClientRequest(t) // in flight, deterministic
		if i%2 == 0 {
			cancel()
			pt.respondResult(t, rawID, `{"won":"cancel-path"}`)
		} else {
			pt.respondResult(t, rawID, `{"won":"response-path"}`)
			cancel()
		}
		var err error
		select {
		case err = <-ch:
		case <-time.After(harnessTimeout):
			t.Fatalf("iter %d: request never settled", i)
		}
		if err == nil {
			if len(gotMsg.Result) == 0 {
				t.Errorf("iter %d: nil error with empty result", i)
			}
		} else if !errors.Is(err, context.Canceled) {
			t.Errorf("iter %d: err = %v, want nil or Canceled", i, err)
		}
		if pt.tr.Pending() != 0 {
			t.Fatalf("iter %d: Pending = %d, want 0", i, pt.tr.Pending())
		}
		if !pt.tr.Healthy() {
			t.Fatalf("iter %d: transport unhealthy after race", i)
		}
		_ = pt.tr.Close()
	}
}

func TestTransportOversizedLineIsTerminal(t *testing.T) {
	pt := newPipeTransport(t)
	ch := make(chan error, 1)
	go func() {
		_, err := pt.tr.Request(context.Background(), "m", nil)
		ch <- err
	}()
	_, _ = pt.nextClientRequest(t)
	// A single line past the budget (valid JSON shape, oversized body).
	// The write runs detached: once the reader detects the over-cap
	// prefix it goes terminal without consuming the tail, so joining the
	// write would block. Cleanup closes the pipe, releasing it.
	padding := strings.Repeat(" ", MaxMessageBytes)
	oversize := `{"jsonrpc":"2.0","id":1,"method":"x","params":"` + padding + `"}` + "\n"
	go func() {
		_, _ = pt.fromW.Write([]byte(oversize))
	}()
	select {
	case err := <-ch:
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("err = %v, want ErrTooLarge", err)
		}
	case <-time.After(harnessTimeout):
		t.Fatal("oversize did not terminate pending request")
	}
	if pt.tr.Healthy() {
		t.Error("transport healthy after oversize frame")
	}
	if _, err := pt.tr.Request(context.Background(), "m", nil); !errors.Is(err, ErrTooLarge) {
		t.Errorf("post-failure request err = %v, want fail-fast ErrTooLarge", err)
	}
	if pt.tr.Pending() != 0 {
		t.Errorf("Pending = %d, want 0", pt.tr.Pending())
	}
}

func TestTransportTruncatedAtEOF(t *testing.T) {
	pt := newPipeTransport(t)
	ch := make(chan error, 1)
	go func() {
		_, err := pt.tr.Request(context.Background(), "m", nil)
		ch <- err
	}()
	_, _ = pt.nextClientRequest(t)
	done := make(chan error, 1)
	go func() {
		_, err := pt.fromW.Write([]byte(`{"jsonrpc":"2.0","id":1,"res`))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("partial write: %v", err)
		}
	case <-time.After(harnessTimeout):
		t.Fatal("partial write blocked")
	}
	_ = pt.fromW.Close() // EOF with a partial line: truncated, terminal
	select {
	case err := <-ch:
		if !errors.Is(err, ErrTransport) {
			t.Fatalf("err = %v, want ErrTransport", err)
		}
	case <-time.After(harnessTimeout):
		t.Fatal("truncation did not fail pending request")
	}
}

func TestTransportEOFWithNothingPendingFailsFast(t *testing.T) {
	pt := newPipeTransport(t)
	_ = pt.fromW.Close()
	select {
	case <-pt.tr.Done():
	case <-time.After(harnessTimeout):
		t.Fatal("reader did not exit on EOF")
	}
	if pt.tr.Healthy() {
		t.Error("transport healthy after peer EOF; a dead stream must fail fast, not hang future calls")
	}
	if _, err := pt.tr.Request(context.Background(), "m", nil); !errors.Is(err, ErrTransport) {
		t.Errorf("post-EOF request err = %v, want ErrTransport", err)
	}
}

func TestTransportReaderErrorFailsPending(t *testing.T) {
	toR, toW := io.Pipe()
	fromR, fromW := io.Pipe()
	tr, err := NewTransport(fromR, toW)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	t.Cleanup(func() {
		toW.Close()
		fromW.Close()
		_ = tr.Close()
	})
	const n = 2
	ch := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := tr.Request(context.Background(), "m", nil)
			ch <- err
		}()
	}
	// Drain both request lines so both calls are provably pending.
	drain := bufio.NewReader(toR)
	for i := 0; i < n; i++ {
		readLineGuard(t, drain)
	}
	boom := errors.New(strings.Repeat("boom-", 200))
	_ = fromW.CloseWithError(boom)
	for i := 0; i < n; i++ {
		select {
		case err := <-ch:
			if !errors.Is(err, ErrTransport) {
				t.Errorf("pending %d err = %v, want ErrTransport", i, err)
			}
			if len([]rune(err.Error())) > MaxErrorDetailRunes+256 {
				t.Errorf("failure detail of %d runes is unbounded", len([]rune(err.Error())))
			}
		case <-time.After(harnessTimeout):
			t.Fatalf("pending %d never failed", i)
		}
	}
	if tr.Pending() != 0 {
		t.Errorf("Pending = %d, want 0", tr.Pending())
	}
	if _, err := tr.Request(context.Background(), "m", nil); !errors.Is(err, ErrTransport) {
		t.Errorf("post-failure err = %v, want fail-fast", err)
	}
}

func TestTransportShutdownNoPending(t *testing.T) {
	pt := newPipeTransport(t)
	if err := pt.tr.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	if err := pt.tr.Close(); err != nil {
		t.Fatalf("second Close error = %v, want idempotent nil", err)
	}
	if _, err := pt.tr.Request(context.Background(), "m", nil); !errors.Is(err, ErrClosed) {
		t.Errorf("post-close request err = %v, want ErrClosed", err)
	}
	if err := pt.tr.Notify("notifications/initialized", nil); !errors.Is(err, ErrClosed) {
		t.Errorf("post-close notify err = %v, want ErrClosed", err)
	}
	if pt.tr.Healthy() {
		t.Error("closed transport reports healthy")
	}
}

func TestTransportShutdownFailsPending(t *testing.T) {
	pt := newPipeTransport(t)
	ch := make(chan error, 1)
	go func() {
		_, err := pt.tr.Request(context.Background(), "m", nil)
		ch <- err
	}()
	_, _ = pt.nextClientRequest(t)
	if err := pt.tr.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	select {
	case err := <-ch:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("pending err = %v, want ErrClosed", err)
		}
	case <-time.After(harnessTimeout):
		t.Fatal("shutdown did not fail pending request")
	}
	if pt.tr.Pending() != 0 {
		t.Errorf("Pending = %d, want 0", pt.tr.Pending())
	}
}

func TestTransportShutdownRace(t *testing.T) {
	pt := newPipeTransport(t)
	// Drain what the transport writes so racing requests reach their wait
	// state instead of blocking in writes; the drainer exits when cleanup
	// closes the pipe.
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for {
			if _, err := pt.toR.ReadBytes('\n'); err != nil {
				return
			}
		}
	}()
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = pt.tr.Request(context.Background(), "m", nil)
		}(i)
	}
	// Let requests register (each blocks writing into the undrained pipe
	// or waiting); then shut down while notifies race alongside.
	var nwg sync.WaitGroup
	for i := 0; i < 4; i++ {
		nwg.Add(1)
		go func() {
			defer nwg.Done()
			_ = pt.tr.Notify("notifications/initialized", nil)
		}()
	}
	_ = pt.tr.Close()
	nwg.Wait()
	wg.Wait()
	// The drainer exits when cleanup closes the pipe; requests must all
	// have settled exactly once regardless of the interleaving.
	for i, err := range errs {
		if err == nil {
			t.Errorf("request %d settled nil after shutdown race", i)
		}
	}
	if pt.tr.Pending() != 0 {
		t.Errorf("Pending = %d, want 0", pt.tr.Pending())
	}
}

func TestTransportNotifyRoundTrip(t *testing.T) {
	pt := newPipeTransport(t)
	// Notify blocks until the line is consumed (synchronous pipe), so it
	// runs concurrently with the read, like every other transport write.
	nch := make(chan error, 1)
	go func() {
		nch <- pt.tr.Notify(MethodInitialized, map[string]any{})
	}()
	line := readLineGuard(t, pt.toR)
	msg, err := ParseMessage(line)
	if err != nil {
		t.Fatalf("ParseMessage(notify) error = %v", err)
	}
	if msg.Kind != KindNotification || msg.Method != MethodInitialized {
		t.Errorf("got kind %v method %q, want notification %q", msg.Kind, msg.Method, MethodInitialized)
	}
	select {
	case err := <-nch:
		if err != nil {
			t.Errorf("Notify error = %v, want nil", err)
		}
	case <-time.After(harnessTimeout):
		t.Fatal("Notify did not complete")
	}
}

func TestTransportWriteAfterShutdownWritesNothing(t *testing.T) {
	r, w := heldReader(t)
	wc := &countWriter{}
	tr, err := NewTransport(r, wc)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	_ = tr.Close()
	before := wc.bytes
	if err := tr.Notify("m", nil); !errors.Is(err, ErrClosed) {
		t.Errorf("notify err = %v, want ErrClosed", err)
	}
	if wc.bytes != before {
		t.Errorf("wrote %d bytes after shutdown, want 0", wc.bytes-before)
	}
	_ = w.Close()
	waitDone(t, tr) // owner closed the stream: reader terminates
}

func TestTransportPartialWriter(t *testing.T) {
	toR, toW := io.Pipe()
	fromR, fromW := io.Pipe()
	tr, err := NewTransport(fromR, &byteWriter{w: toW})
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	t.Cleanup(func() {
		toW.Close()
		fromW.Close()
		_ = tr.Close()
	})
	ch := make(chan error, 1)
	go func() {
		_, err := tr.Request(context.Background(), "tools/list", nil)
		ch <- err
	}()
	// The frame arrives one byte per Write; the line reader assembles it.
	line := readLineGuard(t, bufio.NewReader(toR))
	msg, err := ParseMessage(line)
	if err != nil || msg.Kind != KindRequest {
		t.Fatalf("assembled frame kind = %v err = %v", msg.Kind, err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := fromW.Write([]byte(fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{}}\n", rawIDOf(t, line))))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("respond: %v", err)
		}
	case <-time.After(harnessTimeout):
		t.Fatal("respond blocked")
	}
	select {
	case err := <-ch:
		if err != nil {
			t.Fatalf("request over partial writer err = %v", err)
		}
	case <-time.After(harnessTimeout):
		t.Fatal("request timed out")
	}
}

func rawIDOf(t *testing.T, line []byte) string {
	t.Helper()
	var env struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(line, &env); err != nil {
		t.Fatalf("id: %v", err)
	}
	return string(env.ID)
}

func TestTransportWriterErrorIsTerminal(t *testing.T) {
	r, w := heldReader(t)
	defer func() { _ = w.Close() }()
	tr, err := NewTransport(r, errWriter{})
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	t.Cleanup(func() {
		_ = tr.Close()
		_ = w.Close()
		waitDone(t, tr)
	})
	if _, err := tr.Request(context.Background(), "m", nil); !errors.Is(err, ErrTransport) {
		t.Fatalf("err = %v, want ErrTransport", err)
	}
	if tr.Healthy() {
		t.Error("transport healthy after write failure")
	}
	if tr.Pending() != 0 {
		t.Errorf("Pending = %d, want 0", tr.Pending())
	}
	if _, err := tr.Request(context.Background(), "m", nil); !errors.Is(err, ErrTransport) {
		t.Errorf("second err = %v, want fail-fast", err)
	}
}

func TestTransportOversizedOutboundRejectedBeforeWrite(t *testing.T) {
	r, w := heldReader(t)
	defer func() { _ = w.Close() }()
	wc := &countWriter{}
	tr, err := NewTransport(r, wc)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	defer func() {
		_ = tr.Close()
		_ = w.Close()
		waitDone(t, tr)
	}()
	huge := strings.Repeat("p", MaxMessageBytes)
	if _, err := tr.Request(context.Background(), "m", map[string]any{"blob": huge}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if wc.bytes != 0 {
		t.Errorf("wrote %d bytes of oversized frame, want 0", wc.bytes)
	}
	if tr.Pending() != 0 || !tr.Healthy() {
		t.Error("oversized outbound changed transport state")
	}
}

func TestTransportDoneClosesAfterEOF(t *testing.T) {
	pt := newPipeTransport(t)
	_ = pt.fromW.Close()
	select {
	case <-pt.tr.Done():
	case <-time.After(harnessTimeout):
		t.Fatal("reader did not terminate after EOF")
	}
}

// countWriter records written bytes; it never blocks.
type countWriter struct {
	mu    sync.Mutex
	bytes int
}

func (w *countWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.bytes += len(b)
	return len(b), nil
}

// byteWriter emits one byte per Write call, exercising short-write loops.
type byteWriter struct {
	w io.Writer
}

func (w *byteWriter) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	return w.w.Write(b[:1])
}

// errWriter fails every write.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) {
	return 0, errors.New("injected writer failure")
}
