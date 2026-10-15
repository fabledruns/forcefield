package providers

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"forcefield/internal/tools"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtdoc "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithy "github.com/aws/smithy-go"
)

// fakeBedrockStream feeds scripted Converse events without AWS or network.
type fakeBedrockStream struct {
	events []brtypes.ConverseStreamOutput
	err    error
	closed atomic.Bool
	once   sync.Once
	ch     chan brtypes.ConverseStreamOutput
}

func (f *fakeBedrockStream) Events() <-chan brtypes.ConverseStreamOutput {
	f.once.Do(func() {
		f.ch = make(chan brtypes.ConverseStreamOutput, len(f.events))
		for _, ev := range f.events {
			f.ch <- ev
		}
		close(f.ch)
	})
	return f.ch
}

func (f *fakeBedrockStream) Close() error {
	f.closed.Store(true)
	return nil
}

func (f *fakeBedrockStream) Err() error { return f.err }

// fakeBedrockClient records inputs and replays scripted outputs.
type fakeBedrockClient struct {
	converseOut   *bedrockruntime.ConverseOutput
	converseErr   error
	stream        bedrockStream
	streamErr     error
	streamCalls   atomic.Int64
	gotModel      string
	gotMessages   int
	gotSystem     int
	gotToolConfig bool
}

func (f *fakeBedrockClient) Converse(_ context.Context, in *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	if in.ModelId != nil {
		f.gotModel = *in.ModelId
	}
	f.gotMessages = len(in.Messages)
	f.gotSystem = len(in.System)
	f.gotToolConfig = in.ToolConfig != nil
	if f.converseErr != nil {
		return nil, f.converseErr
	}
	return f.converseOut, nil
}

func (f *fakeBedrockClient) ConverseStream(_ context.Context, in *bedrockruntime.ConverseStreamInput, _ ...func(*bedrockruntime.Options)) (bedrockStream, error) {
	f.streamCalls.Add(1)
	if in.ModelId != nil {
		f.gotModel = *in.ModelId
	}
	f.gotMessages = len(in.Messages)
	f.gotSystem = len(in.System)
	f.gotToolConfig = in.ToolConfig != nil
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	return f.stream, nil
}

func bedrockTestProvider(fake *fakeBedrockClient) *BedrockProvider {
	p := NewBedrockProvider(Spec{
		ID: "btest", Type: "bedrock", Label: "Bedrock Test",
		Region: "us-east-1", Model: "test-model",
	})
	p.newClient = func(context.Context, Spec) (bedrockConverseClient, error) { return fake, nil }
	p.retry = fastRetry
	return p
}

func strptr(s string) *string { return &s }
func i32ptr(v int32) *int32   { return &v }

func textDeltaEvent(idx int32, text string) brtypes.ConverseStreamOutput {
	return &brtypes.ConverseStreamOutputMemberContentBlockDelta{Value: brtypes.ContentBlockDeltaEvent{
		ContentBlockIndex: &idx,
		Delta:             &brtypes.ContentBlockDeltaMemberText{Value: text},
	}}
}

func toolStartEvent(idx int32, id, name string) brtypes.ConverseStreamOutput {
	return &brtypes.ConverseStreamOutputMemberContentBlockStart{Value: brtypes.ContentBlockStartEvent{
		ContentBlockIndex: &idx,
		Start:             &brtypes.ContentBlockStartMemberToolUse{Value: brtypes.ToolUseBlockStart{ToolUseId: &id, Name: &name}},
	}}
}

func toolDeltaEvent(idx int32, fragment string) brtypes.ConverseStreamOutput {
	return &brtypes.ConverseStreamOutputMemberContentBlockDelta{Value: brtypes.ContentBlockDeltaEvent{
		ContentBlockIndex: &idx,
		Delta:             &brtypes.ContentBlockDeltaMemberToolUse{Value: brtypes.ToolUseBlockDelta{Input: &fragment}},
	}}
}

