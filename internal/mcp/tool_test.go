package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"forcefield/internal/tools"
)

// Deterministic adapter tests drive the real transport and client over
// in-memory pipes, reusing the client_test.go harness: the test reads each
// outbound tools/call line, asserts the wire identity (original remote
// name, never the qualified name), and writes canned results. Guards are
// hang backstops only, except documented quiet checks proving nothing is
// sent on failure paths.

// readyAdapterHarness drives a client through initialize plus one
// tools/list page built from entries, returning the harness and the
// validated snapshot for adapter construction.
func readyAdapterHarness(t *testing.T, serverKey string, entries []string) (*clientHarness, []DiscoveredTool) {
	t.Helper()
	h := newClientHarness(t, serverKey)
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	_, _, rawID := h.nextRequest(t)
	h.answerResult(t, rawID, listPage(entries, ""))
	if err := awaitOp(t, ch); err != nil {
		t.Fatalf("Discover error = %v", err)
	}
	defs, err := h.cl.Tools()
	if err != nil {
		t.Fatalf("Tools error = %v", err)
	}
	return h, defs
}

// startExecute runs adapter execution concurrently (pipe writes
// rendezvous, so the test goroutine must keep draining).
func startExecute(ctx context.Context, tool *Tool, args map[string]any) <-chan executeOutcome {
	ch := make(chan executeOutcome, 1)
	go func() {
		res, err := tool.Execute(ctx, args)
		ch <- executeOutcome{res: res, err: err}
	}()
	return ch
}

type executeOutcome struct {
	res tools.Result
	err error
}

func awaitExecute(t *testing.T, ch <-chan executeOutcome) (tools.Result, error) {
	t.Helper()
	select {
	case out := <-ch:
		return out.res, out.err
	case <-time.After(clientHarnessTimeout):
		t.Fatal("execute never settled")
		return tools.Result{}, nil
	}
}

// nextCall reads one outbound tools/call and decodes its params.
func nextCall(t *testing.T, h *clientHarness) (CallToolParams, json.RawMessage) {
	t.Helper()
	method, params, rawID := h.nextRequest(t)
	if method != MethodCallTool {
		t.Fatalf("method = %q, want %q", method, MethodCallTool)
	}
	var p CallToolParams
	if err := json.Unmarshal(params, &p); err != nil {
		t.Fatalf("call params: %v", err)
	}
	return p, rawID
}

func textResult(texts ...string) string {
	parts := make([]string, 0, len(texts))
	for _, tx := range texts {
		enc, _ := json.Marshal(tx)
		parts = append(parts, `{"type":"text","text":`+string(enc)+`}`)
	}
	return `{"content":[` + strings.Join(parts, ",") + `]}`
}

func TestNewToolValid(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{
		toolEntry("read_file", "reads files", `{"type":"object","properties":{"path":{"type":"string"}}}`),
	})
	adapter, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	if adapter.Name() != "mcp__srv__read_file" {
		t.Errorf("Name = %q", adapter.Name())
	}
	if adapter.ServerKey() != "srv" || adapter.RemoteName() != "read_file" {
		t.Errorf("server = %q remote = %q", adapter.ServerKey(), adapter.RemoteName())
	}
	if adapter.Description() != "reads files" {
		t.Errorf("Description = %q", adapter.Description())
	}
}

func TestNewToolRejects(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{
		toolEntry("read_file", "reads", `{"type":"object"}`),
	})
	good := defs[0]
	cases := map[string]DiscoveredTool{
		"unqualified":   {Server: "srv", Name: "read_file", Qualified: "read_file"},
		"empty":         {},
		"empty-remote":  {Server: "srv", Name: "", Qualified: "mcp__srv__x"},
		"empty-server":  {Server: "", Name: "read_file", Qualified: "mcp__srv__read_file"},
		"mismatch":      {Server: "srv", Name: "read_file", Qualified: "mcp__srv__other"},
		"other-server":  {Server: "other", Name: "read_file", Qualified: "mcp__other__read_file"},
		"bad-qualified": {Server: "srv", Name: "read_file", Qualified: "mcp__srv__has space"},
	}
	for name, def := range cases {
		if _, err := NewTool(h.cl, def); err == nil {
			t.Errorf("%s: NewTool accepted invalid definition %+v", name, def)
		}
	}
	if _, err := NewTool(nil, good); err == nil {
		t.Error("nil client accepted")
	}
}

