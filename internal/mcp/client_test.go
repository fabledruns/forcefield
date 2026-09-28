package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// Deterministic client tests drive the real Phase 2 transport over
// in-memory pipes: the test reads each outbound request line and writes
// canned responses, so every interleaving is explicit. A 5s guard wraps
// blocking harness steps purely as a hang backstop, never as a
// correctness condition (except one documented 50ms negative check that
// proves initialized is not sent after failure).

const clientHarnessTimeout = 5 * time.Second

// clientHarness wires a Client to a Transport over two io.Pipes. Cleanup
// closes writers, closes the client, and waits for reader termination.
type clientHarness struct {
	tr    *Transport
	toR   *bufio.Reader
	toW   *io.PipeWriter
	fromW *io.PipeWriter
	cl    *Client
}

func newClientHarness(t *testing.T, serverKey string) *clientHarness {
	t.Helper()
	toR, toW := io.Pipe()
	fromR, fromW := io.Pipe()
	tr, err := NewTransport(fromR, toW)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	cl, err := NewClient(tr, serverKey, DefaultClientInfo())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	h := &clientHarness{tr: tr, toR: bufio.NewReader(toR), toW: toW, fromW: fromW, cl: cl}
	t.Cleanup(func() {
		_ = toW.Close()
		_ = fromW.Close()
		_ = cl.Close()
		select {
		case <-tr.Done():
		case <-time.After(clientHarnessTimeout):
			t.Error("reader goroutine did not exit")
		}
	})
	return h
}

// startOp runs a client operation concurrently (pipe writes rendezvous,
// so the test goroutine must keep draining).
func startOp(op func() error) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- op() }()
	return ch
}

// awaitOp collects an operation outcome with a hang backstop.
func awaitOp(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(clientHarnessTimeout):
		t.Fatal("operation never settled")
		return nil
	}
}

// nextOutbound reads one client frame with a hang backstop, returning
// the parsed form and the raw line for id echoing.
func (h *clientHarness) nextOutbound(t *testing.T) (Message, []byte) {
	t.Helper()
	type res struct {
		line []byte
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		line, err := h.toR.ReadBytes('\n')
		ch <- res{line, err}
	}()
	select {
	case out := <-ch:
		if out.err != nil {
			t.Fatalf("read outbound: %v", out.err)
		}
		msg, err := ParseMessage(out.line)
		if err != nil {
			t.Fatalf("ParseMessage(outbound) error = %v", err)
		}
		return msg, out.line
	case <-time.After(clientHarnessTimeout):
		t.Fatal("timed out waiting for client frame")
		return Message{}, nil
	}
}

// nextRequest reads one client request and returns method, params, and
// the raw id bytes for echoing.
func (h *clientHarness) nextRequest(t *testing.T) (string, json.RawMessage, json.RawMessage) {
	t.Helper()
	msg, line := h.nextOutbound(t)
	if msg.Kind != KindRequest {
		t.Fatalf("outbound kind = %v, want request", msg.Kind)
	}
	var env struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(line, &env); err != nil || len(env.ID) == 0 {
		t.Fatalf("client frame id: %v", err)
	}
	var params json.RawMessage
	if msg.Params != nil {
		params = msg.Params
	}
	return msg.Method, params, env.ID
}