func stopEvent(reason brtypes.StopReason) brtypes.ConverseStreamOutput {
	return &brtypes.ConverseStreamOutputMemberMessageStop{Value: brtypes.MessageStopEvent{StopReason: reason}}
}

func metadataEvent(in, out, total int32) brtypes.ConverseStreamOutput {
	return &brtypes.ConverseStreamOutputMemberMetadata{Value: brtypes.ConverseStreamMetadataEvent{
		Usage: &brtypes.TokenUsage{InputTokens: &in, OutputTokens: &out, TotalTokens: &total},
	}}
}

func TestBedrockStreamTextToolUsage(t *testing.T) {
	fake := &fakeBedrockClient{stream: &fakeBedrockStream{events: []brtypes.ConverseStreamOutput{
		textDeltaEvent(0, "hello "),
		textDeltaEvent(0, "world"),
		toolStartEvent(1, "call-1", "list"),
		toolDeltaEvent(1, `{"path":`),
		toolDeltaEvent(1, `"."}`),
		stopEvent(brtypes.StopReasonToolUse),
		metadataEvent(10, 5, 15),
	}}}
	p := bedrockTestProvider(fake)

	stream, err := p.StreamChat(context.Background(),
		[]Message{{Role: SystemRole, Content: "be brief"}, {Role: UserRole, Content: "hi"}},
		[]tools.Definition{{Name: "list", Description: "List.", InputSchema: map[string]any{"type": "object"}}},
	)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	events := drainEvents(t, stream)

	if fake.gotModel != "test-model" {
		t.Errorf("model = %q, want test-model", fake.gotModel)
	}
	if fake.gotSystem != 1 || fake.gotMessages != 1 || !fake.gotToolConfig {
		t.Errorf("system=%d messages=%d tools=%v, want 1/1/true", fake.gotSystem, fake.gotMessages, fake.gotToolConfig)
	}
	var text string
	var toolsSeen int
	var done FinishReason
	var usage *Usage
	for _, e := range events {
		if e.Err != nil {
			t.Fatalf("stream error: %v", e.Err)
		}
		text += e.Text
		if len(e.ToolCalls) > 0 {
			toolsSeen = len(e.ToolCalls)
			if e.ToolCalls[0].ID != "call-1" || e.ToolCalls[0].Name != "list" || e.ToolCalls[0].Arguments["path"] != "." {
				t.Errorf("tool call = %#v, want reassembled list call", e.ToolCalls[0])
			}
		}
		if e.Done {
			done = e.StopReason
			usage = e.Usage
		}
	}
	if text != "hello world" || toolsSeen != 1 || done != FinishToolCalls {
		t.Errorf("text=%q tools=%d done=%q, want hello world/1/tool_calls", text, toolsSeen, done)
	}
	if usage == nil || usage.PromptTokens != 10 || usage.CompletionTokens != 5 || usage.TotalTokens != 15 {
		t.Errorf("usage = %#v, want 10/5/15", usage)
	}
}

func TestBedrockStopReasonMapping(t *testing.T) {
	cases := []struct {
		reason brtypes.StopReason
		want   FinishReason
	}{
		{brtypes.StopReasonEndTurn, FinishStop},
		{brtypes.StopReasonToolUse, FinishToolCalls},
		{brtypes.StopReasonMaxTokens, FinishLength},
		{brtypes.StopReasonModelContextWindowExceeded, FinishLength},
		{brtypes.StopReasonStopSequence, FinishStop},
		{brtypes.StopReasonContentFiltered, FinishStop},
		{brtypes.StopReasonGuardrailIntervened, FinishStop},
	}
	for _, tc := range cases {
		if got := bedrockStopReasonFor(tc.reason); got != tc.want {
			t.Errorf("bedrockStopReasonFor(%q) = %q, want %q", tc.reason, got, tc.want)
		}
	}
}