func TestToolDescriptionBounded(t *testing.T) {
	h, _ := readyAdapterHarness(t, "srv", []string{
		toolEntry("t", "short", `{"type":"object"}`),
	})
	rogue := DiscoveredTool{
		Server: "srv", Name: "t", Qualified: "mcp__srv__t",
		Description: strings.Repeat("d", MaxDescriptionBytes+100),
	}
	adapter, err := NewTool(h.cl, rogue)
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	if !strings.HasSuffix(adapter.Description(), TruncationMarker) {
		t.Error("rogue description not truncated with marker")
	}
	if len(adapter.Description()) > MaxDescriptionBytes+len(TruncationMarker) {
		t.Error("rogue description unbounded")
	}
}

func TestToolSchemaReadOnly(t *testing.T) {
	schema := `{"type":"object","properties":{"path":{"type":"string"},"opts":{"type":"object","properties":{"n":{"type":"number"}}},"tags":{"type":"array"}}}`
	h, defs := readyAdapterHarness(t, "srv", []string{toolEntry("t", "d", schema)})
	adapter, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	first := adapter.InputSchema()
	if first["type"] != "object" {
		t.Fatalf("schema = %v", first)
	}
	// Mutate everything reachable through the returned map, including
	// nested maps and slices.
	first["type"] = "MUTATED"
	first["properties"].(map[string]any)["path"] = "MUTATED"
	first["properties"].(map[string]any)["opts"].(map[string]any)["properties"].(map[string]any)["n"] = "MUTATED"
	first["properties"].(map[string]any)["tags"] = "MUTATED"
	first["injected"] = true
	second := adapter.InputSchema()
	if second["type"] != "object" {
		t.Errorf("adapter snapshot mutated via returned map: %v", second["type"])
	}
	if _, ok := second["injected"]; ok {
		t.Error("injected key leaked into adapter snapshot")
	}
	props := second["properties"].(map[string]any)
	if _, ok := props["path"].(map[string]any); !ok {
		t.Errorf("nested schema mutated: %v", props["path"])
	}
	// The client's discovery snapshot is equally unaffected.
	defsAgain, _ := h.cl.Tools()
	if defsAgain[0].InputSchema["type"] != "object" {
		t.Error("client snapshot mutated through adapter")
	}
}

func TestToolExecuteSuccess(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{
		toolEntry("read_file", "reads", `{"type":"object","properties":{"path":{"type":"string"}}}`),
	})
	adapter, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	args := map[string]any{"path": "/x/y"}
	ch := startExecute(context.Background(), adapter, args)
	params, rawID := nextCall(t, h)
	// Wire identity is the ORIGINAL remote name, never qualified.
	if params.Name != "read_file" {
		t.Errorf("wire tool name = %q, want original remote name", params.Name)
	}
	if params.Arguments["path"] != "/x/y" {
		t.Errorf("wire args = %v", params.Arguments)
	}
	h.answerResult(t, rawID, textResult("hello", "world"))
	res, err := awaitExecute(t, ch)
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if res.IsError {
		t.Error("IsError set on success")
	}
	if res.Content != "hello\nworld" {
		t.Errorf("Content = %q", res.Content)
	}
	if res.Tool != "mcp__srv__read_file" {
		t.Errorf("Result.Tool = %q, want qualified identity", res.Tool)
	}
	if res.DurationMs < 0 {
		t.Errorf("DurationMs = %d", res.DurationMs)
	}
	if res.Metadata != nil {
		t.Errorf("Metadata = %v, want nil when nothing truncated", res.Metadata)
	}
}

func TestToolExecuteNilArgs(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{toolEntry("ping", "p", `{"type":"object"}`)})
	adapter, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	ch := startExecute(context.Background(), adapter, nil)
	params, rawID := nextCall(t, h)
	if params.Name != "ping" {
		t.Errorf("wire name = %q", params.Name)
	}
	if len(params.Arguments) != 0 {
		t.Errorf("wire args = %v, want empty object (absent encodes as empty)", params.Arguments)
	}
	h.answerResult(t, rawID, textResult("pong"))
	res, err := awaitExecute(t, ch)
	if err != nil || res.Content != "pong" {
		t.Errorf("res = %+v err = %v", res, err)
	}
}

