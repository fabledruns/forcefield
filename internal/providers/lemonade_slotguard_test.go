package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type slotGuardTestServer struct {
	server *httptest.Server

	mu                 sync.Mutex
	models             map[string]*mockLemonadeModel
	maxLLM             int
	healthStatus       int
	healthDelay        time.Duration
	slotsStatus        int
	slotsCount         int
	slotsDelay         time.Duration
	chatHoldCh         chan struct{}
	chatDelay          time.Duration
	markBusyDuringChat bool
	failChatStatus     int

	healthHits      int
	slotsHits       int
	chatHits        int
	loadUnloadHits  int
	inFlightChats   int
	maxInFlightChat int
	chatStartedCh   chan string
	chatFinishedCh  chan string
}

func newSlotGuardTestServer() *slotGuardTestServer {
	s := &slotGuardTestServer{
		models: map[string]*mockLemonadeModel{
			"ModelA": {
				ID:               "ModelA",
				Recipe:           "llamacpp",
				Downloaded:       true,
				MaxContextWindow: 32768,
				ResolvedCtxSize:  8192,
				Loaded:           true,
				Device:           "gpu",
				ParallelSlots:    1,
			},
			"ModelB": {
				ID:               "ModelB",
				Recipe:           "llamacpp",
				Downloaded:       true,
				MaxContextWindow: 32768,
				ResolvedCtxSize:  8192,
				Loaded:           false,
				Device:           "gpu",
				ParallelSlots:    1,
			},
		},
		maxLLM:         1,
		healthStatus:   http.StatusOK,
		slotsStatus:    http.StatusOK,
		slotsCount:     0, // 0 => match loaded model's ParallelSlots
		chatStartedCh:  make(chan string, 32),
		chatFinishedCh: make(chan string, 32),
	}
	s.server = httptest.NewServer(http.HandlerFunc(s.serveHTTP))
	return s
}

func (s *slotGuardTestServer) close() {
	s.server.Close()
}

func (s *slotGuardTestServer) url() string {
	return s.server.URL + "/api/v1"
}

func (s *slotGuardTestServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	subpath, ok := stripQuadPrefix(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch {
	case subpath == "/load" || subpath == "/unload":
		s.mu.Lock()
		s.loadUnloadHits++
		s.mu.Unlock()
		http.Error(w, "mutation forbidden", http.StatusMethodNotAllowed)
	case subpath == "/health":
		s.handleHealth(w, r)
	case subpath == "/slots":
		s.handleSlots(w, r)
	case subpath == "/chat/completions":
		s.handleChat(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *slotGuardTestServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.healthHits++
	status := s.healthStatus
	delay := s.healthDelay
	maxLLM := s.maxLLM
	loaded := make([]map[string]any, 0, len(s.models))
	pinnedCount := 0
	for _, mdl := range s.models {
		if !mdl.Loaded {
			continue
		}
		if mdl.Pinned {
			pinnedCount++
		}
		np := mdl.ParallelSlots
		if np <= 0 {
			np = 1
		}
		loaded = append(loaded, map[string]any{
			"model_name":     mdl.ID,
			"recipe":         mdl.Recipe,
			"status":         "ready",
			"is_busy":        mdl.IsBusy,
			"is_streaming":   mdl.IsBusy,
			"pinned":         mdl.Pinned,
			"launch_command": []string{"llama-server", "-np", fmt.Sprintf("%d", np)},
		})
	}
	s.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"error":"health error %d"}`, status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":            "ok",
		"all_models_loaded": loaded,
		"pinned_models":     map[string]int{"llm": pinnedCount},
		"max_models":        map[string]int{"llm": maxLLM},
	})
}

func (s *slotGuardTestServer) handleSlots(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.slotsHits++
	status := s.slotsStatus
	delay := s.slotsDelay
	count := s.slotsCount
	if count == 0 {
		for _, mdl := range s.models {
			if mdl.Loaded {
				count = mdl.ParallelSlots
				if count <= 0 {
					count = 1
				}
				break
			}
		}
	}
	s.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"error":"slots unavailable"}`)
		return
	}
	slots := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		slots = append(slots, map[string]any{"id": i, "is_processing": false})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(slots)
}

