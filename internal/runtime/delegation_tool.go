package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/session"
	"forcefield/internal/task"
	"forcefield/internal/tools"
	"forcefield/internal/trace"

	"github.com/google/uuid"
)

// delegateReportMaxBytes caps the child report body carved into the
// parent's tool result. The runtime's context guard trims history to
// session.MaxModelToolResultChars on the way to the provider, but the
// header must survive that window, so the body is cut well below it
// here.
const delegateReportMaxBytes = 5000

// maxChildProgressLines bounds parent-visible child progress lines so a
// long child run cannot flood the transcript through the progress path.
const maxChildProgressLines = 32

// delegateTool runs a child agent run inside one tool call. It is the
// only delegation entry point: every caller-visible capability flows
// through the scope admitted by the orchestrator, and every child tool
// call passes the same scheduler permission path as the parent's own.
type delegateTool struct {
	orch *Orchestrator
}

func newDelegateTool(o *Orchestrator) tools.Tool {
	return delegateTool{orch: o}
}

func (delegateTool) Name() string { return "delegate" }

func (delegateTool) Description() string {
	return "Run a task with a specialised child agent and return its final report. " +
		"Use for bounded subtasks that benefit from a focused identity (e.g. research, " +
		"documentation) while you keep coordinating. The child starts with only its own " +
		"tools and permissions; its result is a report you should verify before acting on. " +
		"Provide a self-contained task description: the child does not see your conversation."
}

func (delegateTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent": map[string]any{
				"type":        "string",
				"description": "The child agent's name from the registry (e.g. \"research\", \"docs\", \"coding\").",
			},
			"task": map[string]any{
				"type":        "string",
				"description": "Self-contained description of the subtask for the child agent, including the goal, relevant context, and what a good result looks like.",
			},
		},
		"required": []string{"agent", "task"},
	}
}

// Metadata advertises delegation's execution characteristics: full
// ceiling timeout (a child run subsumes a bounded multi-turn run),
// parallel-safe (sibling delegate calls run concurrently under the
// orchestrator semaphore), cancellable, and never retried (a retried
// delegation duplicates side effects).
func (delegateTool) Metadata() tools.Metadata {
	return tools.Metadata{
		Timeout:              tools.MaxTimeout,
		SupportsParallel:     true,
		SupportsCancellation: true,
		SupportsStreaming:    true,
	}
}

// Execute is the non-streaming fallback used by direct Manager callers.
func (t delegateTool) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	return t.ExecuteStream(ctx, args, nil)
}

// ExecuteStream runs the child and reports throttled lifecycle progress.
// onChunk is the scheduler's redacted transcript sink (see
// runOneWithManager); nil means "no live progress" - the report is
// still complete.
func (t delegateTool) ExecuteStream(ctx context.Context, args map[string]any, onChunk func(tools.StreamChunk)) (tools.Result, error) {
	if o := t.orch; o == nil || o.parent == nil {
		return softDelegateResult("delegation is not wired up"), nil
	}

	scope := orchestrationScopeFrom(ctx)
	if scope == nil || scope.orch == nil || scope.snap == nil || scope.snap.scheduler == nil {
		return softDelegateResult("delegation is only available inside an agent run (no orchestration scope in context)"), nil
	}
	if err := ctx.Err(); err != nil {
		return tools.Result{IsError: true, Content: "delegation cancelled before it started"}, nil
	}

	agentName, taskText, verr := delegateArgs(args)
	if verr != "" {
		return softDelegateResult(verr), nil
	}
	agentName = strings.ToLower(strings.TrimSpace(agentName))
	taskText = strings.TrimSpace(taskText)
	if runes := []rune(taskText); len(runes) > scope.orch.resolved.maxTaskChars {
		return softDelegateResult(fmt.Sprintf(
			"task is %d characters, exceeding the limit of %d - delegate a smaller, more focused task",
			len(runes), scope.orch.resolved.maxTaskChars)), nil
	}

	def, err := scope.orch.registry.Get(agentName)
	if err != nil {
		return softDelegateResult(fmt.Sprintf("unknown agent %q (%v)", agentName, err)), nil
	}

	toolCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Process-wide child concurrency: acquired before the child spawns
	// and released after the join, so admitting a child and holding a
	// slot are one atomic, cancellable step.
	select {
	case scope.orch.sem <- struct{}{}:
		defer func() { <-scope.orch.sem }()
	case <-toolCtx.Done():
		return tools.Result{IsError: true, Content: "delegation cancelled before it started"}, nil
	}

	newDepth, admitted := scope.admitSpawn()
	if !admitted {
		if newDepth > scope.orch.resolved.maxDepth {
			return tools.Result{IsError: true, Content: fmt.Sprintf(
				"delegation depth limit reached (depth %d with limit %d) - handle this task yourself or revise the plan",
				newDepth, scope.orch.resolved.maxDepth)}, nil
		}
		return softDelegateResult(fmt.Sprintf(
			"delegation limit reached (%d child runs in this session, limit %d)",
			scope.spawnedTotal(), scope.orch.resolved.maxPerRun)), nil
	}

	return scope.orch.runChild(toolCtx, scope, def, newDepth, taskText, onChunk)
}