func TestToolExecuteConcurrent(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{toolEntry("echo", "e", `{"type":"object"}`)})
	adapter, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	const n = 8
	type result struct {
		res tools.Result
		err error
	}
	out := make([]result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := adapter.Execute(context.Background(), map[string]any{"n": i})
			out[i] = result{res, err}
		}(i)
	}
	// Single-threaded peer loop: read each call, echo its args back.
	// Transport correlation (not arrival order) routes every reply.
	for i := 0; i < n; i++ {
		params, rawID := nextCall(t, h)
		nVal, _ := params.Arguments["n"].(float64)
		h.answerResult(t, rawID, textResult(fmt.Sprintf("n=%v", nVal)))
	}
	wg.Wait()
	seen := make(map[string]bool, n)
	for i, r := range out {
		if r.err != nil {
			t.Errorf("call %d err = %v", i, r.err)
			continue
		}
		if r.res.IsError {
			t.Errorf("call %d IsError set", i)
		}
		seen[r.res.Content] = true
	}
	if len(seen) != n {
		t.Errorf("got %d distinct results, want %d (correlation broken)", len(seen), n)
	}
}

func TestToolExecuteNotReady(t *testing.T) {
	h := newClientHarness(t, "srv") // never initialized
	def := DiscoveredTool{Server: "srv", Name: "t", Qualified: "mcp__srv__t", Description: "d"}
	adapter, err := NewTool(h.cl, def)
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	ch := startExecute(context.Background(), adapter, map[string]any{})
	_, err = awaitExecute(t, ch)
	if !errors.Is(err, ErrTransport) {
		t.Errorf("err = %v, want ErrTransport for unready client", err)
	}
	expectQuiet(t, h) // nothing reached the wire
}

func TestToolExecuteTransportDead(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{toolEntry("t", "d", `{"type":"object"}`)})
	adapter, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	_ = h.fromW.Close() // peer EOF kills the stream; no reconnect exists
	select {
	case <-h.tr.Done():
	case <-time.After(clientHarnessTimeout):
		t.Fatal("reader did not exit on EOF")
	}
	if h.cl.Ready() {
		t.Fatal("client still ready over dead transport")
	}
	_, err = adapter.Execute(context.Background(), map[string]any{})
	if !errors.Is(err, ErrTransport) {
		t.Errorf("err = %v, want ErrTransport", err)
	}
}

func TestToolExecuteClosedClient(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{toolEntry("t", "d", `{"type":"object"}`)})
	adapter, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	if err := h.cl.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	ch := startExecute(context.Background(), adapter, map[string]any{})
	_, err = awaitExecute(t, ch)
	if !errors.Is(err, ErrClosed) {
		t.Errorf("err = %v, want ErrClosed (distinguishable from transport failure)", err)
	}
	expectQuiet(t, h)
}

func TestToolExecuteRPCError(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{toolEntry("t", "d", `{"type":"object"}`)})
	adapter, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	ch := startExecute(context.Background(), adapter, map[string]any{"a": 1})
	_, rawID := nextCall(t, h)
	h.answerError(t, rawID, -32602, "bad params")
	_, err = awaitExecute(t, ch)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %v, want *RPCError preserved", err)
	}
	if rpcErr.Code != -32602 {
		t.Errorf("code = %d", rpcErr.Code)
	}
}

func TestToolExecuteIsError(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{toolEntry("t", "d", `{"type":"object"}`)})
	adapter, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	ch := startExecute(context.Background(), adapter, map[string]any{})
	_, rawID := nextCall(t, h)
	h.answerResult(t, rawID, `{"content":[{"type":"text","text":"declined: nope"}],"isError":true}`)
	res, err := awaitExecute(t, ch)
	if err != nil {
		t.Fatalf("isError must be a soft result, got hard error %v", err)
	}
	if !res.IsError {
		t.Error("IsError not set")
	}
	if res.Content != "declined: nope" {
		t.Errorf("Content = %q", res.Content)
	}
}

