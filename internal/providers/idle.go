package providers

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// defaultBodyIdleTimeout bounds how long a 200 response body may go
// without delivering data. It is an idle timeout, not a total timeout:
// a stream that keeps arriving (tokens, deltas, SSE keep-alive
// comments) runs indefinitely, no matter how long the whole response
// takes. Only a stalled body - headers received, then silence - fails.
//
// Rationale: ResponseHeaderTimeout bounds the header phase, and
// Client.Timeout stays 0 so legitimate long streams survive. Without an
// idle bound, a provider sending a byte per minute (or nothing at all
// on a half-dead connection) holds a model turn forever with no error
// for any retry, supervisor, or timeout machinery to observe.
var defaultBodyIdleTimeout = 60 * time.Second

// idleTimeoutError reports a response body that exceeded the idle
// budget. It implements net.Error with Timeout() true (and deliberately
// does NOT wrap context.DeadlineExceeded, so it is never confused with
// a caller-cancelled context): Classify reports ErrKindTimeout and
// IsTransient reports true, routing the failure through the existing
// turn-retry and ExitRetryable machinery.
type idleTimeoutError struct {
	idle time.Duration
}

func (e *idleTimeoutError) Error() string {
	return fmt.Sprintf("provider stream idle for %s without data (idle timeout)", e.idle)
}

// Timeout reports the timeout nature for net.Error classification.
func (e *idleTimeoutError) Timeout() bool { return true }

// Temporary reports the error as transient for net.Error consumers.
func (e *idleTimeoutError) Temporary() bool { return true }

// errIdleReaderClosed is returned for reads after Close. It is distinct
// from io.EOF (clean stream end) so a use-after-close can never be
// mistaken for a completed response.
var errIdleReaderClosed = fmt.Errorf("read from closed idle-timeout body")

// readResult is one pump delivery: a data chunk, a terminal pump error,
// or both (a final short chunk plus EOF).
type readResult struct {
	buf []byte
	err error
}

// idleTimeoutReader wraps a response body with an idle deadline that
// resets on every delivered byte. Exactly one pump goroutine reads the
// underlying body; Read multiplexes pump deliveries against a fresh per
// wait timer, so slow-but-active streams never trip it while a stalled
// one always does. Not safe for concurrent use (matching every
// consumer: each adapter reads its body from one goroutine).
type idleTimeoutReader struct {
	rc   io.ReadCloser
	idle time.Duration

	ch   chan readResult
	done chan struct{}

	mu      sync.Mutex
	pending []byte // stashed overflow from a chunk larger than Read's buffer
	eof     bool   // pump reported clean EOF; further reads repeat it
	failed  error  // pump reported a non-EOF error; further reads repeat it
	expired bool   // idle budget spent; further reads report idleTimeoutError
	closed  bool   // Close called; further reads report errIdleReaderClosed

	stopOnce sync.Once
}

// newIdleTimeoutReader starts the pump and returns the guarded body.
// A non-positive idle disables the bound (reads pass through to rc,
// still tracked for Close); production always passes a positive value.
func newIdleTimeoutReader(rc io.ReadCloser, idle time.Duration) *idleTimeoutReader {
	r := &idleTimeoutReader{
		rc:   rc,
		idle: idle,
		ch:   make(chan readResult),
		done: make(chan struct{}),
	}
	go r.pump()
	return r
}

func (r *idleTimeoutReader) pump() {
	tmp := make([]byte, 32*1024)
	for {
		n, err := r.rc.Read(tmp)
		var chunk []byte
		if n > 0 {
			chunk = append([]byte(nil), tmp[:n]...)
		}
		select {
		case r.ch <- readResult{buf: chunk, err: err}:
		case <-r.done:
			return
		}
		if err != nil {
			return
		}
	}
}

// stopPump releases the pump exactly once. Callers must also Close the
// underlying body when the pump may be blocked inside rc.Read: Close
// unblocks pending network reads, letting the pump reach the done check
// and exit instead of leaking.
func (r *idleTimeoutReader) stopPump() {
	r.stopOnce.Do(func() { close(r.done) })
}

// Read returns stashed bytes, fresh pump data, or a terminal condition.
// Data delivery resets the idle budget: the timer below is created fresh
// for each Read that must wait, and any bytes abort the wait. Only a wait
// that outlasts the full budget with zero bytes expires.
func (r *idleTimeoutReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	if r.expired {
		idle := r.idle
		r.mu.Unlock()
		return 0, &idleTimeoutError{idle: idle}
	}
	if r.closed {
		r.mu.Unlock()
		return 0, errIdleReaderClosed
	}
	if len(r.pending) > 0 {
		n := copy(p, r.pending)
		r.pending = append(r.pending[:0], r.pending[n:]...)
		r.mu.Unlock()
		return n, nil
	}
	if r.eof {
		r.mu.Unlock()
		return 0, io.EOF
	}
	if r.failed != nil {
		err := r.failed
		r.mu.Unlock()
		return 0, err
	}
	r.mu.Unlock()

	timer := time.NewTimer(r.idle)
	defer timer.Stop()
	for {
		select {
		case res := <-r.ch:
			if n, err, done := r.deliver(p, res); done {
				return n, err
			}
			// Empty chunk with no error: no progress and no failure,
			// so loop back into the SAME timer. The budget keeps
			// running; zero-byte ticks can never extend a stall.
		case <-timer.C:
			return r.expire()
		case <-r.done:
			r.mu.Lock()
			if r.expired {
				idle := r.idle
				r.mu.Unlock()
				return 0, &idleTimeoutError{idle: idle}
			}
			r.mu.Unlock()
			return 0, errIdleReaderClosed
		}
	}
}

// deliver folds one pump result into state. It reports done=true with
// the Read result, or done=false to keep waiting (empty chunk).
func (r *idleTimeoutReader) deliver(p []byte, res readResult) (int, error, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.expired {
		return 0, &idleTimeoutError{idle: r.idle}, true
	}
	if r.closed {
		return 0, errIdleReaderClosed, true
	}
	if len(res.buf) > 0 {
		n := copy(p, res.buf)
		if n < len(res.buf) {
			r.pending = append(r.pending, res.buf[n:]...)
		}
		// A terminal error riding with a final short chunk is recorded
		// for the reads after these bytes drain.
		if res.err == io.EOF {
			r.eof = true
		} else if res.err != nil {
			r.failed = res.err
		}
		return n, nil, true
	}
	if res.err == io.EOF {
		r.eof = true
		return 0, io.EOF, true
	}
	if res.err != nil {
		r.failed = res.err
		return 0, res.err, true
	}
	return 0, nil, false
}

// expire latches the timeout, releases the pump (closing the body
// unblocks its in-flight read), and reports the transient failure.
func (r *idleTimeoutReader) expire() (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expired = true
	r.stopPump()
	_ = r.rc.Close()
	return 0, &idleTimeoutError{idle: r.idle}
}

// Close stops the pump, releases the timer state, and forwards the
// close so the underlying connection is freed. Idempotent.
func (r *idleTimeoutReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	r.pending = nil
	r.stopPump()
	return r.rc.Close()
}
