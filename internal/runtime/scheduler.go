package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// SchedulerConfig controls how the scheduler runs a batch of tool calls.
type SchedulerConfig struct {
	// MaxConcurrency caps how many tool calls run at once. Values <= 0 are
	// treated as 1 (fully sequential).
	MaxConcurrency int
	// MaxRetries is how many additional attempts a retryable failure gets,
	// on top of the first attempt.
	MaxRetries int
	// BaseBackoff is the delay before the first retry; each subsequent
	// retry doubles it (exponential backoff).
	BaseBackoff time.Duration
}

// DefaultSchedulerConfig is used when the runtime isn't configured
// otherwise: up to 4 tools in flight at once, 2 retries for transient
// failures, starting at a 200ms backoff.
var DefaultSchedulerConfig = SchedulerConfig{
	MaxConcurrency: 4,
	MaxRetries:     2,
	BaseBackoff:    200 * time.Millisecond,
}

// scheduler executes a batch of independent tool calls - a single model
// turn's worth - concurrently, aggregating their results before they're
// handed back to the model. Calls within a batch are assumed independent;
// ordering *between* batches (i.e. between model turns) is preserved
// naturally because the runtime only asks for the next batch once the
// previous one has fully finished.
type scheduler struct {
	manager *tools.Manager
	cfg     SchedulerConfig
}

func newScheduler(manager *tools.Manager, cfg SchedulerConfig) *scheduler {
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 1
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = 200 * time.Millisecond
	}
	return &scheduler{manager: manager, cfg: cfg}
}

// Run executes calls with up to cfg.MaxConcurrency running at once and
// returns their results in the same order as calls, regardless of
// completion order, so callers can append them to conversation history
// deterministically. A failure in one call never cancels its siblings;
// only ctx being cancelled (e.g. the user pressing Ctrl+C) stops the whole
// batch early. emit is called from multiple goroutines and is
// synchronized internally, so callers don't need to worry about
// concurrent Event delivery.
func (s *scheduler) Run(ctx context.Context, calls []providers.ToolCall, emit func(Event) bool) []ToolResult {
	results := make([]ToolResult, len(calls))
	if len(calls) == 0 {
		return results
	}

	var emitMu sync.Mutex
	safeEmit := func(e Event) bool {
		emitMu.Lock()
		defer emitMu.Unlock()
		return emit(e)
	}

	sem := make(chan struct{}, s.cfg.MaxConcurrency)
	var wg sync.WaitGroup
	var stopped atomic.Bool

	for i, call := range calls {
		i, call := i, call

		wg.Add(1)
		go func() {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = s.cancelledResult(call)
				return
			}
			defer func() { <-sem }()

			if stopped.Load() || ctx.Err() != nil {
				results[i] = s.cancelledResult(call)
				return
			}

			result := s.runOne(ctx, call, safeEmit)
			results[i] = result
			if ctx.Err() != nil {
				stopped.Store(true)
			}
		}()
	}

	wg.Wait()
	return results
}

// runOne executes a single tool call to completion, including retries,
// timeout enforcement, and streaming progress events. It never panics the
// caller: lookup failures and execution errors are both turned into a
// failed ToolResult.
func (s *scheduler) runOne(ctx context.Context, call providers.ToolCall, emit func(Event) bool) ToolResult {
	started := time.Now()
	emit(Event{Type: EventToolStart, ToolCall: &call})

	tool, ok := s.manager.Lookup(call.Name)
	if !ok {
		result := ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			Arguments:  call.Arguments,
			Success:    false,
			IsError:    true,
			Content:    "tool not found: " + call.Name,
			Duration:   time.Since(started),
			Attempt:    1,
			Err:        tools.ErrNotFound,
		}
		emit(Event{Type: EventToolFailed, ToolResult: &result})
		return result
	}

	meta := tools.MetadataOf(tool)

	onChunk := func(chunk tools.StreamChunk) {
		emit(Event{Type: EventToolProgress, ToolProgress: &ToolProgress{
			ToolCallID: call.ID,
			Name:       call.Name,
			Stream:     chunk.Stream,
			Data:       chunk.Data,
		}})
	}

	maxAttempts := 1
	if meta.Retryable {
		maxAttempts += s.cfg.MaxRetries
	}

	var last tools.Result
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		callCtx := ctx
		var cancel context.CancelFunc
		if meta.Timeout > 0 {
			callCtx, cancel = context.WithTimeout(ctx, meta.Timeout)
		}

		res, err := execute(callCtx, tool, call.Arguments, onChunk)
		if cancel != nil {
			cancel()
		}
		last, lastErr = res, err

		if ctx.Err() != nil {
			// Parent cancellation (Ctrl+C, whole-run timeout): stop
			// retrying and report cancellation, not failure.
			result := ToolResult{
				ToolCallID: call.ID,
				Name:       call.Name,
				Arguments:  call.Arguments,
				Success:    false,
				IsError:    true,
				Content:    "tool execution cancelled",
				Duration:   time.Since(started),
				Attempt:    attempt,
				Err:        ctx.Err(),
			}
			emit(Event{Type: EventToolCancelled, ToolResult: &result})
			return result
		}

		if err == nil {
			// Tool ran to completion; res.IsError (a tool-reported,
			// deterministic failure, e.g. "file not found") is never
			// retried even if the tool is marked retryable.
			result := ToolResult{
				ToolCallID: call.ID,
				Name:       call.Name,
				Arguments:  call.Arguments,
				Content:    res.Content,
				IsError:    res.IsError,
				Success:    !res.IsError,
				Duration:   time.Since(started),
				Attempt:    attempt,
			}
			if res.IsError {
				emit(Event{Type: EventToolFailed, ToolResult: &result})
			} else {
				emit(Event{Type: EventToolFinish, ToolResult: &result})
			}
			return result
		}

		// err != nil: an execution-level failure (process couldn't start,
		// transient I/O error, etc). Retry only if the tool opted in and
		// attempts remain.
		if !meta.Retryable || attempt == maxAttempts {
			break
		}

		backoff := s.cfg.BaseBackoff * time.Duration(1<<uint(attempt-1))
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			result := ToolResult{
				ToolCallID: call.ID,
				Name:       call.Name,
				Arguments:  call.Arguments,
				Success:    false,
				IsError:    true,
				Content:    "tool execution cancelled",
				Duration:   time.Since(started),
				Attempt:    attempt,
				Err:        ctx.Err(),
			}
			emit(Event{Type: EventToolCancelled, ToolResult: &result})
			return result
		}
	}

	content := last.Content
	if content == "" && lastErr != nil {
		content = lastErr.Error()
	}
	result := ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		Arguments:  call.Arguments,
		Content:    content,
		IsError:    true,
		Success:    false,
		Duration:   time.Since(started),
		Attempt:    maxAttempts,
		Err:        lastErr,
	}
	emit(Event{Type: EventToolFailed, ToolResult: &result})
	return result
}

// execute runs tool, preferring its streaming path when available so
// callers get live progress events; otherwise it falls back to the plain
// Execute method.
func execute(ctx context.Context, tool tools.Tool, args map[string]any, onChunk func(tools.StreamChunk)) (tools.Result, error) {
	if st, ok := tool.(tools.StreamingTool); ok {
		return st.ExecuteStream(ctx, args, onChunk)
	}
	return tool.Execute(ctx, args)
}

func (s *scheduler) cancelledResult(call providers.ToolCall) ToolResult {
	return ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		Arguments:  call.Arguments,
		Success:    false,
		IsError:    true,
		Content:    "tool execution cancelled before it started",
		Attempt:    0,
		Err:        context.Canceled,
	}
}