// answer writes one response frame for rawID.
func (h *clientHarness) answer(t *testing.T, rawID json.RawMessage, payload string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := h.fromW.Write([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,%s}`+"\n", string(rawID), payload)))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("answer write: %v", err)
		}
	case <-time.After(clientHarnessTimeout):
		t.Fatal("timed out writing answer")
	}
}

// answerResult writes a result response.
func (h *clientHarness) answerResult(t *testing.T, rawID json.RawMessage, result string) {
	t.Helper()
	h.answer(t, rawID, `"result":`+result)
}

// answerError writes an error response.
func (h *clientHarness) answerError(t *testing.T, rawID json.RawMessage, code int, message string) {
	t.Helper()
	enc, _ := json.Marshal(message)
	h.answer(t, rawID, fmt.Sprintf(`"error":{"code":%d,"message":%s}`, code, string(enc)))
}

// initResult builds an initialize result payload.
func initResult(version, capabilities string) string {
	return fmt.Sprintf(`{"protocolVersion":%s,"capabilities":%s,"serverInfo":{"name":"fake","version":"1"}}`,
		quoteJSONString(version), capabilities)
}

func quoteJSONString(s string) string {
	enc, _ := json.Marshal(s)
	return string(enc)
}

// driveInitialize runs Initialize while answering the handshake from
// scripted result/error payloads. It returns the Initialize outcome after
// consuming exactly the initialize exchange (no initialized line).
func driveInitialize(t *testing.T, h *clientHarness, answerFn func(rawID json.RawMessage)) error {
	t.Helper()
	ch := startOp(func() error { return h.cl.Initialize(context.Background()) })
	method, _, rawID := h.nextRequest(t)
	if method != MethodInitialize {
		t.Fatalf("first method = %q, want %q", method, MethodInitialize)
	}
	answerFn(rawID)
	return awaitOp(t, ch)
}

// succeedInitialize completes a healthy handshake and consumes the
// initialized notification, asserting exact method order and names.
func succeedInitialize(t *testing.T, h *clientHarness) {
	t.Helper()
	ch := startOp(func() error { return h.cl.Initialize(context.Background()) })
	method, params, rawID := h.nextRequest(t)
	if method != MethodInitialize {
		t.Fatalf("first method = %q, want initialize", method)
	}
	var initParams struct {
		ProtocolVersion string `json:"protocolVersion"`
		ClientInfo      struct {
			Name string `json:"name"`
		} `json:"clientInfo"`
	}
	if err := json.Unmarshal(params, &initParams); err != nil {
		t.Fatalf("initialize params: %v", err)
	}
	if initParams.ProtocolVersion != ClientVersion {
		t.Errorf("announced version = %q, want pinned %q", initParams.ProtocolVersion, ClientVersion)
	}
	if initParams.ClientInfo.Name != "forcefield" {
		t.Errorf("client name = %q, want forcefield", initParams.ClientInfo.Name)
	}
	h.answerResult(t, rawID, initResult(ClientVersion, `{"tools":{}}`))
	// Initialized must follow success, as a notification (no id).
	msg, _ := h.nextOutbound(t)
	if msg.Kind != KindNotification || msg.Method != MethodInitialized {
		t.Fatalf("second frame = %v %q, want notification %q", msg.Kind, msg.Method, MethodInitialized)
	}
	if err := awaitOp(t, ch); err != nil {
		t.Fatalf("Initialize error = %v", err)
	}
	if h.cl.State() != StateInitialized {
		t.Errorf("state = %v, want initialized", h.cl.State())
	}
	if h.cl.AgreedVersion() != ClientVersion {
		t.Errorf("agreed = %q, want %q", h.cl.AgreedVersion(), ClientVersion)
	}
}

// expectQuiet asserts no further client frame arrives within a short
// window. This is the one timing-dependent negative check: it proves a
// failed handshake emits nothing more (initialized must not follow
// failure). The window is a harness guard, not a protocol timeout.
func expectQuiet(t *testing.T, h *clientHarness) {
	t.Helper()
	ch := make(chan []byte, 1)
	go func() {
		line, err := h.toR.ReadBytes('\n')
		if err == nil {
			ch <- line
		}
	}()
	select {
	case line := <-ch:
		t.Fatalf("unexpected client frame after failure: %s", strings.TrimSpace(string(line)))
	case <-time.After(50 * time.Millisecond):
	}
}

func toolEntry(name, desc, schema string) string {
	if desc == "" {
		return fmt.Sprintf(`{"name":%s,"inputSchema":%s}`, quoteJSONString(name), schema)
	}
	return fmt.Sprintf(`{"name":%s,"description":%s,"inputSchema":%s}`, quoteJSONString(name), quoteJSONString(desc), schema)
}

func listPage(tools []string, cursor string) string {
	next := ""
	if cursor != "" {
		next = fmt.Sprintf(`,"nextCursor":%s`, quoteJSONString(cursor))
	}
	return fmt.Sprintf(`{"tools":[%s]%s}`, strings.Join(tools, ","), next)
}

func TestClientInitSuccess(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	if h.cl.Ready() {
		t.Error("Ready before discovery; must stay unready until tools snapshot")
	}
	if h.cl.PeerName() != "fake" {
		t.Errorf("peer = %q, want fake", h.cl.PeerName())
	}
}

func TestClientInitUnsupportedVersion(t *testing.T) {
	h := newClientHarness(t, "srv")
	err := driveInitialize(t, h, func(rawID json.RawMessage) {
		h.answerResult(t, rawID, initResult("1999-01-01", `{"tools":{}}`))
	})
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("err = %v, want ErrUnsupportedVersion", err)
	}
	if h.cl.State() != StateFailed {
		t.Errorf("state = %v, want failed", h.cl.State())
	}
	expectQuiet(t, h)
}

func TestClientInitFutureVersion(t *testing.T) {
	h := newClientHarness(t, "srv")
	err := driveInitialize(t, h, func(rawID json.RawMessage) {
		h.answerResult(t, rawID, initResult("2030-01-01", `{"tools":{}}`))
	})
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("err = %v, want ErrUnsupportedVersion", err)
	}
}

func TestClientInitMalformedResult(t *testing.T) {
	for name, payload := range map[string]string{
		"array":   `[]`,
		"string":  `"nope"`,
		"missing": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newClientHarness(t, "srv")
			err := driveInitialize(t, h, func(rawID json.RawMessage) {
				h.answerResult(t, rawID, payload)
			})
			if err == nil {
				t.Fatalf("nil error for %s result", name)
			}
			if h.cl.State() != StateFailed {
				t.Errorf("state = %v, want failed", h.cl.State())
			}
			expectQuiet(t, h)
		})
	}
}

func TestClientInitMissingToolsCapability(t *testing.T) {
	h := newClientHarness(t, "srv")
	err := driveInitialize(t, h, func(rawID json.RawMessage) {
		h.answerResult(t, rawID, initResult(ClientVersion, `{"resources":{}}`))
	})
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("err = %v, want ErrProtocol", err)
	}
	if h.cl.State() != StateFailed {
		t.Errorf("state = %v, want failed", h.cl.State())
	}
}

func TestClientInitServerRPCError(t *testing.T) {
	h := newClientHarness(t, "srv")
	err := driveInitialize(t, h, func(rawID json.RawMessage) {
		h.answerError(t, rawID, -32600, "boom")
	})
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %v, want *RPCError", err)
	}
	if rpcErr.Code != -32600 {
		t.Errorf("code = %d, want -32600", rpcErr.Code)
	}
	if h.cl.State() != StateFailed {
		t.Errorf("state = %v, want failed", h.cl.State())
	}
	expectQuiet(t, h)
}

func TestClientInitTransportFailure(t *testing.T) {
	h := newClientHarness(t, "srv")
	ch := startOp(func() error { return h.cl.Initialize(context.Background()) })
	// Drain the request write so the call is provably pending, then kill
	// the stream: EOF fails it terminally either way the race lands.
	method, _, _ := h.nextRequest(t)
	if method != MethodInitialize {
		t.Fatalf("method = %q, want initialize", method)
	}
	_ = h.fromW.Close()
	if err := awaitOp(t, ch); !errors.Is(err, ErrTransport) {
		t.Fatalf("err = %v, want ErrTransport", err)
	}
	if h.cl.State() != StateFailed {
		t.Errorf("state = %v, want failed", h.cl.State())
	}
}

func TestClientInitTwiceRejected(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	if err := h.cl.Initialize(context.Background()); err == nil {
		t.Fatal("second Initialize accepted")
	} else if !errors.Is(err, ErrProtocol) {
		t.Errorf("err = %v, want ErrProtocol", err)
	}
}

func TestClientDiscoverBeforeInit(t *testing.T) {
	h := newClientHarness(t, "srv")
	if err := h.cl.Discover(context.Background()); !errors.Is(err, ErrProtocol) {
		t.Errorf("err = %v, want ErrProtocol", err)
	}
	if _, err := h.cl.Tools(); err == nil {
		t.Error("Tools before init accepted")
	}
}

func TestClientDiscoverOneTool(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	method, params, rawID := h.nextRequest(t)
	if method != MethodListTools {
		t.Fatalf("method = %q, want tools/list", method)
	}
	var listParams ListToolsParams
	if err := json.Unmarshal(params, &listParams); err != nil {
		t.Fatalf("list params: %v", err)
	}
	if listParams.Cursor != nil {
		t.Errorf("first page cursor = %v, want absent", *listParams.Cursor)
	}
	schema := `{"type":"object","properties":{"path":{"type":"string"}}}`
	h.answerResult(t, rawID, listPage([]string{toolEntry("read_file", "reads files", schema)}, ""))
	if err := awaitOp(t, ch); err != nil {
		t.Fatalf("Discover error = %v", err)
	}
	if h.cl.State() != StateReady || !h.cl.Ready() {
		t.Errorf("state = %v ready = %v, want ready/true", h.cl.State(), h.cl.Ready())
	}
	tools, err := h.cl.Tools()
	if err != nil {
		t.Fatalf("Tools error = %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	got := tools[0]
	if got.Qualified != "mcp__srv__read_file" || got.Name != "read_file" || got.Server != "srv" {
		t.Errorf("tool = %+v, want qualified identity", got)
	}
	if got.Description != "reads files" {
		t.Errorf("description = %q", got.Description)
	}
	if got.InputSchema["type"] != "object" {
		t.Errorf("schema = %v", got.InputSchema)
	}
	warnings, _ := h.cl.Warnings()
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if n, _ := h.cl.Skipped(); n != 0 {
		t.Errorf("skipped = %d, want 0", n)
	}
}

func TestClientDiscoverMultipleTools(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	_, _, rawID := h.nextRequest(t)
	entries := []string{
		toolEntry("a", "first", `{"type":"object"}`),
		toolEntry("b", "second", `{"type":"object"}`),
		toolEntry("c", "", `{"type":"object"}`),
	}
	h.answerResult(t, rawID, listPage(entries, ""))
	if err := awaitOp(t, ch); err != nil {
		t.Fatalf("Discover error = %v", err)
	}
	tools, _ := h.cl.Tools()
	if len(tools) != 3 {
		t.Fatalf("tools = %d, want 3", len(tools))
	}
	for i, want := range []string{"mcp__srv__a", "mcp__srv__b", "mcp__srv__c"} {
		if tools[i].Qualified != want {
			t.Errorf("tool[%d] = %q, want %q", i, tools[i].Qualified, want)
		}
	}
}

func TestClientMalformedToolsSkipped(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	_, _, rawID := h.nextRequest(t)
	entries := []string{
		toolEntry("good", "fine", `{"type":"object"}`),
		`{"name":""}`,
		`{"name":"bad-schema","inputSchema":[]}`,
		`{"name":"bad-props","inputSchema":{"type":"object","properties":[]}}`,
	}
	h.answerResult(t, rawID, listPage(entries, ""))
	if err := awaitOp(t, ch); err != nil {
		t.Fatalf("Discover error = %v, want nil (skips, not failure)", err)
	}
	tools, _ := h.cl.Tools()
	if len(tools) != 1 || tools[0].Name != "good" {
		t.Fatalf("tools = %+v, want only good", tools)
	}
	warnings, _ := h.cl.Warnings()
	if len(warnings) != 3 {
		t.Fatalf("warnings = %d, want 3", len(warnings))
	}
	if n, _ := h.cl.Skipped(); n != 3 {
		t.Errorf("skipped = %d, want 3", n)
	}
	for _, w := range warnings {
		if len([]rune(w)) > MaxErrorDetailRunes+len(TruncationMarker) {
			t.Errorf("warning of %d runes is unbounded", len([]rune(w)))
		}
	}
}

func TestClientOversizedDescriptionTruncated(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	_, _, rawID := h.nextRequest(t)
	big := strings.Repeat("d", MaxDescriptionBytes+100)
	h.answerResult(t, rawID, listPage([]string{toolEntry("t", big, `{"type":"object"}`)}, ""))
	if err := awaitOp(t, ch); err != nil {
		t.Fatalf("Discover error = %v", err)
	}
	tools, _ := h.cl.Tools()
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1 (truncated, not skipped)", len(tools))
	}
	if !strings.HasSuffix(tools[0].Description, TruncationMarker) {
		t.Error("description missing truncation marker")
	}
	if n, _ := h.cl.Skipped(); n != 0 {
		t.Errorf("skipped = %d, want 0", n)
	}
}

func TestClientSchemaViolationsSkipped(t *testing.T) {
	deep := `{"type":"object"}`
	for i := 0; i < MaxSchemaDepth+2; i++ {
		deep = fmt.Sprintf(`{"type":"object","properties":{"n":%s}}`, deep)
	}
	props := []string{}
	for i := 0; i < MaxSchemaProperties+1; i++ {
		props = append(props, fmt.Sprintf(`"p%d":{"type":"string"}`, i))
	}
	manyProps := `{"type":"object","properties":{` + strings.Join(props, ",") + `}}`
	bigEnum := `{"enum":[` + strings.TrimRight(strings.Repeat(`"v",`, MaxSchemaEnumEntries+1), ",") + `]}`
	// Over-long property names are a covered string-leaf path (unknown
	// keywords like "const" are ignored by design, matching ValidateArgs
	// granularity, so they cannot serve as a bound probe here).
	bigPropName := fmt.Sprintf(`{"type":"object","properties":{%s:{"type":"string"}}}`, quoteJSONString(strings.Repeat("p", MaxSchemaStringBytes+1)))
	cases := map[string]string{
		"depth":    deep,
		"props":    manyProps,
		"enum":     bigEnum,
		"string":   bigPropName,
		"oversize": `{"type":"object","blob":` + quoteJSONString(strings.Repeat("z", MaxSchemaBytes)) + `}`,
	}
	for name, schema := range cases {
		t.Run(name, func(t *testing.T) {
			h := newClientHarness(t, "srv")
			succeedInitialize(t, h)
			ch := startOp(func() error { return h.cl.Discover(context.Background()) })
			_, _, rawID := h.nextRequest(t)
			h.answerResult(t, rawID, listPage([]string{toolEntry("t", "d", schema)}, ""))
			if err := awaitOp(t, ch); err != nil {
				t.Fatalf("Discover error = %v, want nil (skip)", err)
			}
			tools, _ := h.cl.Tools()
			if len(tools) != 0 {
				t.Errorf("tools = %d, want 0 skipped", len(tools))
			}
			if n, _ := h.cl.Skipped(); n != 1 {
				t.Errorf("skipped = %d, want 1", n)
			}
		})
	}
}

func TestClientDuplicateQualifiedNamesDropped(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	_, _, rawID := h.nextRequest(t)
	// "a.b" and "a_b" sanitize to the same segment: fail closed, drop both.
	entries := []string{
		toolEntry("a.b", "first", `{"type":"object"}`),
		toolEntry("keep", "fine", `{"type":"object"}`),
		toolEntry("a_b", "second", `{"type":"object"}`),
	}
	h.answerResult(t, rawID, listPage(entries, ""))
	if err := awaitOp(t, ch); err != nil {
		t.Fatalf("Discover error = %v, want nil", err)
	}
	tools, _ := h.cl.Tools()
	if len(tools) != 1 || tools[0].Name != "keep" {
		t.Fatalf("tools = %+v, want only keep", tools)
	}
	warnings, _ := h.cl.Warnings()
	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want 1 collision note", len(warnings))
	}
	if !strings.Contains(warnings[0], "duplicate") {
		t.Errorf("warning %q never names the duplicate", warnings[0])
	}
}

func TestClientExactDuplicateRawNamesDropped(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	_, _, rawID := h.nextRequest(t)
	entries := []string{
		toolEntry("x", "one", `{"type":"object"}`),
		toolEntry("x", "two", `{"type":"object"}`),
	}
	h.answerResult(t, rawID, listPage(entries, ""))
	if err := awaitOp(t, ch); err != nil {
		t.Fatalf("Discover error = %v, want nil", err)
	}
	tools, _ := h.cl.Tools()
	if len(tools) != 0 {
		t.Errorf("tools = %+v, want none (no silent overwrite)", tools)
	}
}

func TestClientEmptyToolList(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	_, _, rawID := h.nextRequest(t)
	h.answerResult(t, rawID, listPage(nil, ""))
	if err := awaitOp(t, ch); err != nil {
		t.Fatalf("Discover error = %v", err)
	}
	tools, _ := h.cl.Tools()
	if len(tools) != 0 {
		t.Errorf("tools = %d, want 0", len(tools))
	}
	if !h.cl.Ready() {
		t.Error("empty snapshot must still be ready")
	}
}

func TestClientMissingToolsArrayFails(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	_, _, rawID := h.nextRequest(t)
	h.answerResult(t, rawID, `{}`)
	if err := awaitOp(t, ch); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("err = %v, want ErrInvalidMessage", err)
	}
	if h.cl.State() != StateFailed {
		t.Errorf("state = %v, want failed", h.cl.State())
	}
}

func TestClientPaginationMultiplePages(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	// Page one: no cursor sent, cursor returned.
	method, params, rawID := h.nextRequest(t)
	if method != MethodListTools {
		t.Fatalf("method = %q", method)
	}
	var p ListToolsParams
	if err := json.Unmarshal(params, &p); err != nil || p.Cursor != nil {
		t.Fatalf("first page params = %s, want no cursor", string(params))
	}
	h.answerResult(t, rawID, listPage([]string{toolEntry("t1", "one", `{"type":"object"}`)}, "c1"))
	// Page two: cursor echoed.
	method, params, rawID = h.nextRequest(t)
	if method != MethodListTools {
		t.Fatalf("method = %q", method)
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Cursor == nil || *p.Cursor != "c1" {
		t.Fatalf("second page params = %s, want cursor c1", string(params))
	}
	h.answerResult(t, rawID, listPage([]string{toolEntry("t2", "two", `{"type":"object"}`)}, ""))
	if err := awaitOp(t, ch); err != nil {
		t.Fatalf("Discover error = %v", err)
	}
	tools, _ := h.cl.Tools()
	if len(tools) != 2 || tools[0].Name != "t1" || tools[1].Name != "t2" {
		t.Fatalf("tools = %+v, want [t1 t2] in order", tools)
	}
}

func TestClientPaginationMaxPages(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	for i := 0; i < MaxListPages; i++ {
		_, _, rawID := h.nextRequest(t)
		// Unique names per page (the duplicate rule is tested
		// separately) and unique cursors per page (repeats fail).
		h.answerResult(t, rawID, listPage([]string{toolEntry(fmt.Sprintf("t%d", i), "d", `{"type":"object"}`)}, fmt.Sprintf("cursor-%d", i)))
	}
	// The cap is hit without an 11th request: discovery fails.
	if err := awaitOp(t, ch); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if h.cl.State() != StateFailed {
		t.Errorf("state = %v, want failed", h.cl.State())
	}
}

func TestClientPaginationRepeatedCursor(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	_, _, rawID := h.nextRequest(t)
	h.answerResult(t, rawID, listPage([]string{toolEntry("t", "d", `{"type":"object"}`)}, "loop"))
	_, _, rawID = h.nextRequest(t)
	h.answerResult(t, rawID, listPage([]string{toolEntry("u", "d", `{"type":"object"}`)}, "loop"))
	if err := awaitOp(t, ch); !errors.Is(err, ErrProtocol) {
		t.Fatalf("err = %v, want ErrProtocol", err)
	}
}

func TestClientPaginationMalformedCursor(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	_, _, rawID := h.nextRequest(t)
	h.answerResult(t, rawID, `{"tools":[],"nextCursor":42}`)
	if err := awaitOp(t, ch); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("err = %v, want ErrInvalidMessage", err)
	}
}

func TestClientPaginationOversizedCursor(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	_, _, rawID := h.nextRequest(t)
	h.answerResult(t, rawID, listPage(nil, strings.Repeat("c", MaxCursorBytes+1)))
	if err := awaitOp(t, ch); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestClientPaginationRPCError(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	_, _, rawID := h.nextRequest(t)
	h.answerError(t, rawID, -32000, "list blew up")
	err := awaitOp(t, ch)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %v, want *RPCError preserved", err)
	}
	if rpcErr.Code != -32000 {
		t.Errorf("code = %d", rpcErr.Code)
	}
}

func TestClientToolAccumulationBounded(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	pages := 0
	for {
		_, _, rawID := h.nextRequest(t)
		pages++
		var entries []string
		for i := 0; i < 50; i++ {
			entries = append(entries, toolEntry(fmt.Sprintf("tool-%d-%d", pages, i), "d", `{"type":"object"}`))
		}
		cursor := ""
		if pages < 3 {
			cursor = fmt.Sprintf("c%d", pages)
		}
		h.answerResult(t, rawID, listPage(entries, cursor))
		if pages == 3 {
			break
		}
	}
	// 150 valid tools exceed the 128 cap: discovery fails, nothing usable.
	if err := awaitOp(t, ch); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if _, err := h.cl.Tools(); err == nil {
		t.Error("Tools usable after over-cap failure")
	}
}

func TestClientWarningCap(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	_, _, rawID := h.nextRequest(t)
	var entries []string
	for i := 0; i < MaxDiscoveryWarnings+8; i++ {
		entries = append(entries, `{"name":""}`)
	}
	h.answerResult(t, rawID, listPage(entries, ""))
	if err := awaitOp(t, ch); err != nil {
		t.Fatalf("Discover error = %v", err)
	}
	warnings, _ := h.cl.Warnings()
	if len(warnings) != MaxDiscoveryWarnings {
		t.Errorf("warnings = %d, want capped %d", len(warnings), MaxDiscoveryWarnings)
	}
	if n, _ := h.cl.Skipped(); n != MaxDiscoveryWarnings+8 {
		t.Errorf("skipped = %d, want full count", n)
	}
}

func TestClientShutdownBeforeInit(t *testing.T) {
	h := newClientHarness(t, "srv")
	if err := h.cl.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	if err := h.cl.Initialize(context.Background()); !errors.Is(err, ErrClosed) {
		t.Errorf("init err = %v, want ErrClosed", err)
	}
	if err := h.cl.Discover(context.Background()); !errors.Is(err, ErrClosed) {
		t.Errorf("discover err = %v, want ErrClosed", err)
	}
	if h.cl.State() != StateClosed {
		t.Errorf("state = %v, want closed", h.cl.State())
	}
}

func TestClientShutdownAfterReady(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	_, _, rawID := h.nextRequest(t)
	h.answerResult(t, rawID, listPage([]string{toolEntry("t", "d", `{"type":"object"}`)}, ""))
	if err := awaitOp(t, ch); err != nil {
		t.Fatalf("Discover error = %v", err)
	}
	if err := h.cl.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	if err := h.cl.Close(); err != nil {
		t.Fatalf("second Close error = %v, want idempotent nil", err)
	}
	if h.cl.Ready() {
		t.Error("Ready after close")
	}
	if _, err := h.cl.Tools(); !errors.Is(err, ErrClosed) {
		t.Errorf("tools err = %v, want ErrClosed", err)
	}
}

func TestClientTransportFailureAfterReady(t *testing.T) {
	h := newClientHarness(t, "srv")
	succeedInitialize(t, h)
	ch := startOp(func() error { return h.cl.Discover(context.Background()) })
	_, _, rawID := h.nextRequest(t)
	h.answerResult(t, rawID, listPage([]string{toolEntry("t", "d", `{"type":"object"}`)}, ""))
	if err := awaitOp(t, ch); err != nil {
		t.Fatalf("Discover error = %v", err)
	}
	_ = h.fromW.Close() // peer EOF: transport dies with no reconnect
	select {
	case <-h.tr.Done():
	case <-time.After(clientHarnessTimeout):
		t.Fatal("reader did not exit on EOF")
	}
	if h.cl.Ready() {
		t.Error("Ready over a dead transport; must report unready with no reconnect")
	}
	if _, err := h.cl.Tools(); !errors.Is(err, ErrTransport) {
		t.Errorf("tools err = %v, want fail-fast ErrTransport", err)
	}
}

func TestClientOpsAfterFailure(t *testing.T) {
	h := newClientHarness(t, "srv")
	_ = driveInitialize(t, h, func(rawID json.RawMessage) {
		h.answerResult(t, rawID, initResult("nope", `{"tools":{}}`))
	})
	if err := h.cl.Discover(context.Background()); err == nil {
		t.Error("Discover after failed init accepted")
	}
	if _, err := h.cl.Tools(); err == nil {
		t.Error("Tools after failed init accepted")
	}
	if err := h.cl.Close(); err != nil {
		t.Errorf("Close after failure err = %v, want nil", err)
	}
}

func TestClientSentinelTable(t *testing.T) {
	build := func(t *testing.T, answer string, isErr bool, code int) error {
		h := newClientHarness(t, "srv")
		if isErr {
			return driveInitialize(t, h, func(rawID json.RawMessage) {
				h.answerError(t, rawID, code, "x")
			})
		}
		return driveInitialize(t, h, func(rawID json.RawMessage) {
			h.answerResult(t, rawID, answer)
		})
	}
	if err := build(t, initResult("2100-01-01", `{"tools":{}}`), false, 0); !errors.Is(err, ErrUnsupportedVersion) {
		t.Errorf("version err = %v", err)
	}
	if err := build(t, initResult(ClientVersion, `{"nope":{}}`), false, 0); !errors.Is(err, ErrProtocol) {
		t.Errorf("capability err = %v", err)
	}
	if err := build(t, `[]`, false, 0); !errors.Is(err, ErrInvalidMessage) {
		t.Errorf("malformed err = %v", err)
	}
	var rpcErr *RPCError
	if err := build(t, "", true, -32602); !errors.As(err, &rpcErr) {
		t.Errorf("rpc err = %v, want *RPCError", err)
	}
}

func TestClientBoundedServerErrorDetail(t *testing.T) {
	h := newClientHarness(t, "srv")
	err := driveInitialize(t, h, func(rawID json.RawMessage) {
		h.answerError(t, rawID, -32000, strings.Repeat("e", MaxErrorDetailRunes*4))
	})
	if err == nil {
		t.Fatal("nil error")
	}
	if len([]rune(err.Error())) > MaxErrorDetailRunes+128 {
		t.Errorf("error of %d runes is unbounded", len([]rune(err.Error())))
	}
}

func TestClientConstructorRejects(t *testing.T) {
	tr, err := NewTransport(strings.NewReader(""), io.Discard)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	defer func() { _ = tr.Close() }()
	if _, err := NewClient(nil, "srv", DefaultClientInfo()); err == nil {
		t.Error("nil transport accepted")
	}
	if _, err := NewClient(tr, "bad key!", DefaultClientInfo()); err == nil {
		t.Error("bad server key accepted")
	}
	if _, err := NewClient(tr, "srv", Implementation{}); err == nil {
		t.Error("empty client name accepted")
	}
}

func TestClientStateNames(t *testing.T) {
	for s, want := range map[ClientState]string{
		StateNew: "new", StateInitializing: "initializing",
		StateInitialized: "initialized", StateDiscovering: "discovering",
		StateReady: "ready", StateFailed: "failed", StateClosed: "closed",
	} {
		if s.String() != want {
			t.Errorf("state %d = %q, want %q", int(s), s.String(), want)
		}
	}
	if (ClientState(999)).String() != "unknown" {
		t.Error("out-of-range state must render unknown, never panic")
	}
}

func TestClientDefaults(t *testing.T) {
	info := DefaultClientInfo()
	if info.Name != "forcefield" || info.Version == "" {
		t.Errorf("default info = %+v, want forcefield + stamped-or-dev version", info)
	}
}
