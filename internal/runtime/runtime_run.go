package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"forcefield/internal/agent"
	"forcefield/internal/providers"
	"forcefield/internal/session"
	"forcefield/internal/task"
	"forcefield/internal/tools"
	"forcefield/internal/trace"

	"github.com/google/uuid"
)

// runSnapshot is one consistent view of the switchable run state. It is
// captured under RLock at StreamChat entry so SetAgent / SetModel /
// SetProvider take effect on the next StreamChat, never mid-turn.
type runSnapshot struct {
	agent         *agent.Agent
	manager       *tools.Manager
	provider      providers.ModelProvider
	scheduler     *scheduler
	limits        Limits
	contextBudget ContextBudget
	// caps holds the negotiated provider capabilities for this run
	// (transport features from the instance, limits from the model
	// table); capsReported distinguishes "known to lack" from
	// "too old to say" so unreported providers keep history.
	caps         providers.Capabilities
	capsReported bool
	authRequired bool
	authEnvVar   string
	providerName string
	modelName    string
	// mode selects the tool and prompt policy for this run (see
	// planmode.go). It is fixed at StreamChat entry, like the rest of
	// the snapshot, so a run can never change policy mid-turn.
	mode RunMode
}

// snapshotRunState copies the switchable run state under RLock.
func (r *Runtime) snapshotRunState() runSnapshot {
	var snap runSnapshot
	if r == nil {
		return snap
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	snap.agent = r.agent
	snap.manager = r.manager
	snap.provider = r.provider
	snap.scheduler = r.scheduler
	snap.limits = r.limits
	snap.authRequired = r.authRequired
	snap.authEnvVar = r.authEnvVar
	// Capabilities negotiate independently of configuration: even a
	// config-less runtime reports what its provider can do.
	modelName := ""
	if r.cfg != nil {
		snap.providerName = r.cfg.Model.Provider
		snap.modelName = r.cfg.Model.Name
		modelName = r.cfg.Model.Name
	}
	snap.caps = providers.ResolveCapabilities(r.provider, modelName)
	snap.capsReported = providers.ReportsCapabilities(r.provider)
	if r.cfg != nil {
		snap.limits = limitsForAgent(r.cfg, r.activeAgent, snap.limits)
		snap.contextBudget = contextBudgetFromConfig(r.cfg, modelName, r.activeAgent, snap.caps)
	} else {
		snap.contextBudget = DefaultContextBudget()
	}
	return snap
}

// StreamChat runs the agent loop and emits structured events.
func (r *Runtime) StreamChat(ctx context.Context, messages []providers.Message) (<-chan Event, error) {
	return r.StreamChatWithMode(ctx, messages, ModeChat)
}

// StreamChatWithMode runs the agent loop under one RunMode: ModeChat for
// normal turns, ModePlan for read-only /plan turns. Both modes share the
// same loop, scheduler, permissions, and cancellation; only the tool
// subset and the system-prompt overlay differ.
func (r *Runtime) StreamChatWithMode(ctx context.Context, messages []providers.Message, mode RunMode) (<-chan Event, error) {
	if ctx == nil {
		return nil, fmt.Errorf("stream context cannot be nil")
	}
	if r == nil {
		return nil, fmt.Errorf("runtime not available")
	}

	snap := r.snapshotRunState()
	snap.mode = mode
	if mode == ModePlan {
		pm, err := planManager(snap.manager)
		if err != nil {
			return nil, fmt.Errorf("build plan tool set: %w", err)
		}
		snap.manager = pm
	}
	initial := buildMessagesWithBudget(messages, snap.agent, snap.contextBudget)
	if overlay := mode.planOverlay(); overlay != "" &&
		len(initial) > 0 && initial[0].Role == providers.SystemRole {
		initial[0].Content += overlay
	}
	// Local execution trace (P1.11): one run handle for the whole
	// StreamChat invocation. Nil when disabled or unopenable — every
	// trace call below is nil-safe, so tracing can never fail the run.
	var tr *trace.Run
	if r.tracer.Enabled() {
		tr = r.tracer.StartRun(uuid.NewString(), trace.RunMeta{
			Provider:     snap.providerName,
			Model:        snap.modelName,
			Agent:        r.CurrentAgent(),
			MessageCount: len(messages),
		})
	}
	// One terminal event can remain buffered after an interactive consumer
	// has detached on Ctrl+C. That lets the run goroutine finish cleanup
	// instead of blocking forever trying to report cancellation.
	events := make(chan Event, 1)
	go func() {
		defer close(events)
		if tr != nil {
			defer tr.Close()
		}
		r.runMu.Lock()
		defer r.runMu.Unlock()
		r.run(ctx, initial, func(event Event) bool {
			if event.Type == EventCancelled {
				// Cancellation is terminal and should not block teardown if the
				// TUI retired this stream already. Normal events retain strict
				// backpressure so consumers observe them in order.
				select {
				case events <- event:
					traceEvent(tr, event)
					return true
				default:
					return false
				}
			}
			select {
			case events <- event:
				// Record what the consumer observed: dropped
				// (cancelled) events never happened from its view.
				traceEvent(tr, event)
				return true
			case <-ctx.Done():
				return false
			}
		}, snap, tr)
	}()

	return events, nil
}

// traceEvent translates one observed runtime event into the local
// execution trace. Text, thinking, and progress deltas are deliberately
// skipped: turn boundaries plus tool start/outcome pairs carry the
// diagnostic signal without per-token volume.
func traceEvent(tr *trace.Run, e Event) {
	if tr == nil {
		return
	}
	switch e.Type {
	case EventToolStart:
		if e.ToolCall != nil {
			tr.ToolCall(e.ToolCall.ID, e.ToolCall.Name, e.ToolCall.Arguments)
		}
	case EventToolFinish, EventToolFailed, EventToolCancelled, EventToolDenied:
		if e.ToolResult == nil {
			return
		}
		res := e.ToolResult
		var exit *int
		if res.HasExitCode {
			v := res.ExitCode
			exit = &v
		}
		errText := ""
		if res.Err != nil {
			errText = res.Err.Error()
		}
		tr.ToolResult(res.ToolCallID, res.Name, traceToolStatus(e.Type), res.Success, res.Duration, res.Attempt, exit, res.Content, errText)
	case EventDone:
		finalLen := 0
		if e.Response != nil {
			finalLen = len(e.Response.Content)
		}
		tr.RunDone(string(e.Status), finalLen)
	case EventError:
		tr.RunError(e.Err)
	case EventCancelled:
		tr.RunError(e.Err)
	case EventBlocked:
		tr.RunBlocked(e.Err)
	}
}

func traceToolStatus(t EventType) string {
	switch t {
	case EventToolFinish:
		return "finish"
	case EventToolFailed:
		return "failed"
	case EventToolCancelled:
		return "cancelled"
	case EventToolDenied:
		return "denied"
	default:
		return "unknown"
	}
}

// Stream is kept as a compatibility alias for StreamChat.
func (r *Runtime) Stream(ctx context.Context, messages []providers.Message) (<-chan Event, error) {
	return r.StreamChat(ctx, messages)
}

// Run executes the agent loop and returns its final response.
func (r *Runtime) Run(messages []providers.Message) (providers.Response, error) {
	return r.RunContext(context.Background(), messages)
}

// RunContext executes Run with caller-controlled cancellation.
func (r *Runtime) RunContext(ctx context.Context, messages []providers.Message) (providers.Response, error) {
	resp, _, err := r.RunContextWithStatus(ctx, messages)
	return resp, err
}

// RunContextWithStatus executes the agent loop like RunContext and also
// reports the terminal verification outcome. Callers that turn completion
// into a process exit code (ff run, supervisors) must use the status:
// only StatusVerified counts as success; StatusPartial (or any other
// non-verified Done status) must exit non-zero even though a response
// was produced.
func (r *Runtime) RunContextWithStatus(ctx context.Context, messages []providers.Message) (providers.Response, Status, error) {
	events, err := r.StreamChat(ctx, messages)
	if err != nil {
		return providers.Response{}, "", err
	}

	for event := range events {
		switch event.Type {
		case EventDone:
			if event.Response == nil {
				return providers.Response{}, event.Status, fmt.Errorf("runtime completed without a response")
			}
			return *event.Response, event.Status, nil
		case EventError:
			return providers.Response{}, "", event.Err
		case EventCancelled:
			if event.Err != nil {
				return providers.Response{}, "", event.Err
			}
			return providers.Response{}, "", context.Canceled
		case EventBlocked:
			return providers.Response{}, event.Status, fmt.Errorf("task blocked: %w", event.Err)
		}
	}

	if err := ctx.Err(); err != nil {
		return providers.Response{}, "", err
	}
	return providers.Response{}, "", fmt.Errorf("runtime stopped without a completion event")
}

// maxToolResultChars keeps verbose tool output from dominating later turns.
// It mirrors session.MaxModelToolResultChars, which the session layer
// applies when replaying persisted history, so a resumed run sees the
// identical model-visible window as the originating run.
const maxToolResultChars = session.MaxModelToolResultChars

// run executes the persistent agent loop and enforces runtime limits.
func (r *Runtime) run(ctx context.Context, messages []providers.Message, emit func(Event) bool, snap runSnapshot, tr *trace.Run) {
	state := task.New(goalFrom(messages))
	ctx = task.WithState(ctx, state)
	cleanup := tools.NewRunControl(ctx)
	ctx = tools.WithRunControl(ctx, cleanup)
	cancelled := func(err error) {
		cleanup.Wait()
		r.emitCancelled(emit, state, err)
	}
	// Orchestration panics surface as run errors with bounded cleanup
	// (see docs/Runtime.md).
	defer func() {
		if p := recover(); p != nil {
			waitDone := make(chan struct{})
			go func() { cleanup.Wait(); close(waitDone) }()
			select {
			case <-waitDone:
			case <-time.After(5 * time.Second):
			}
			func() {
				defer func() { _ = recover() }()
				emit(Event{Type: EventError, Err: fmt.Errorf("agent run panicked: %v", p), TaskState: snapshotPtr(state)})
			}()
		}
	}()
	if err := ctx.Err(); err != nil {
		cancelled(err)
		return
	}

	limits := snap.limits
	// executed records every tool call ID that ran in this run, with
	// its terminal outcome. A repeated ID reuses the recorded result
	// instead of executing again, so a provider retry or a model echo
	// can never duplicate a side-effecting tool call.
	executed := make(map[string]executedCall)
	loops := newLoopDetector()

	// The agent and plan overlay are fixed for the run: build the base
	// prompt (contract + catalog + memory, several KB) once instead of
	// rebuilding it on every iteration. Only the task-state suffix
	// changes per iteration; see refreshSystemPrompt.
	promptBase, hasPromptBase := buildPromptBase(snap)

	for {
		if err := ctx.Err(); err != nil {
			cancelled(err)
			return
		}
		iteration := state.BeginIteration()
		if limits.MaxIterations > 0 && iteration > limits.MaxIterations {
			r.emitBlocked(emit, state, fmt.Sprintf("stopped after %d iterations (maximum reached)", limits.MaxIterations))
			return
		}

		refreshSystemPrompt(messages, promptBase, hasPromptBase, state)

		// Window the provider view every turn so conversation/tool
		// history can never grow past the model's context budget. The
		// full messages slice is retained for future windows; only
		// this turn's request is bounded.
		windowed := windowForProvider(messages, snap)
		turnStart := time.Now()
		tr.TurnStart(int64(iteration), len(windowed), estimateMessagesTokens(windowed))
		response, err := r.runModelTurn(ctx, windowed, emit, snap)
		if err != nil {
			tr.TurnError(int64(iteration), err, time.Since(turnStart))
			if cancellationErr(ctx, err) {
				cancelled(cancellationCause(ctx, err))
				return
			}
			emit(Event{Type: EventError, Err: err, TaskState: snapshotPtr(state)})
			return
		}
		tr.TurnEnd(int64(iteration), string(response.StopReason),
			response.Usage.PromptTokens, response.Usage.CompletionTokens,
			response.Usage.TotalTokens, len(response.ToolCalls), time.Since(turnStart))

		// Length exhaustion is incomplete, not success — with or without
		// tool calls. A length-truncated turn may carry partial argument
		// JSON; executing it would run an operation the model never
		// completed. Block without executing so partial output is never
		// confused with successful completion.
		if response.StopReason == providers.FinishLength {
			r.emitBlocked(emit, state, "model output truncated due to length limit (finish_reason=length) - response incomplete; retry with narrower tool output, fewer turns in context, or a model with a larger context window")
			return
		}
		if len(response.ToolCalls) == 0 {
			status := state.FinalStatus()
			state.SetStatus(status)
			emit(Event{Type: EventDone, Response: &response, Status: status, TaskState: snapshotPtr(state)})
			return
		}

		if limits.MaxToolCalls > 0 && state.ToolCallCount()+len(response.ToolCalls) > limits.MaxToolCalls {
			r.emitBlocked(emit, state, fmt.Sprintf("stopped after %d tool calls (maximum reached)", limits.MaxToolCalls))
			return
		}

		// Duplicate IDs reuse the recorded result without re-executing
		// (see executeCalls): they must not append a second
		// assistant/tool-result pair with the same ID to history,
		// which would bloat replay and risk strict-provider
		// rejection. The same non-empty ID twice in one batch keeps
		// only its first occurrence. Filter to not-yet-seen calls for
		// history; the full batch still goes to executeCalls so
		// idempotency and transcript events are unchanged. Empty IDs
		// carry no identity and always persist.
		skipHistory := make([]bool, len(response.ToolCalls))
		for i, tc := range response.ToolCalls {
			if tc.ID == "" {
				continue
			}
			if _, ok := executed[tc.ID]; ok {
				skipHistory[i] = true
				continue
			}
			for _, prev := range response.ToolCalls[:i] {
				if prev.ID == tc.ID {
					skipHistory[i] = true
					break
				}
			}
		}
		newCalls := make([]providers.ToolCall, 0, len(response.ToolCalls))
		for i, tc := range response.ToolCalls {
			if skipHistory[i] {
				continue
			}
			newCalls = append(newCalls, tc)
		}
		if len(newCalls) > 0 {
			messages = append(messages, providers.Message{
				Role:      providers.AssistantRole,
				Content:   response.Content,
				ToolCalls: newCalls,
			})
		}

		if snap.scheduler == nil || snap.manager == nil {
			emit(Event{Type: EventError, Err: fmt.Errorf("tool system not initialized"), TaskState: snapshotPtr(state)})
			return
		}
		loopCalls, loopIndexes := freshCallsForLoopDetection(response.ToolCalls, executed)
		results := r.executeCalls(ctx, response.ToolCalls, emit, snap, executed)

		if ctx.Err() != nil {
			cancelled(ctx.Err())
			return
		}

		for i, tc := range response.ToolCalls {
			result := results[i]
			state.RecordTool(tc.Name, result.Success)
			if skipHistory[i] {
				continue
			}
			content := truncateToolResult(result.Content)
			content = session.ScrubContent(content)
			content = session.FenceToolResult(tc.Name, content)
			messages = append(messages, providers.Message{
				Role:       providers.ToolRole,
				Name:       tc.Name,
				Content:    content,
				ToolCallID: tc.ID,
			})
		}

		loopResults := make([]ToolResult, len(loopIndexes))
		for i, index := range loopIndexes {
			loopResults[i] = results[index]
		}
		if loops.Observe(loopCalls, loopResults) {
			r.emitBlocked(emit, state, "Agent stopped: repeated tool execution detected without meaningful progress.")
			return
		}

		if limits.MaxConsecutiveFailures > 0 && state.ConsecutiveFailures() >= limits.MaxConsecutiveFailures {
			r.emitBlocked(emit, state, fmt.Sprintf(
				"stopped after %d consecutive tool failures - the agent appears stuck", state.ConsecutiveFailures()))
			return
		}
	}
}

// freshCallsForLoopDetection excludes a provider replay of an already-seen
// call ID. executeCalls reuses that result without invoking a tool, so it is
// not repeated execution and cannot cause side effects. Fresh IDs (and calls
// without IDs) remain visible to the progress guard.
func freshCallsForLoopDetection(calls []providers.ToolCall, executed map[string]executedCall) ([]providers.ToolCall, []int) {
	fresh := make([]providers.ToolCall, 0, len(calls))
	indexes := make([]int, 0, len(calls))
	for i, call := range calls {
		if call.ID != "" {
			if _, exists := executed[call.ID]; exists {
				continue
			}
		}
		fresh = append(fresh, call)
		indexes = append(indexes, i)
	}
	return fresh, indexes
}

// executedCall is the recorded outcome of one tool call ID within a
// run: the result plus the terminal event kind originally emitted, so
// a repeated ID replays the identical observable outcome.
type executedCall struct {
	result   ToolResult
	terminal EventType
}

// executeCalls runs one batch of tool calls with run-scoped idempotency.
// Calls whose non-empty ID already executed reuse the recorded result
// (marked explicitly, never silently) and emit the same Start/terminal
// event pair, keeping transcript and session pairing intact. Calls
// without IDs always execute — without identity there is nothing safe
// to key on. Fresh calls run concurrently through the scheduler; the
// returned slice aligns with calls.
func (r *Runtime) executeCalls(ctx context.Context, calls []providers.ToolCall, emit func(Event) bool, snap runSnapshot, executed map[string]executedCall) []ToolResult {
	results := make([]ToolResult, len(calls))
	var fresh []providers.ToolCall
	var freshIdx []int
	for i := range calls {
		tc := calls[i]
		if tc.ID != "" {
			if prev, ok := executed[tc.ID]; ok {
				dup := prev.result
				dup.Content += "\n\n[note: this tool call repeats call " + tc.ID +
					" from earlier in the same run; the recorded result is reused and the tool was not executed again]"
				results[i] = dup
				call := tc
				emit(Event{Type: EventToolStart, ToolCall: &call})
				emit(Event{Type: prev.terminal, ToolResult: &dup})
				continue
			}
		}
		fresh = append(fresh, tc)
		freshIdx = append(freshIdx, i)
	}
	if len(fresh) > 0 {
		// Concurrency follows negotiated capabilities: providers that
		// report no parallel tool support run batches sequentially.
		// Unreported providers keep the configured concurrency.
		maxConc := snap.scheduler.cfg.MaxConcurrency
		if snap.capsReported && !snap.caps.ParallelToolCalls {
			maxConc = 1
		}
		terminals := make(map[string]EventType, len(fresh))
		watchEmit := func(e Event) bool {
			if e.ToolResult != nil {
				switch e.Type {
				case EventToolFinish, EventToolFailed, EventToolCancelled, EventToolDenied:
					terminals[e.ToolResult.ToolCallID] = e.Type
				}
			}
			return emit(e)
		}
		freshResults := snap.scheduler.runWithConcurrency(ctx, fresh, watchEmit, snap.manager, maxConc)
		for j, res := range freshResults {
			results[freshIdx[j]] = res
			id := fresh[j].ID
			if id == "" {
				continue
			}
			terminal, ok := terminals[id]
			if !ok {
				// No terminal event (e.g. cancelled before start, which
				// is deliberately eventless): record as cancelled so a
				// repeat can never execute what this run skipped.
				terminal = EventToolCancelled
			}
			executed[id] = executedCall{result: res, terminal: terminal}
		}
	}
	return results
}

// toolCallingAllowed reports whether tool definitions are offered this
// turn. Definitions are withheld only when the provider explicitly
// reports no tool support; unreported providers keep the historical
// behavior of receiving them.
func toolCallingAllowed(snap runSnapshot) bool {
	return !snap.capsReported || snap.caps.ToolCalling
}

// emitBlocked reports a runtime-enforced stop with the final task snapshot.
func (r *Runtime) emitBlocked(emit func(Event) bool, state *task.State, reason string) {
	state.SetStatus(task.StatusBlocked)
	emit(Event{
		Type:      EventBlocked,
		Status:    task.StatusBlocked,
		TaskState: snapshotPtr(state),
		Err:       fmt.Errorf("%s", reason),
	})
}

// emitCancelled preserves cancellation as a first-class terminal result.
// Providers may return context.Canceled directly or the caller's context
// may have already been cancelled; both are a user/run-control outcome, not
// a model-provider failure.
func (r *Runtime) emitCancelled(emit func(Event) bool, state *task.State, err error) {
	if err == nil {
		err = context.Canceled
	}
	emit(Event{
		Type:      EventCancelled,
		TaskState: snapshotPtr(state),
		Err:       err,
	})
}

func cancellationErr(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled)
}

func cancellationCause(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func snapshotPtr(state *task.State) *task.Snapshot {
	snap := state.Snapshot()
	return &snap
}