func TestToolResultStructuredFallback(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{toolEntry("t", "d", `{"type":"object"}`)})
	adapter, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	ch := startExecute(context.Background(), adapter, map[string]any{})
	_, rawID := nextCall(t, h)
	h.answerResult(t, rawID, `{"content":[],"structuredContent":{"temp":21.5,"ok":true}}`)
	res, err := awaitExecute(t, ch)
	if err != nil || res.IsError {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(res.Content), &decoded); err != nil {
		t.Fatalf("structured fallback is not JSON: %q (%v)", res.Content, err)
	}
	if decoded["temp"] != 21.5 || decoded["ok"] != true {
		t.Errorf("decoded = %v", decoded)
	}
}

func TestToolResultNonTextPlaceholder(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{toolEntry("t", "d", `{"type":"object"}`)})
	adapter, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	ch := startExecute(context.Background(), adapter, map[string]any{})
	_, rawID := nextCall(t, h)
	h.answerResult(t, rawID, `{"content":[{"type":"image","data":"AAAA"},{"type":"text","text":"caption"}]}`)
	res, err := awaitExecute(t, ch)
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if !strings.Contains(res.Content, "image") || !strings.Contains(res.Content, "caption") {
		t.Errorf("Content = %q, want placeholder plus caption", res.Content)
	}
	if strings.Contains(res.Content, "AAAA") {
		t.Errorf("raw non-text payload leaked into content: %q", res.Content)
	}
}

func TestToolResultEmpty(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{toolEntry("t", "d", `{"type":"object"}`)})
	adapter, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	ch := startExecute(context.Background(), adapter, map[string]any{})
	_, rawID := nextCall(t, h)
	h.answerResult(t, rawID, `{"content":[]}`)
	res, err := awaitExecute(t, ch)
	if err != nil || res.IsError {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if res.Content != "" {
		t.Errorf("Content = %q, want honestly empty", res.Content)
	}
}

func TestToolResultMalformed(t *testing.T) {
	for name, payload := range map[string]string{
		"array": `[]`,
		"null":  `null`,
		"str":   `"oops"`,
	} {
		t.Run(name, func(t *testing.T) {
			h, defs := readyAdapterHarness(t, "srv", []string{toolEntry("t", "d", `{"type":"object"}`)})
			adapter, err := NewTool(h.cl, defs[0])
			if err != nil {
				t.Fatalf("NewTool error = %v", err)
			}
			ch := startExecute(context.Background(), adapter, map[string]any{})
			_, rawID := nextCall(t, h)
			h.answerResult(t, rawID, payload)
			_, err = awaitExecute(t, ch)
			if !errors.Is(err, ErrInvalidMessage) {
				t.Errorf("err = %v, want ErrInvalidMessage (never silent success)", err)
			}
		})
	}
}

func TestToolResultOversizedTruncated(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{toolEntry("t", "d", `{"type":"object"}`)})
	adapter, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	adapter.SetLimits(tools.Limits{MaxBytes: 64})
	ch := startExecute(context.Background(), adapter, map[string]any{})
	_, rawID := nextCall(t, h)
	h.answerResult(t, rawID, textResult(strings.Repeat("x", 1000)))
	res, err := awaitExecute(t, ch)
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if res.IsError {
		t.Error("truncation must not flip IsError")
	}
	meta, ok := res.Metadata["truncated"]
	if !ok || meta != true {
		t.Errorf("Metadata = %v, want truncated=true", res.Metadata)
	}
	if res.Metadata["limit_bytes"] != 64 {
		t.Errorf("Metadata = %v, want limit_bytes=64", res.Metadata)
	}
	if !strings.Contains(res.Content, "output truncated") {
		t.Errorf("content missing truncation note: %q", res.Content[len(res.Content)-80:])
	}
	if len(res.Content) > 64+256 {
		t.Errorf("content of %d bytes exceeds cap plus note", len(res.Content))
	}
}

func TestToolExecuteBoundedRemoteError(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{toolEntry("t", "d", `{"type":"object"}`)})
	adapter, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	ch := startExecute(context.Background(), adapter, map[string]any{})
	_, rawID := nextCall(t, h)
	h.answerError(t, rawID, -32000, strings.Repeat("e", MaxErrorDetailRunes*4))
	_, err = awaitExecute(t, ch)
	if err == nil {
		t.Fatal("nil error")
	}
	if len([]rune(err.Error())) > MaxErrorDetailRunes+256 {
		t.Errorf("error of %d runes is unbounded", len([]rune(err.Error())))
	}
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32000 {
		t.Errorf("err = %v, want *RPCError with code", err)
	}
}