// delegateArgs extracts the schema-validated arguments defensively (the
// scheduler's strict validation already ran for model-originated calls;
// direct callers get the same soft failure).
func delegateArgs(args map[string]any) (agentName, taskText, failure string) {
	if args == nil {
		return "", "", `missing "agent" and "task"`
	}
	agentName = stringArg(args, "agent")
	taskText = stringArg(args, "task")
	if strings.TrimSpace(agentName) == "" {
		return "", "", `missing "agent"`
	}
	if strings.TrimSpace(taskText) == "" {
		return "", "", `missing "task"`
	}
	return agentName, taskText, ""
}

// stringArg reads one string field. Only strings pass; anything else is
// a missing field.
func stringArg(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// softDelegateResult returns a model-actionable soft error.
func softDelegateResult(msg string) tools.Result {
	return tools.Result{IsError: true, Content: msg}
}

// childOutput records lifecycle counters from the child's event stream
// and captures exactly one terminal outcome. It is the single consumer
// of child events: no external consumer exists, so no child producer
// can ever block on a missing reader, and the recorder is closed by the
// child loop's own termination rather than by anything else.
type childRunRecorder struct {
	mu        sync.Mutex
	turns     int
	toolCalls int
	denied    map[string]int
	failed    int

	budget int

	seen bool
	out  childOutcome

	onChunk   func(tools.StreamChunk)
	agentName string
}

type childOutcome struct {
	terminal  EventType
	response  *providers.Response
	status    Status
	taskState *task.Snapshot
	errText   string
}

// On ingests one child event. Safe for concurrent schedulers inside the
// child run: every field access is under the recorder mutex.
func (r *childRunRecorder) On(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch e.Type {
	case EventThinking:
		r.turns++
		// One throttled lifecycle line per child turn. The scheduler
		// redacts chunks before they reach the transcript.
		if r.onChunk != nil && r.budget > 0 {
			r.budget--
			name := r.agentName
			r.onChunk(tools.StreamChunk{Stream: "progress", Data: fmt.Sprintf("[delegate: agent %q turn %d]", name, r.turns)})
		}
	case EventToolFinish, EventToolFailed, EventToolCancelled, EventToolDenied:
		if e.ToolResult == nil {
			return
		}
		// Result-bearing terminal events count the call exactly once.
		r.toolCalls++
		switch e.Type {
		case EventToolDenied:
			if r.denied == nil {
				r.denied = make(map[string]int)
			}
			r.denied[e.ToolResult.Name]++
		case EventToolFailed:
			r.failed++
		}
	case EventDone, EventError, EventBlocked, EventCancelled:
		if r.seen {
			return // exactly one terminal record: the first wins
		}
		r.seen = true
		r.out = childOutcome{
			terminal:  e.Type,
			response:  e.Response,
			status:    e.Status,
			taskState: e.TaskState,
			errText:   eventErrText(e),
		}
	}
}

func eventErrText(e Event) string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return ""
}

