package runtime

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/sandbox"
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

// scheduler runs independent tool calls concurrently and centralizes
// permission checks.
type scheduler struct {
	manager     *tools.Manager
	permissions *permissions.Manager
	asker       permissions.Asker
	cfg         SchedulerConfig
}

func newScheduler(manager *tools.Manager, perms *permissions.Manager, asker permissions.Asker, cfg SchedulerConfig) *scheduler {
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 1
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = 200 * time.Millisecond
	}
	return &scheduler{manager: manager, permissions: perms, asker: asker, cfg: cfg}
}

// Run executes calls concurrently and returns results in call order.
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

// runOne executes one tool call with retries, timeouts, and progress events.
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

	if denied, result := s.checkPermission(ctx, call, emit); denied {
		return *result
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
				ToolCallID:  call.ID,
				Name:        call.Name,
				Arguments:   call.Arguments,
				Content:     res.Content,
				IsError:     res.IsError,
				Success:     !res.IsError,
				Duration:    time.Since(started),
				Attempt:     attempt,
				Stdout:      res.Stdout,
				Stderr:      res.Stderr,
				ExitCode:    res.ExitCode,
				HasExitCode: res.ExitCode != 0 || res.Tool == "shell",
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

// checkPermission resolves a tool call's permission before execution.
func (s *scheduler) checkPermission(ctx context.Context, call providers.ToolCall, emit func(Event) bool) (denied bool, result *ToolResult) {
	if s.permissions == nil {
		return false, nil // no permission manager configured: fail open
	}

	decision := s.permissions.Check(call.Name)

	if decision == permissions.Allow {
		return false, nil
	}

	if decision == permissions.Ask {
		resolved, err := s.resolveAsk(ctx, call)
		if err != nil {
			result := s.deniedResult(call, fmt.Sprintf("permission prompt failed: %v", err))
			emit(Event{Type: EventToolDenied, ToolResult: result})
			return true, result
		}
		if resolved == permissions.Allow {
			return false, nil
		}
	}

	// decision == permissions.Deny, or an "ask" that resolved to deny.
	result = s.deniedResult(call, fmt.Sprintf("permission denied for tool %q", call.Name))
	emit(Event{Type: EventToolDenied, ToolResult: result})
	return true, result
}

// executionEnforcementSource is implemented by tools whose process
// execution has a policy story (the shell tool, via its sandbox
// executor). The scheduler consults it so permission requests can state
// exactly what will run and under what boundary.
type executionEnforcementSource interface {
	ExecutionEnforcement(ctx context.Context) (sandbox.Enforcement, bool)
}

// resolveAsk prompts for a decision and persists "always" responses.
func (s *scheduler) resolveAsk(ctx context.Context, call providers.ToolCall) (permissions.Decision, error) {
	if s.asker == nil {
		// No interactive surface available (e.g. non-interactive
		// automation): fail closed rather than silently executing.
		return permissions.Deny, fmt.Errorf("tool %q requires approval but no permission prompt is configured", call.Name)
	}

	req := permissions.Request{Tool: call.Name, Arguments: call.Arguments}
	if src, ok := s.lookupEnforcementSource(call.Name); ok {
		if e, ok := src.ExecutionEnforcement(ctx); ok {
			req.Execution = &e
		}
	}

	prompt, err := s.asker.Ask(ctx, req)
	if err != nil {
		return permissions.Deny, err
	}

	if prompt.Persist() {
		if err := s.permissions.Update(call.Name, prompt.Decision()); err != nil {
			// The in-memory decision below still applies to this call;
			// only persistence failed. Surface it via the returned error
			// is tempting but would incorrectly deny this call, so it's
			// swallowed here - a future "/permissions" view is the right
			// place to report it.
			_ = err
		}
	}

	return prompt.Decision(), nil
}

func (s *scheduler) deniedResult(call providers.ToolCall, reason string) *ToolResult {
	result := &ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		Arguments:  call.Arguments,
		Success:    false,
		IsError:    true,
		Content:    reason,
	}
	return result
}

// lookupEnforcementSource resolves the registered tool implementing the
// enforcement-source interface, if any.
func (s *scheduler) lookupEnforcementSource(name string) (executionEnforcementSource, bool) {
	tool, ok := s.manager.Lookup(name)
	if !ok {
		return nil, false
	}
	src, ok := tool.(executionEnforcementSource)
	return src, ok
}

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