func (s *slotGuardTestServer) handleChat(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var reqMap map[string]any
	_ = json.Unmarshal(raw, &reqMap)
	modelName, _ := reqMap["model"].(string)

	s.mu.Lock()
	s.chatHits++
	s.inFlightChats++
	if s.inFlightChats > s.maxInFlightChat {
		s.maxInFlightChat = s.inFlightChats
	}
	holdCh := s.chatHoldCh
	delay := s.chatDelay
	markBusy := s.markBusyDuringChat
	failStatus := s.failChatStatus
	if markBusy {
		if mdl, ok := s.models[modelName]; ok {
			mdl.Loaded = true
			mdl.IsBusy = true
		}
	}
	s.mu.Unlock()

	select {
	case s.chatStartedCh <- modelName:
	default:
	}

	defer func() {
		s.mu.Lock()
		s.inFlightChats--
		if markBusy {
			if mdl, ok := s.models[modelName]; ok {
				mdl.IsBusy = false
			}
		}
		s.mu.Unlock()
		select {
		case s.chatFinishedCh <- modelName:
		default:
		}
	}()

	if failStatus != 0 {
		w.WriteHeader(failStatus)
		fmt.Fprintf(w, `{"error":"forced failure %d"}`, failStatus)
		return
	}

	stream, _ := reqMap["stream"].(bool)
	if !stream {
		if holdCh != nil {
			select {
			case <-holdCh:
			case <-r.Context().Done():
				return
			}
		}
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi \"}}]}\n\n")
	if flusher != nil {
		flusher.Flush()
	}

	if holdCh != nil {
		select {
		case <-holdCh:
		case <-r.Context().Done():
			return
		}
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}

	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n")
	fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func TestSlotGuard_SharedAcrossCloneForRunAndSerializesNP1(t *testing.T) {
	srv := newSlotGuardTestServer()
	defer srv.close()
	srv.chatDelay = 25 * time.Millisecond

	root := NewLemonade(Spec{ID: "lemonade", Type: "lemonade", BaseURL: srv.url(), Model: "ModelA"})
	const workers = 4
	clones := make([]*LemonadeProvider, workers)
	for i := 0; i < workers; i++ {
		c, err := root.CloneForRun()
		if err != nil {
			t.Fatalf("CloneForRun(%d): %v", i, err)
		}
		lp := c.(*LemonadeProvider)
		if lp.slotGuard != root.slotGuard {
			t.Fatalf("clone %d slotGuard pointer mismatch: got %p want %p", i, lp.slotGuard, root.slotGuard)
		}
		if lp.gate == root.gate {
			t.Fatalf("clone %d must keep isolated per-clone requestGate", i)
		}
		// Mutate clone reasoning independently.
		enabled := i%2 == 0
		lp.SetReasoning(ReasoningConfig{Thinking: &ThinkingConfig{Enabled: &enabled}})
		clones[i] = lp
	}
	if root.GetReasoning().Thinking != nil {
		t.Fatalf("mutating clone reasoning leaked to root")
	}

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	var maxSlotWait atomic.Int64
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(lp *LemonadeProvider) {
			defer wg.Done()
			events, err := lp.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "ping"}}, nil)
			if err != nil {
				errs <- err
				return
			}
			var gotDone bool
			for ev := range events {
				if ev.Err != nil {
					errs <- ev.Err
					return
				}
				if ev.Done {
					gotDone = true
					if ev.Usage != nil && ev.Usage.Timings.SlotWaitMS > maxSlotWait.Load() {
						maxSlotWait.Store(ev.Usage.Timings.SlotWaitMS)
					}
				}
			}
			if !gotDone {
				errs <- fmt.Errorf("stream ended without Done")
			}
		}(clones[i])
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent clone StreamChat failed: %v", err)
	}

	srv.mu.Lock()
	maxConc := srv.maxInFlightChat
	loadHits := srv.loadUnloadHits
	srv.mu.Unlock()
	if maxConc != 1 {
		t.Fatalf("max concurrent chats under -np 1 = %d, want 1", maxConc)
	}
	if loadHits != 0 {
		t.Fatalf("SlotGuard must never call POST /load or /unload, saw %d hits", loadHits)
	}
	stats := root.SlotStats()
	if stats.Admitted != workers || stats.Waits == 0 || stats.InFlight != 0 {
		t.Fatalf("unexpected SlotStats: %+v", stats)
	}
}