func (r *childRunRecorder) terminalSeen() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen
}

func (r *childRunRecorder) deniedTotal() (total int, names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, n := range r.denied {
		total += n
		names = append(names, name)
	}
	return total, names
}

// runChild builds and drives the child run and joins it before
// returning. There is no detached mode: every exit path waits for the
// child to terminate, and nothing completes after the tool result.
func (o *Orchestrator) runChild(
	ctx context.Context,
	scope *orchestrationScope,
	def agent.Definition,
	newDepth int,
	taskText string,
	outerChunk func(tools.StreamChunk),
) (tools.Result, error) {
	if o == nil || o.parent == nil {
		return tools.Result{IsError: true, Content: "delegation is not wired up"}, nil
	}

	// Delegate is available to the child only where another nesting
	// level still fits. Combined with the admitSpawn depth check this
	// makes the depth bound structural on both sides.
	allowDelegate := newDepth < o.resolved.maxDepth

	childMgr, err := o.childAgentManager(def, allowDelegate)
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}

	// The child scheduler shares global permission rules with the parent
	// but owns its own, empty session decisions: nothing the operator
	// approved in the parent session carries into the child, and nothing
	// the child records leaks back. Concurrency is halved so nested
	// schedulers never multiply the parent's parallel footprint.
	childCfg := scope.snap.scheduler.cfg
	childCfg.MaxConcurrency = 2
	// The child's chain includes its own name (root first), so approval
	// surfaces attribute the ask to the full delegation path.
	childScope := scope.childScope(def.Name)
	asker := o.childAsker(def, childScope.chain)
	scheduler := newScheduler(childMgr, scope.snap.scheduler.permissions, asker, childCfg)
	scheduler.askSerial = o.childAskSerial()

	childSnap := *scope.snap
	childSnap.agent = o.parent.buildAgent(def)
	childSnap.manager = childMgr
	childSnap.scheduler = scheduler
	childSnap.mode = ModeChat // children always chat: plan overlays never leak into child policy

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	history := []providers.Message{{Role: providers.UserRole, Content: taskText}}
	initial := buildMessagesWithBudget(history, childSnap.agent, childSnap.contextBudget)

	rec := &childRunRecorder{
		budget:    maxChildProgressLines,
		onChunk:   outerChunk,
		agentName: def.Name,
	}
	emit := func(e Event) bool {
		rec.On(e)
		return true
	}

	// Child trace: same enablement, redaction, retention, and caps as a
	// top-level run. The run id is a fresh UUID; the agent field carries
	// the child name for forensics.
	var childTr *trace.Run
	if o.parent.tracer != nil && o.parent.tracer.Enabled() {
		childTr = o.parent.tracer.StartRun(uuid.NewString(), trace.RunMeta{
			Provider:     childSnap.providerName,
			Model:        childSnap.modelName,
			Agent:        def.Name,
			MessageCount: len(initial),
		})
	}

	childCtx = withOrchestrationScope(childCtx, childScope)

	done := make(chan struct{})
	go func() {
		defer close(done)
		o.parent.run(childCtx, initial, emit, childSnap, childTr)
		if childTr != nil {
			childTr.Close()
		}
	}()

	// Unconditional join: wait for the child's terminal outcome even
	// under cancellation. Parent cancellation stops future work by
	// cancelling childCtx; the child's scheduler, tool timeouts, and
	// provider streams honor it, matching single-agent semantics.
	// Nothing is abandoned and nothing reports after the tool result.
	select {
	case <-done:
	case <-ctx.Done():
		<-done
	}

	if childCtx.Err() != nil && !rec.terminalSeen() {
		// Defensive: the run loop always emits a terminal event. A
		// missing record under cancellation is recorded honestly as a
		// cancellation rather than invented as success.
		rec.mu.Lock()
		rec.seen = true
		rec.out = childOutcome{terminal: EventCancelled, errText: childCtx.Err().Error()}
		rec.mu.Unlock()
	}

	return composeDelegateResult(def.Name, rec), nil
}