func TestBedrockComplete(t *testing.T) {
	fake := &fakeBedrockClient{converseOut: &bedrockruntime.ConverseOutput{
		Output: &brtypes.ConverseOutputMemberMessage{Value: brtypes.Message{
			Role: brtypes.ConversationRoleAssistant,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberText{Value: "answer"},
			},
		}},
		StopReason: brtypes.StopReasonEndTurn,
		Usage:      &brtypes.TokenUsage{InputTokens: i32ptr(7), OutputTokens: i32ptr(3), TotalTokens: i32ptr(10)},
	}}
	p := bedrockTestProvider(fake)

	resp, err := p.Complete(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if resp.Content != "answer" || resp.StopReason != FinishStop || resp.Usage.TotalTokens != 10 {
		t.Errorf("resp = %+v, want answer/stop/10", resp)
	}
	if fake.gotToolConfig {
		t.Error("toolConfig sent with no defs, want nil")
	}
}

func TestBedrockCompleteToolCalls(t *testing.T) {
	fake := &fakeBedrockClient{converseOut: &bedrockruntime.ConverseOutput{
		Output: &brtypes.ConverseOutputMemberMessage{Value: brtypes.Message{
			Role: brtypes.ConversationRoleAssistant,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberToolUse{Value: brtypes.ToolUseBlock{
					ToolUseId: strptr("tu-1"), Name: strptr("read"),
					Input: fakeBedrockDoc(t, map[string]any{"file": "a.txt"}),
				}},
			},
		}},
		StopReason: brtypes.StopReasonToolUse,
		Usage:      &brtypes.TokenUsage{InputTokens: i32ptr(1), OutputTokens: i32ptr(2), TotalTokens: i32ptr(3)},
	}}
	p := bedrockTestProvider(fake)

	resp, err := p.Complete(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "tu-1" || resp.ToolCalls[0].Arguments["file"] != "a.txt" {
		t.Errorf("tool calls = %#v, want tu-1 read a.txt", resp.ToolCalls)
	}
	if resp.StopReason != FinishToolCalls {
		t.Errorf("stop = %q, want tool_calls", resp.StopReason)
	}
}

// fakeBedrockDoc builds a response document through the real SDK lazy
// document type, exactly as request/response documents are represented.
func fakeBedrockDoc(t *testing.T, m map[string]any) brtdoc.Interface {
	t.Helper()
	return brtdoc.NewLazyDocument(m)
}

// smithyAPIError is a test APIError carrying a code, message, and fault.
type smithyAPIError struct {
	code, message string
	fault         smithy.ErrorFault
}

func (e *smithyAPIError) Error() string                 { return e.code + ": " + e.message }
func (e *smithyAPIError) ErrorCode() string             { return e.code }
func (e *smithyAPIError) ErrorMessage() string          { return e.message }
func (e *smithyAPIError) ErrorFault() smithy.ErrorFault { return e.fault }

func TestBedrockErrorMapping(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		want      ErrorKind
		retryable bool
	}{
		{"throttle", &smithyAPIError{"ThrottlingException", "slow", smithy.FaultClient}, ErrKindRateLimit, true},
		{"model timeout", &smithyAPIError{"ModelTimeoutException", "slow model", smithy.FaultServer}, ErrKindTimeout, true},
		{"internal", &smithyAPIError{"InternalServerException", "boom", smithy.FaultServer}, ErrKindServer, true},
		{"unavailable", &smithyAPIError{"ServiceUnavailableException", "down", smithy.FaultServer}, ErrKindServer, true},
		{"stream error", &smithyAPIError{"ModelStreamErrorException", "mid", smithy.FaultServer}, ErrKindServer, true},
		{"not ready", &smithyAPIError{"ModelNotReadyException", "warming", smithy.FaultServer}, ErrKindServer, true},
		{"access denied", &smithyAPIError{"AccessDeniedException", "denied", smithy.FaultClient}, ErrKindAuth, false},
		{"expired token", &smithyAPIError{"ExpiredTokenException", "expired", smithy.FaultClient}, ErrKindAuth, false},
		{"validation", &smithyAPIError{"ValidationException", "bad", smithy.FaultClient}, ErrKindInvalidRequest, false},
		{"not found", &smithyAPIError{"ResourceNotFoundException", "no model", smithy.FaultClient}, ErrKindNotFound, false},
		{"quota code", &smithyAPIError{"ServiceQuotaExceededException", "quota", smithy.FaultClient}, ErrKindQuota, false},
		{"quota wording", &smithyAPIError{"ThrottlingException", "quota exceeded for account", smithy.FaultClient}, ErrKindQuota, false},
		{"unknown server fault", &smithyAPIError{"WeirdException", "x", smithy.FaultServer}, ErrKindServer, true},
		{"unknown client fault", &smithyAPIError{"WeirdException", "x", smithy.FaultClient}, ErrKindInvalidRequest, false},
		{"canceled", context.Canceled, ErrKindCanceled, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, retryable, _ := bedrockErrorInfo(tc.err)
			if kind != tc.want || retryable != tc.retryable {
				t.Errorf("bedrockErrorInfo() = (%v,%v), want (%v,%v)", kind, retryable, tc.want, tc.retryable)
			}
		})
	}
}