func TestSlotGuard_ParallelNP2AllowsTwoConcurrent(t *testing.T) {
	srv := newSlotGuardTestServer()
	defer srv.close()
	srv.models["ModelA"].ParallelSlots = 2
	srv.slotsCount = 2
	srv.chatDelay = 60 * time.Millisecond

	root := NewLemonade(Spec{ID: "lemonade", Type: "lemonade", BaseURL: srv.url(), Model: "ModelA"})
	const workers = 4
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		c, _ := root.CloneForRun()
		lp := c.(*LemonadeProvider)
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := lp.Complete(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
			if err != nil {
				errs <- err
				return
			}
			if resp.Content != "ok" {
				errs <- fmt.Errorf("unexpected content %q", resp.Content)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("worker failed: %v", err)
	}

	srv.mu.Lock()
	maxConc := srv.maxInFlightChat
	srv.mu.Unlock()
	if maxConc != 2 {
		t.Fatalf("max concurrent chats under -np 2 = %d, want 2", maxConc)
	}
	if stats := root.SlotStats(); stats.InFlight != 0 || stats.Admitted != workers {
		t.Fatalf("unexpected stats after completion: %+v", stats)
	}
}

func TestSlotGuard_GateBeforeLeaseAndReleaseOnFailures(t *testing.T) {
	srv := newSlotGuardTestServer()
	defer srv.close()
	holdCh := make(chan struct{})
	srv.chatHoldCh = holdCh

	root := NewLemonade(Spec{ID: "lemonade", Type: "lemonade", BaseURL: srv.url(), Model: "ModelA"})

	// 1. Overlapping call on the SAME clone returns ErrInFlight cleanly.
	ev1, err := root.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "first"}}, nil)
	if err != nil {
		t.Fatalf("first StreamChat: %v", err)
	}
	<-srv.chatStartedCh
	if _, err := root.Complete(context.Background(), []Message{{Role: UserRole, Content: "overlap"}}, nil); !IsInFlight(err) {
		t.Fatalf("overlapping call on same clone err = %v, want ErrInFlight", err)
	}

	// 2. Clone blocked waiting on SlotGuard cancels: its per-clone gate MUST release.
	c2Any, _ := root.CloneForRun()
	c2 := c2Any.(*LemonadeProvider)
	cancelCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c2.Complete(cancelCtx, []Message{{Role: UserRole, Content: "canceled"}}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled clone acquire err = %v, want DeadlineExceeded", err)
	}

	// Release Stream 1 and drain it.
	close(holdCh)
	for range ev1 {
	}

	// Now c2's gate must be free so a fresh request succeeds immediately.
	srv.mu.Lock()
	srv.chatHoldCh = nil
	srv.mu.Unlock()
	if _, err := c2.Complete(context.Background(), []Message{{Role: UserRole, Content: "retry after cancel"}}, nil); err != nil {
		t.Fatalf("c2 gate remained wedged after canceled acquire: %v", err)
	}

	// 3. HTTP error from /chat/completions must release both slot lease and gate.
	srv.mu.Lock()
	srv.failChatStatus = http.StatusInternalServerError
	srv.mu.Unlock()
	if _, err := c2.Complete(context.Background(), []Message{{Role: UserRole, Content: "500"}}, nil); err == nil {
		t.Fatal("expected HTTP 500 error")
	}
	srv.mu.Lock()
	srv.failChatStatus = 0
	srv.mu.Unlock()
	if _, err := c2.Complete(context.Background(), []Message{{Role: UserRole, Content: "after 500"}}, nil); err != nil {
		t.Fatalf("c2 failed after prior HTTP 500 (gate or lease leaked): %v", err)
	}
	if stats := root.SlotStats(); stats.InFlight != 0 {
		t.Fatalf("InFlight = %d, want 0", stats.InFlight)
	}
}

type slowCloseTransport struct {
	inner       http.RoundTripper
	closeDelay  time.Duration
	closeDoneAt chan time.Time
}

func (t *slowCloseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(req.URL.Path, "/chat/completions") {
		resp.Body = &slowCloseBody{
			ReadCloser: resp.Body,
			delay:      t.closeDelay,
			closedAt:   t.closeDoneAt,
		}
	}
	return resp, nil
}

type slowCloseBody struct {
	io.ReadCloser
	delay    time.Duration
	closedAt chan time.Time
	once     sync.Once
}