func TestToolExecuteOversizedArgsRejected(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{toolEntry("t", "d", `{"type":"object"}`)})
	adapter, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	ch := startExecute(context.Background(), adapter, map[string]any{"blob": strings.Repeat("b", MaxMessageBytes)})
	_, err = awaitExecute(t, ch)
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
	expectQuiet(t, h) // rejected before anything reached the wire
}

func TestToolLimitsConventions(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{toolEntry("t", "d", `{"type":"object"}`)})
	adapter, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	def := adapter.ToolLimits()
	if def.Timeout != tools.DefaultToolTimeout {
		t.Errorf("default Timeout = %v, want %v", def.Timeout, tools.DefaultToolTimeout)
	}
	adapter.SetLimits(tools.Limits{MaxBytes: 1024, Timeout: 45 * time.Second})
	got := adapter.ToolLimits()
	if got.MaxBytes != 1024 || got.Timeout != 45*time.Second {
		t.Errorf("limits = %+v, want override applied", got)
	}
	meta := adapter.Metadata()
	if meta.Timeout != got.Timeout {
		t.Errorf("metadata Timeout = %v, want resolved %v", meta.Timeout, got.Timeout)
	}
	if meta.Retryable {
		t.Error("Retryable set; remote calls are not idempotent")
	}
	if !meta.SupportsParallel || !meta.SupportsCancellation || meta.SupportsStreaming {
		t.Errorf("metadata = %+v, want parallel+cancellation, no streaming", meta)
	}
	if _, ok := any(adapter).(tools.BoundaryChecker); ok {
		t.Error("adapter must not claim BoundaryChecker over opaque args")
	}
}

func TestToolRegistryCompatible(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{
		toolEntry("a", "first", `{"type":"object"}`),
		toolEntry("b", "second", `{"type":"object"}`),
	})
	a, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	b, err := NewTool(h.cl, defs[1])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	// Phase 5 path preview on a test-local manager only: construct,
	// register, look up, filter, and execute without touching runtime.
	mgr := tools.NewManager(tools.NewRegistry())
	if err := mgr.Register(a); err != nil {
		t.Fatalf("Register error = %v", err)
	}
	if err := mgr.Register(b); err != nil {
		t.Fatalf("Register error = %v", err)
	}
	if err := mgr.Register(a); !errors.Is(err, tools.ErrAlreadyRegistered) {
		t.Errorf("duplicate register err = %v, want ErrAlreadyRegistered", err)
	}
	if _, ok := mgr.Lookup("mcp__srv__a"); !ok {
		t.Error("Lookup missed qualified name")
	}
	if _, ok := mgr.Lookup("a"); ok {
		t.Error("unqualified name must never resolve")
	}
	filtered, err := mgr.Filtered([]string{"mcp__srv__b"})
	if err != nil {
		t.Fatalf("Filtered error = %v", err)
	}
	if _, ok := filtered.Lookup("mcp__srv__a"); ok {
		t.Error("filtered manager leaks unlisted MCP tool")
	}
	execCh := make(chan executeOutcome, 1)
	go func() {
		res, err := filtered.Execute(context.Background(), "mcp__srv__b", map[string]any{})
		execCh <- executeOutcome{res, err}
	}()
	method, rawParams, rawID := h.nextRequest(t)
	var callParams CallToolParams
	if err := json.Unmarshal(rawParams, &callParams); err != nil {
		t.Fatalf("call params: %v", err)
	}
	if method != MethodCallTool || callParams.Name != "b" {
		t.Errorf("wire call = %q %q, want tools/call for remote b", method, callParams.Name)
	}
	h.answerResult(t, rawID, textResult("via-manager"))
	select {
	case out := <-execCh:
		if out.err != nil || out.res.Content != "via-manager" {
			t.Errorf("res = %+v err = %v", out.res, out.err)
		}
	case <-time.After(clientHarnessTimeout):
		t.Fatal("manager execute timed out")
	}
}