func TestBedrockRetryThenSucceeds(t *testing.T) {
	throttle := &smithyAPIError{"ThrottlingException", "slow", smithy.FaultClient}
	fake := &fakeBedrockClient{streamErr: throttle}
	p := bedrockTestProvider(fake)
	p.retry = retryPolicy{MaxRetries: 1, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond, MaxRetryAfter: time.Millisecond}

	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded, want rate-limit error")
	}
	if got := Classify(err); got != ErrKindRateLimit {
		t.Errorf("Classify() = %v, want RateLimit", got)
	}
	if fake.streamCalls.Load() != 2 {
		t.Errorf("calls = %d, want 2 (initial + 1 retry)", fake.streamCalls.Load())
	}

	// Quota never retries.
	fakeQuota := &fakeBedrockClient{streamErr: &smithyAPIError{"ServiceQuotaExceededException", "quota", smithy.FaultClient}}
	pq := bedrockTestProvider(fakeQuota)
	pq.retry = p.retry
	if _, err := pq.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil); err == nil {
		t.Fatal("quota StreamChat() succeeded")
	} else if got := Classify(err); got != ErrKindQuota {
		t.Errorf("Classify() = %v, want Quota", got)
	}
	if fakeQuota.streamCalls.Load() != 1 {
		t.Errorf("quota calls = %d, want 1 (no retry)", fakeQuota.streamCalls.Load())
	}
}

func TestBedrockMalformedToolJSONIsProtocolError(t *testing.T) {
	fake := &fakeBedrockClient{stream: &fakeBedrockStream{events: []brtypes.ConverseStreamOutput{
		toolStartEvent(0, "c1", "x"),
		toolDeltaEvent(0, "{oops"),
		stopEvent(brtypes.StopReasonToolUse),
	}}}
	p := bedrockTestProvider(fake)

	events := drainEvents(t, mustStream(t, p))
	found := false
	for _, e := range events {
		if e.Err != nil {
			found = true
			if Classify(e.Err) != ErrKindProtocol {
				t.Errorf("kind = %v, want Protocol", Classify(e.Err))
			}
		}
	}
	if !found {
		t.Fatal("no error event for malformed tool JSON")
	}
}

func TestBedrockTruncatedStreamIsError(t *testing.T) {
	fake := &fakeBedrockClient{stream: &fakeBedrockStream{events: []brtypes.ConverseStreamOutput{
		textDeltaEvent(0, "partial"),
	}}}
	p := bedrockTestProvider(fake)

	events := drainEvents(t, mustStream(t, p))
	last := events[len(events)-1]
	if last.Err == nil {
		t.Fatal("truncated stream reported no error")
	}
	if Classify(last.Err) != ErrKindProtocol {
		t.Errorf("kind = %v, want Protocol", Classify(last.Err))
	}
}