func (b *slowCloseBody) Close() error {
	time.Sleep(b.delay)
	err := b.ReadCloser.Close()
	b.once.Do(func() {
		b.closedAt <- time.Now()
	})
	return err
}

func TestSlotGuard_HoldsLeaseUntilActualHTTPAndSSETerminates(t *testing.T) {
	srv := newSlotGuardTestServer()
	defer srv.close()
	holdCh := make(chan struct{})
	srv.chatHoldCh = holdCh

	root := NewLemonade(Spec{ID: "lemonade", Type: "lemonade", BaseURL: srv.url(), Model: "ModelA"})
	closeDoneAt := make(chan time.Time, 2)
	root.client = &http.Client{
		Timeout:   30 * time.Second,
		Transport: &slowCloseTransport{inner: http.DefaultTransport, closeDelay: 60 * time.Millisecond, closeDoneAt: closeDoneAt},
	}

	c2Any, _ := root.CloneForRun()
	c2 := c2Any.(*LemonadeProvider)

	// Start Stream 1 and cancel its caller context WITHOUT draining; Waiter 2 must
	// still wait until Stream 1's resp.Body.Close() finishes terminating HTTP/SSE!
	ctx1, cancel1 := context.WithCancel(context.Background())
	ev1, err := root.StreamChat(ctx1, []Message{{Role: UserRole, Content: "stream1"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat 1: %v", err)
	}
	<-srv.chatStartedCh

	c2EnteredAt := make(chan time.Time, 1)
	go func() {
		_, err := c2.Complete(context.Background(), []Message{{Role: UserRole, Content: "turn2"}}, nil)
		if err != nil {
			t.Errorf("turn2 failed: %v", err)
		}
	}()

	time.Sleep(25 * time.Millisecond)
	select {
	case m := <-srv.chatStartedCh:
		t.Fatalf("turn2 (%s) acquired slot before Stream 1 terminated", m)
	default:
	}

	// Cancel caller 1 while server releases; resp.Body.Close() takes an extra 60ms.
	cancel1()
	close(holdCh)
	for range ev1 {
	}

	<-srv.chatStartedCh
	c2EnteredAt <- time.Now()

	closed := <-closeDoneAt
	entered := <-c2EnteredAt
	if entered.Before(closed) {
		t.Fatalf("turn2 entered server (%v) BEFORE Stream 1 Body.Close completed (%v)", entered, closed)
	}
}

func TestSlotGuard_ModelSwitchNoDeadlockWithLocalInFlightAndExternalBusy(t *testing.T) {
	srv := newSlotGuardTestServer()
	defer srv.close()
	srv.markBusyDuringChat = true
	holdCh := make(chan struct{})
	srv.chatHoldCh = holdCh

	root := NewLemonade(Spec{ID: "lemonade", Type: "lemonade", BaseURL: srv.url(), Model: "ModelA"})

	// Clone 1 executes ModelA; mock server sets ModelA.IsBusy = true while HTTP runs.
	c1Any, _ := root.CloneForRun()
	c1 := c1Any.(*LemonadeProvider)
	ev1, err := c1.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "run A"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat A: %v", err)
	}
	<-srv.chatStartedCh

	// Clone 2 shares root.slotGuard and targets ModelB while ModelA is loaded + is_busy=true.
	// Pre-populate cachedHealth with ModelA.is_busy=true (fetched while c1 is running) to
	// prove Release() invalidates stale cached is_busy rather than deadlocking!
	c2Any, _ := root.CloneForRun()
	c2 := c2Any.(*LemonadeProvider)
	c2.spec.Model = "ModelB"

	bDone := make(chan error, 1)
	go func() {
		_, err := c2.Complete(context.Background(), []Message{{Role: UserRole, Content: "run B"}}, nil)
		bDone <- err
	}()

	// Wait briefly to confirm ModelB is blocked locally behind ModelA.
	time.Sleep(35 * time.Millisecond)
	select {
	case m := <-srv.chatStartedCh:
		t.Fatalf("ModelB (%s) started prematurely while ModelA was active", m)
	case err := <-bDone:
		t.Fatalf("ModelB finished prematurely: %v", err)
	default:
	}

	// Finish ModelA; ModelB must unblock and succeed cleanly without deadlocking on is_busy.
	close(holdCh)
	for range ev1 {
	}
	<-srv.chatFinishedCh

	select {
	case err := <-bDone:
		if err != nil {
			t.Fatalf("ModelB failed after ModelA released: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ModelB deadlocked waiting after ModelA released")
	}

	// Now prove -np 2 same-model concurrent turn is NOT deadlocked by Forcefield's own is_busy!
	srv.mu.Lock()
	srv.models["ModelA"].Loaded = true
	srv.models["ModelA"].ParallelSlots = 2
	srv.models["ModelA"].IsBusy = false
	srv.models["ModelB"].Loaded = false
	srv.slotsCount = 2
	holdCh2 := make(chan struct{})
	srv.chatHoldCh = holdCh2
	srv.mu.Unlock()

	root2 := NewLemonade(Spec{ID: "lemonade", Type: "lemonade", BaseURL: srv.url(), Model: "ModelA"})
	s1Any, _ := root2.CloneForRun()
	s2Any, _ := root2.CloneForRun()
	s1, s2 := s1Any.(*LemonadeProvider), s2Any.(*LemonadeProvider)

	stream1, err := s1.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "slot0"}}, nil)
	if err != nil {
		t.Fatalf("s1 StreamChat: %v", err)
	}
	<-srv.chatStartedCh // ModelA now has IsBusy=true in /health from Forcefield slot0.

	s2Done := make(chan error, 1)
	go func() {
		_, err := s2.Complete(context.Background(), []Message{{Role: UserRole, Content: "slot1"}}, nil)
		s2Done <- err
	}()

	select {
	case started := <-srv.chatStartedCh:
		if started != "ModelA" {
			t.Fatalf("unexpected model started %q", started)
		}
	case <-time.After(time.Second):
		t.Fatal("second -np 2 slot deadlocked on Forcefield's own is_busy=true")
	}

	close(holdCh2)
	for range stream1 {
	}
	if err := <-s2Done; err != nil {
		t.Fatalf("s2 failed: %v", err)
	}
}