// composeDelegateResult renders the parent-visible tool result: a
// bounded header with honest status accounting, the child's final
// answer (scrubbed), and denial/failure notes. Child statuses are
// informational: the parent's own verification state is computed
// independently and is never upgraded by a child's claim.
func composeDelegateResult(agentName string, rec *childRunRecorder) tools.Result {
	rec.mu.Lock()
	out := rec.out
	turns, toolCalls, failed := rec.turns, rec.toolCalls, rec.failed
	seen := rec.seen
	rec.mu.Unlock()
	denied, deniedNames := rec.deniedTotal()

	head := fmt.Sprintf("[delegate] agent=%q turns=%d tool_calls=%d failed_tool_calls=%d denied=%d",
		agentName, turns, toolCalls, failed, denied)
	if len(deniedNames) > 0 {
		head += " (denied: " + strings.Join(dedupToolNames(deniedNames), ", ") + ")"
	}

	if !seen {
		return tools.Result{IsError: true, Content: head + "\nchild run ended without a terminal event - treating as failure"}
	}

	switch out.terminal {
	case EventDone:
		body := ""
		if out.response != nil {
			body = out.response.Content
		}
		statusLine := fmt.Sprintf("[delegate] child status=%s", out.status)
		if out.taskState != nil {
			statusLine += fmt.Sprintf(" verification=%s phase=%q", out.taskState.Verification, out.taskState.Phase)
		}
		content := truncateDelegateBody(head+"\n"+statusLine+"\n\n"+scrubbedContent(body), delegateReportMaxBytes)
		return tools.Result{
			Content: content,
			Metadata: map[string]any{
				"agent": agentName, "turns": turns, "tool_calls": toolCalls, "denied": denied,
			},
		}
	case EventError:
		return tools.Result{IsError: true, Content: head + "\nchild run failed: " + scrubbedContent(out.errText)}
	case EventBlocked:
		return tools.Result{IsError: true, Content: head + "\nchild run blocked: " + scrubbedContent(out.errText)}
	case EventCancelled:
		return tools.Result{IsError: true, Content: head + "\ndelegation cancelled"}
	default:
		return tools.Result{IsError: true, Content: head + "\nunknown child terminal event"}
	}
}

// truncateDelegateBody applies a rune-boundary byte cut with the shared
// truncation marker so the model sees honest bounds.
func truncateDelegateBody(content string, maxBytes int) string {
	truncated, rec := tools.TruncateString(content, maxBytes)
	if rec.Truncated {
		return truncated + tools.TruncationNote(rec.KeptBytes, rec.OriginalBytes)
	}
	return truncated
}

// scrubbedContent applies the shared redaction before the child report
// enters the parent's tool path. The scheduler scrubs again after; this
// is defense-in-depth at the composition site.
func scrubbedContent(content string) string {
	return session.ScrubContent(content)
}

func dedupToolNames(list []string) []string {
	seen := make(map[string]struct{}, len(list))
	uniq := make([]string, 0, len(list))
	for _, item := range list {
		if _, ok := seen[item]; !ok {
			seen[item] = struct{}{}
			uniq = append(uniq, item)
		}
	}
	return uniq
}

// childAskSerial returns the shared ask serializer in interactive mode,
// or nil in deny mode (children never open prompts there; each child
// scheduler keeps its own askMu, and deny asks never block on a human).
func (o *Orchestrator) childAskSerial() *sync.Mutex {
	if o == nil || o.resolved.childPermsMode != config.ChildPermissionsInteractive {
		return nil
	}
	return &o.askLock
}