func TestBedrockStreamErrorEvent(t *testing.T) {
	fake := &fakeBedrockClient{stream: &fakeBedrockStream{
		events: []brtypes.ConverseStreamOutput{textDeltaEvent(0, "partial")},
		err:    &smithyAPIError{"ModelStreamErrorException", "mid-stream failure", smithy.FaultServer},
	}}
	p := bedrockTestProvider(fake)

	events := drainEvents(t, mustStream(t, p))
	found := false
	for _, e := range events {
		if e.Err != nil {
			found = true
			if Classify(e.Err) != ErrKindServer {
				t.Errorf("kind = %v, want Server", Classify(e.Err))
			}
		}
	}
	if !found {
		t.Fatal("no error event for stream failure")
	}
}

func TestBedrockCancellationSilent(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	fake := &fakeBedrockClient{stream: &hangingBedrockStream{block: block}}
	p := bedrockTestProvider(fake)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := p.StreamChat(ctx, nil, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	cancel()
	for event := range stream {
		if event.Err != nil {
			t.Fatalf("error after cancel: %v", event.Err)
		}
		if event.Done {
			t.Fatal("Done after cancel")
		}
	}
}

// hangingBedrockStream blocks until the test releases it, exercising
// cancellation mid-stream.
type hangingBedrockStream struct{ block chan struct{} }

func (h *hangingBedrockStream) Events() <-chan brtypes.ConverseStreamOutput {
	ch := make(chan brtypes.ConverseStreamOutput)
	go func() {
		<-h.block
		close(ch)
	}()
	return ch
}

func (h *hangingBedrockStream) Close() error { return nil }
func (h *hangingBedrockStream) Err() error   { return nil }

func TestBedrockCapabilities(t *testing.T) {
	p := NewBedrockProvider(Spec{ID: "b", Type: "bedrock", Region: "us-east-1", Model: "m"})
	caps := p.Capabilities()
	if !caps.Streaming || !caps.ToolCalling {
		t.Errorf("caps = %+v, want streaming+tools", caps)
	}
	if caps.ParallelToolCalls {
		t.Errorf("caps = %+v, want sequential tool batches until verified", caps)
	}
	if got := CapabilitiesFor("bedrock"); !got.Streaming {
		t.Errorf("CapabilitiesFor(bedrock) = %+v, want streaming", got)
	}
}

func TestBedrockToolResultGrouping(t *testing.T) {
	system, msgs, err := toBedrockMessages([]Message{
		{Role: AssistantRole, ToolCalls: []ToolCall{{ID: "a", Name: "x"}}},
		{Role: ToolRole, ToolCallID: "a", Name: "x", Content: "1"},
		{Role: ToolRole, ToolCallID: "b", Name: "y", Content: "2"},
		{Role: UserRole, Content: "next"},
	})
	if err != nil {
		t.Fatalf("toBedrockMessages error = %v", err)
	}
	if len(system) != 0 || len(msgs) != 3 {
		t.Fatalf("msgs = %d, want 3 (assistant, grouped results, user)", len(msgs))
	}
	if len(msgs[1].Content) != 2 {
		t.Errorf("grouped results = %d blocks, want 2", len(msgs[1].Content))
	}
}

func TestBedrockMissingRegionAtRequest(t *testing.T) {
	p := NewBedrockProvider(Spec{ID: "b", Type: "bedrock", Model: "m"})
	p.newClient = func(context.Context, Spec) (bedrockConverseClient, error) {
		return defaultBedrockClient(context.Background(), Spec{ID: "b", Type: "bedrock", Model: "m"})
	}
	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded without region")
	}
	if !strings.Contains(err.Error(), "region") {
		t.Errorf("error = %q, want it to name region", err)
	}
}

func TestBedrockRedactsNothingSensitive(t *testing.T) {
	// Bedrock sends no long-lived secret on the wire (SigV4 signs with
	// credentials); assert error paths carry no prompt content either.
	fake := &fakeBedrockClient{streamErr: &smithyAPIError{"ValidationException", "bad request shape", smithy.FaultClient}}
	p := bedrockTestProvider(fake)
	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "secret prompt"}}, nil)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if strings.Contains(err.Error(), "secret prompt") {
		t.Errorf("error echoed prompt content: %q", err)
	}
}