func TestSlotGuard_ExternalBusyWaitsAndRespectsContextCancel(t *testing.T) {
	srv := newSlotGuardTestServer()
	defer srv.close()
	srv.models["ModelA"].IsBusy = true // external non-Forcefield work

	root := NewLemonade(Spec{ID: "lemonade", Type: "lemonade", BaseURL: srv.url(), Model: "ModelB"})
	root.slotGuard.pollInterval = 15 * time.Millisecond

	cancelCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := root.Complete(cancelCtx, []Message{{Role: UserRole, Content: "blocked by external"}}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded while external busy, got %v", err)
	}

	// Gate must remain clean after cancellation, and clearing external busy lets next call run.
	srv.mu.Lock()
	srv.models["ModelA"].IsBusy = false
	srv.mu.Unlock()
	if _, err := root.Complete(context.Background(), []Message{{Role: UserRole, Content: "now clear"}}, nil); err != nil {
		t.Fatalf("Complete after external busy cleared failed: %v", err)
	}
}

func TestSlotGuard_NonLlamaCppAndSlots400Fallback(t *testing.T) {
	srv := newSlotGuardTestServer()
	defer srv.close()
	srv.models["ModelA"].Recipe = "flm"
	srv.models["ModelA"].ParallelSlots = 2
	srv.slotsStatus = http.StatusBadRequest

	root := NewLemonade(Spec{ID: "lemonade", Type: "lemonade", BaseURL: srv.url(), Model: "ModelA"})
	if _, err := root.Complete(context.Background(), []Message{{Role: UserRole, Content: "flm"}}, nil); err != nil {
		t.Fatalf("non-llamacpp request failed: %v", err)
	}
	srv.mu.Lock()
	slotsHits := srv.slotsHits
	srv.mu.Unlock()
	if slotsHits != 0 {
		t.Fatalf("expected 0 /slots hits for non-llamacpp recipe, got %d", slotsHits)
	}

	// Now switch recipe to llamacpp while /slots still returns 400 -> silent fallback to launch_command -np.
	srv.mu.Lock()
	srv.models["ModelA"].Recipe = "llamacpp"
	srv.models["ModelA"].ParallelSlots = 2
	srv.mu.Unlock()
	root2 := NewLemonade(Spec{ID: "lemonade", Type: "lemonade", BaseURL: srv.url(), Model: "ModelA"})
	if _, err := root2.Complete(context.Background(), []Message{{Role: UserRole, Content: "llamacpp 400"}}, nil); err != nil {
		t.Fatalf("llamacpp /slots 400 fallback failed: %v", err)
	}
}

func TestSlotGuard_PinnedSaturationFailsFastAndDegradedFailOpenWorks(t *testing.T) {
	srv := newSlotGuardTestServer()
	defer srv.close()
	srv.models["ModelA"].Pinned = true

	root := NewLemonade(Spec{ID: "lemonade", Type: "lemonade", BaseURL: srv.url(), Model: "ModelB"})
	_, err := root.Complete(context.Background(), []Message{{Role: UserRole, Content: "pinned conflict"}}, nil)
	if err == nil {
		t.Fatal("expected fail-fast pinned saturation error")
	}
	if Classify(err) != ErrKindInvalidRequest || IsTransient(err) {
		t.Fatalf("expected non-transient ErrKindInvalidRequest, got kind=%v transient=%v err=%v", Classify(err), IsTransient(err), err)
	}
	srv.mu.Lock()
	chatHits := srv.chatHits
	srv.mu.Unlock()
	if chatHits != 0 {
		t.Fatalf("expected 0 /chat/completions calls on pinned saturation, got %d", chatHits)
	}
	// Gate must be unlocked for subsequent attempts.
	srv.mu.Lock()
	srv.models["ModelA"].Pinned = false
	srv.mu.Unlock()
	if _, err := root.Complete(context.Background(), []Message{{Role: UserRole, Content: "unpinned"}}, nil); err != nil {
		t.Fatalf("expected success after unpinning: %v", err)
	}

	// Degraded fail-open: /health returns 404 (e.g. proxy hides /health); chat must still succeed!
	srv.mu.Lock()
	srv.healthStatus = http.StatusNotFound
	srv.mu.Unlock()
	degradedRoot := NewLemonade(Spec{ID: "lemonade", Type: "lemonade", BaseURL: srv.url(), Model: "ModelB"})
	if _, err := degradedRoot.Complete(context.Background(), []Message{{Role: UserRole, Content: "proxy without health"}}, nil); err != nil {
		t.Fatalf("degraded fail-open failed when /health returned 404: %v", err)
	}
	if stats := degradedRoot.SlotStats(); stats.DegradedFallbacks == 0 || stats.InFlight != 0 {
		t.Fatalf("unexpected degraded stats: %+v", stats)
	}

	// Advance fake clock past degraded cooldown and restore /health 200 -> recovers cleanly.
	now := time.Now()
	degradedRoot.slotGuard.now = func() time.Time { return now }
	degradedRoot.slotGuard.mu.Lock()
	degradedRoot.slotGuard.degradedUntil = now.Add(slotGuardDegradedCooldown)
	degradedRoot.slotGuard.mu.Unlock()
	now = now.Add(slotGuardDegradedCooldown + time.Second)
	srv.mu.Lock()
	srv.healthStatus = http.StatusOK
	srv.mu.Unlock()
	if _, err := degradedRoot.Complete(context.Background(), []Message{{Role: UserRole, Content: "recovered"}}, nil); err != nil {
		t.Fatalf("recovered health call failed: %v", err)
	}
	if stats := degradedRoot.SlotStats(); stats.DegradedFallbacks != 1 || stats.Admitted != 2 {
		t.Fatalf("expected clean recovery after cooldown, stats=%+v", stats)
	}
}

func TestSlotGuard_ContextCancelDuringHealthProbeDoesNotPoisonDegradedMode(t *testing.T) {
	srv := newSlotGuardTestServer()
	defer srv.close()
	srv.healthDelay = 80 * time.Millisecond

	root := NewLemonade(Spec{ID: "lemonade", Type: "lemonade", BaseURL: srv.url(), Model: "ModelA"})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if _, err := root.Complete(ctx, []Message{{Role: UserRole, Content: "timeout health"}}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded on canceled health probe, got %v", err)
	}

	// Next call with healthy timeout must NOT enter degraded mode.
	srv.mu.Lock()
	srv.healthDelay = 0
	srv.mu.Unlock()
	if _, err := root.Complete(context.Background(), []Message{{Role: UserRole, Content: "healthy"}}, nil); err != nil {
		t.Fatalf("healthy call failed: %v", err)
	}
	if stats := root.SlotStats(); stats.DegradedFallbacks != 0 {
		t.Fatalf("caller cancellation poisoned health into degraded mode: stats=%+v", stats)
	}
}

func TestSlotGuard_LeaseReleasesBeforeGateOnStreamAndComplete(t *testing.T) {
	srv := newSlotGuardTestServer()
	defer srv.close()
	holdCh := make(chan struct{})
	srv.chatHoldCh = holdCh

	root := NewLemonade(Spec{ID: "lemonade", Type: "lemonade", BaseURL: srv.url(), Model: "ModelA"})

	// Check 1: StreamChat normal completion + cancellation release lease before gate opens.
	for _, cancelMidStream := range []bool{false, true} {
		srv.mu.Lock()
		hold := make(chan struct{})
		srv.chatHoldCh = hold
		srv.mu.Unlock()

		ctx, cancel := context.WithCancel(context.Background())
		evCh, err := root.StreamChat(ctx, []Message{{Role: UserRole, Content: "stream"}}, nil)
		if err != nil {
			cancel()
			t.Fatalf("StreamChat(cancel=%v): %v", cancelMidStream, err)
		}
		<-srv.chatStartedCh
		if cancelMidStream {
			cancel()
		}
		close(hold)
		for range evCh {
		}
		cancel()

		// Spin until this same clone's per-clone gate accepts the next call (i.e. gate released).
		// At the exact moment gate releases, shared lease MUST already be released (InFlight == 0)
		// because defer order is defer gate.release() followed by defer lease.Release()!
		srv.mu.Lock()
		srv.chatHoldCh = nil
		srv.mu.Unlock()
		for {
			_, err := root.Complete(context.Background(), []Message{{Role: UserRole, Content: "probe"}}, nil)
			if IsInFlight(err) {
				time.Sleep(time.Millisecond)
				continue
			}
			if err != nil {
				t.Fatalf("follow-up Complete after stream(cancel=%v): %v", cancelMidStream, err)
			}
			break
		}
		if got := root.SlotStats().InFlight; got != 0 {
			t.Fatalf("after gate released (cancel=%v), InFlight = %d, want 0", cancelMidStream, got)
		}
	}

	// Check 2: Direct HTTP error path releases lease before gate.
	srv.mu.Lock()
	srv.chatHoldCh = nil
	srv.failChatStatus = http.StatusBadRequest
	srv.mu.Unlock()
	if _, err := root.Complete(context.Background(), []Message{{Role: UserRole, Content: "400"}}, nil); err == nil {
		t.Fatal("expected HTTP 400 error")
	}
	if got := root.SlotStats().InFlight; got != 0 {
		t.Fatalf("after 400 error, InFlight = %d, want 0", got)
	}
	if err := root.gate.acquire(); err != nil {
		t.Fatalf("expected gate to be free after 400 error: %v", err)
	}
	root.gate.release()
}

func TestSlotGuard_DisableSlotGuardBypassesQueue(t *testing.T) {
	srv := newSlotGuardTestServer()
	defer srv.close()
	srv.chatDelay = 40 * time.Millisecond

	root := NewLemonade(Spec{
		ID:               "lemonade",
		Type:             "lemonade",
		BaseURL:          srv.url(),
		Model:            "ModelA",
		DisableSlotGuard: true,
	})
	if root.SlotGuardEnabled() {
		t.Fatal("expected SlotGuardEnabled() == false when DisableSlotGuard is true")
	}

	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		c, err := root.CloneForRun()
		if err != nil {
			t.Fatal(err)
		}
		lp := c.(*LemonadeProvider)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := lp.Complete(context.Background(), []Message{{Role: UserRole, Content: "unthrottled"}}, nil); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("unthrottled clone failed: %v", err)
	}
	srv.mu.Lock()
	maxConc := srv.maxInFlightChat
	healthHits := srv.healthHits
	srv.mu.Unlock()
	if maxConc < 2 {
		t.Fatalf("expected concurrent server calls when SlotGuard disabled, got maxConc=%d", maxConc)
	}
	if healthHits != 0 {
		t.Fatalf("expected 0 /health hits when DisableSlotGuard=true, got %d", healthHits)
	}
}
