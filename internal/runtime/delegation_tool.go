package runtime

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/orchevents"
	"forcefield/internal/providers"
	"forcefield/internal/redact"
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

// delegateBatchReportMaxBytes caps the aggregate report one native
// batch delegation returns to the parent. The per-task budget is this
// bound split across the declared tasks, so one verbose child cannot
// crowd out its siblings.
const delegateBatchReportMaxBytes = 20000

// maxBatchTasks caps how many subtasks one native batch delegation may
// carry. The per-run spawn budget (max_children_per_run) is the real
// ceiling; this bound keeps one call's argument array, its admission
// pass, and its aggregate report finite before any child is admitted.
const maxBatchTasks = 16

// maxChildProgressLines bounds parent-visible child progress lines so a
// long child run cannot flood the transcript through the progress path.
const maxChildProgressLines = 32

// maxChildTextChunkBytes bounds one live child-text chunk. Child text
// is coalesced up to a line boundary (or this size) before it is
// published, so a token-by-token stream does not turn into one event
// per token.
const maxChildTextChunkBytes = 512

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
		"Provide a self-contained task description: the child does not see your conversation. " +
		"For independent subtasks, issue multiple delegate calls in the same response: they " +
		"run concurrently up to the configured child limit, and each result arrives separately. " +
		"Delegate one call per response only when the next task depends on the previous result. " +
		"For native batch delegation, pass a tasks array with independent subtasks; the runtime " +
		"launches each task as its own child run concurrently (subject to child limits). " +
		"Each task must be self-contained and independent of the others."
}

func (delegateTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent": map[string]any{
				"type":        "string",
				"description": "The child agent's name from the registry (e.g. \"research\", \"docs\", \"coding\"). Required for single-task mode.",
			},
			"task": map[string]any{
				"type":        "string",
				"description": "Self-contained description of the subtask for the child agent, including the goal, relevant context, and what a good result looks like. Required for single-task mode.",
			},
			"tasks": map[string]any{
				"type":        "array",
				"description": "Native batch delegation: an array of independent subtasks, each with its own agent and task description. All admitted tasks run concurrently; results are returned in declared order. Use this when you have multiple independent subtasks that can run in parallel.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"agent": map[string]any{
							"type":        "string",
							"description": "The child agent's name from the registry.",
						},
						"task": map[string]any{
							"type":        "string",
							"description": "Self-contained description of the subtask.",
						},
					},
					"required": []string{"agent", "task"},
				},
			},
		},
		// Schema validation: either single mode (agent+task) or batch mode (tasks array).
		// The runtime enforces this defensively in ExecuteStream.
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

	agentName, taskText, batchTasks, verr := delegateArgs(args)
	if verr != "" {
		return softDelegateResult(verr), nil
	}

	// Batch mode: one invocation, several independent child runs. The
	// batch path owns its own admission, concurrency, and report
	// composition; it never falls back to the single-task path.
	if len(batchTasks) > 0 {
		return t.executeBatch(ctx, scope, batchTasks, onChunk)
	}

	// Single mode: one task
	agentName = strings.ToLower(strings.TrimSpace(agentName))
	taskText = strings.TrimSpace(taskText)
	taskRunes := len([]rune(taskText))
	scope.orch.emitDelegation(ctx, scope, agentName, orchevents.Event{
		Type:      orchevents.DelegationRequested,
		Agent:     agentName,
		TaskChars: taskRunes,
	})
	if taskRunes > scope.orch.resolved.maxTaskChars {
		msg := fmt.Sprintf(
			"task is %d characters, exceeding the limit of %d - delegate a smaller, more focused task",
			taskRunes, scope.orch.resolved.maxTaskChars)
		scope.orch.emitDelegation(ctx, scope, agentName, orchevents.Event{
			Type:        orchevents.DelegationRejected,
			Agent:       agentName,
			Budget:      orchevents.BudgetTaskChars,
			BudgetLimit: scope.orch.resolved.maxTaskChars,
			BudgetValue: taskRunes,
			Reason:      msg,
		})
		return softDelegateResult(msg), nil
	}

	def, err := scope.orch.registry.Get(agentName)
	if err != nil {
		scope.orch.emitDelegation(ctx, scope, agentName, orchevents.Event{
			Type:   orchevents.DelegationRejected,
			Agent:  agentName,
			Reason: fmt.Sprintf("unknown agent %q (%v)", agentName, err),
		})
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
			msg := fmt.Sprintf(
				"delegation depth limit reached (depth %d with limit %d) - handle this task yourself or revise the plan",
				newDepth, scope.orch.resolved.maxDepth)
			scope.orch.emitDelegation(toolCtx, scope, agentName, orchevents.Event{
				Type:        orchevents.DelegationRejected,
				Agent:       agentName,
				Budget:      orchevents.BudgetDepth,
				BudgetLimit: scope.orch.resolved.maxDepth,
				BudgetValue: newDepth,
				Reason:      msg,
			})
			return tools.Result{IsError: true, Content: msg}, nil
		}
		msg := fmt.Sprintf(
			"delegation limit reached (%d child runs in this session, limit %d)",
			scope.spawnedTotal(), scope.orch.resolved.maxPerRun)
		scope.orch.emitDelegation(toolCtx, scope, agentName, orchevents.Event{
			Type:        orchevents.DelegationRejected,
			Agent:       agentName,
			Budget:      orchevents.BudgetSpawn,
			BudgetLimit: scope.orch.resolved.maxPerRun,
			BudgetValue: scope.spawnedTotal(),
			Reason:      msg,
		})
		return softDelegateResult(msg), nil
	}

	scope.orch.emitDelegation(toolCtx, scope, agentName, orchevents.Event{
		Type:      orchevents.DelegationAccepted,
		Agent:     agentName,
		TaskChars: taskRunes,
	})

	childScope := scope.childScope(def.Name)
	return scope.orch.runChild(toolCtx, scope, childScope, def, newDepth, taskText, onChunk)
}

// batchTask is one declared subtask of a native batch delegation. The
// values are the raw argument strings; trimming and lower-casing happen
// once, in the batch path, exactly like the single-task path does.
type batchTask struct {
	Agent string
	Task  string
}

// preparedBatchTask is one validated subtask: the registry definition
// resolved once, the canonical agent name, and the trimmed task text.
type preparedBatchTask struct {
	agent string
	task  string
	def   agent.Definition
}

// batchTaskLabel is the chain leaf a batch child carries. The agent
// name plus its 1-based position keeps two tasks of the same batch
// distinct even when they target the same agent, so the chain stays a
// unique correlation key for the whole batch.
func batchTaskLabel(agentName string, index int) string {
	return agentName + "#" + strconv.Itoa(index+1)
}

// executeBatch launches every admitted task of a native batch
// delegation as its own child run, concurrently, and returns one
// aggregate report to the parent.
//
// The batch path preserves every control the single-task path has:
//   - the same depth bound and the same cumulative per-run spawn budget,
//     applied per task (a task that does not fit is rejected, the
//     admitted siblings still run);
//   - the same process-wide child semaphore, so a batch cannot exceed
//     max_concurrent_children;
//   - the same per-child snapshot isolation (each task gets its own
//     child scope, scheduler, provider clone, and permission surface);
//   - the same fail-closed permission and sandbox paths.
//
// Two failure rules differ from a plain fan-out of separate delegate
// calls, and both are deliberate:
//   - a task that is not admitted never cancels its siblings; it is
//     reported as not admitted;
//   - one child failing never cancels its siblings; only the parent's
//     own cancellation propagates into every task.
func (t delegateTool) executeBatch(ctx context.Context, scope *orchestrationScope, batchTasks []batchTask, onChunk func(tools.StreamChunk)) (tools.Result, error) {
	if len(batchTasks) == 0 {
		return softDelegateResult("batch delegation: no tasks provided"), nil
	}
	if len(batchTasks) > maxBatchTasks {
		return softDelegateResult(fmt.Sprintf(
			"batch delegation carries %d tasks, exceeding the limit of %d - split the work into smaller delegate calls",
			len(batchTasks), maxBatchTasks)), nil
	}

	resolved := scope.orch.resolved

	// Phase 1: validate every task before anything is admitted. One
	// invalid task rejects the whole batch, so a malformed batch never
	// spends spawn budget and never starts a child.
	prepared := make([]preparedBatchTask, len(batchTasks))
	for i, bt := range batchTasks {
		name := strings.ToLower(strings.TrimSpace(bt.Agent))
		text := strings.TrimSpace(bt.Task)
		if name == "" || text == "" {
			return softDelegateResult(fmt.Sprintf(
				"batch task %d: an agent and a task description are both required", i+1)), nil
		}
		def, err := scope.orch.registry.Get(name)
		if err != nil {
			return softDelegateResult(fmt.Sprintf(
				"unknown agent %q in batch task %d", name, i+1)), nil
		}
		if runes := len([]rune(text)); runes > resolved.maxTaskChars {
			return softDelegateResult(fmt.Sprintf(
				"batch task %d is %d characters, exceeding the limit of %d - delegate a smaller, more focused task",
				i+1, runes, resolved.maxTaskChars)), nil
		}
		prepared[i] = preparedBatchTask{agent: name, task: text, def: def}
	}

	// Phase 2: admit the tasks in declared order. Admission is
	// deterministic: the first tasks that fit the depth and per-run
	// budgets run, the rest are reported as not admitted. A partial
	// admission never cancels the admitted siblings.
	admitted := make([]bool, len(prepared))
	rejected := make([]string, len(prepared))
	admittedCount := 0
	for i := range prepared {
		newDepth, ok := scope.admitSpawn()
		if ok {
			admitted[i] = true
			admittedCount++
			continue
		}
		if newDepth > resolved.maxDepth {
			rejected[i] = fmt.Sprintf(
				"delegation depth limit reached (depth %d with limit %d) - handle this task yourself or revise the plan",
				newDepth, resolved.maxDepth)
			continue
		}
		rejected[i] = fmt.Sprintf(
			"delegation limit reached (%d child runs in this session, limit %d)",
			scope.spawnedTotal(), resolved.maxPerRun)
	}

	// Phase 3: publish the delegation lifecycle for every task, in
	// declared order. The events carry the batch index, so a consumer
	// can tell the tasks apart and anchor each one to its own child.
	for i := range prepared {
		label := batchTaskLabel(prepared[i].def.Name, i)
		ev := orchevents.Event{Agent: prepared[i].agent, BatchIndex: i + 1}
		if !admitted[i] {
			ev.Type = orchevents.DelegationRejected
			ev.Reason = rejected[i]
			if scope.depth+1 > resolved.maxDepth {
				ev.Budget = orchevents.BudgetDepth
				ev.BudgetLimit = resolved.maxDepth
				ev.BudgetValue = scope.depth + 1
			} else {
				ev.Budget = orchevents.BudgetSpawn
				ev.BudgetLimit = resolved.maxPerRun
				ev.BudgetValue = scope.spawnedTotal()
			}
			scope.orch.emitDelegation(ctx, scope, label, ev)
			continue
		}
		ev.Type = orchevents.DelegationRequested
		ev.TaskChars = len([]rune(prepared[i].task))
		scope.orch.emitDelegation(ctx, scope, label, ev)
		ev.Type = orchevents.DelegationAccepted
		scope.orch.emitDelegation(ctx, scope, label, ev)
	}

	// Phase 4: run the admitted tasks concurrently. Each task takes its
	// own child scope (own chain leaf, own snapshot copy, own provider
	// clone) and its own slot of the process-wide child semaphore.
	childDepth := scope.depth + 1
	results := make([]tools.Result, len(prepared))
	var wg sync.WaitGroup
	for i := range prepared {
		if !admitted[i] {
			results[i] = tools.Result{IsError: true, Content: rejected[i]}
			continue
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			taskCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			// Process-wide child concurrency: one slot per running
			// child, exactly like the single-task path. A task that
			// cannot get a slot before the parent is cancelled reports
			// an honest cancellation instead of running detached.
			select {
			case scope.orch.sem <- struct{}{}:
				defer func() { <-scope.orch.sem }()
			case <-taskCtx.Done():
				results[idx] = tools.Result{IsError: true, Content: "delegation cancelled before it started"}
				return
			}

			def := prepared[idx].def
			childScope := scope.childScopeLabeled(def.Name, batchTaskLabel(def.Name, idx))
			childScope.batchIndex = idx + 1
			result, err := scope.orch.runChild(taskCtx, scope, childScope, def, childDepth, prepared[idx].task, onChunk)
			if err != nil {
				result = tools.Result{IsError: true, Content: "delegation failed: " + scrubbedContent(err.Error())}
			}
			results[idx] = result
		}(i)
	}
	wg.Wait()

	return composeBatchResult(prepared, admitted, rejected, results), nil
}

// composeBatchResult renders the parent-visible report of a batch: one
// bounded section per declared task, in declared order, with an honest
// header naming how many tasks completed, failed, and were not
// admitted. Every section carries that child's own delegate report, so
// the parent reads exactly what it would have read from separate
// delegate calls.
func composeBatchResult(prepared []preparedBatchTask, admitted []bool, rejected []string, results []tools.Result) tools.Result {
	n := len(prepared)
	completed, failed, notAdmitted := 0, 0, 0
	for i := range prepared {
		switch {
		case !admitted[i]:
			notAdmitted++
		case results[i].IsError:
			failed++
		default:
			completed++
		}
	}

	// Per-task body budget: the aggregate bound split across the tasks,
	// minus a small allowance for the section headers. The floor keeps
	// a very large batch from starving every task of its report.
	perTask := (delegateBatchReportMaxBytes - n*64) / n
	if perTask < 512 {
		perTask = 512
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[delegate:batch] tasks=%d completed=%d failed=%d not_admitted=%d",
		n, completed, failed, notAdmitted)
	for i := range prepared {
		fmt.Fprintf(&b, "\n\n[batch %d/%d agent=%q]", i+1, n, prepared[i].agent)
		if !admitted[i] {
			b.WriteString("\n")
			b.WriteString(rejected[i])
			continue
		}
		b.WriteString("\n")
		b.WriteString(truncateDelegateBody(results[i].Content, perTask))
	}
	content := truncateDelegateBody(b.String(), delegateBatchReportMaxBytes)

	meta := map[string]any{
		"batch_size":   n,
		"completed":    completed,
		"failed":       failed,
		"not_admitted": notAdmitted,
	}
	return tools.Result{
		Content:  content,
		IsError:  failed > 0 || notAdmitted > 0,
		Metadata: meta,
	}
}

// delegateArgs extracts the schema-validated arguments defensively (the
// scheduler's strict validation already ran for model-originated calls;
// direct callers get the same soft failure).
//
// Returns (agentName, taskText, tasks, failure). The strings are the
// raw mapped values: the caller trims and lower-cases them, exactly as
// the single-task path always has. A batch call (a "tasks" array)
// populates tasks and leaves the single-task fields empty; a single-task
// call populates the two strings and leaves tasks empty. The failure
// wording is fixed and never echoes model-controlled content.
func delegateArgs(args map[string]any) (string, string, []batchTask, string) {
	if args == nil {
		return "", "", nil, `missing "agent" and "task" or "tasks"`
	}

	if raw, ok := args["tasks"]; ok {
		tasks, failure := parseBatchTasks(raw)
		if failure != "" {
			return "", "", nil, failure
		}
		return "", "", tasks, ""
	}

	agentName := stringArg(args, "agent")
	taskText := stringArg(args, "task")
	if strings.TrimSpace(agentName) == "" {
		return "", "", nil, `missing "agent"`
	}
	if strings.TrimSpace(taskText) == "" {
		return "", "", nil, `missing "task"`
	}
	return agentName, taskText, nil, ""
}

// parseBatchTasks validates one "tasks" array defensively. The schema
// layer checks top-level types only, so the nested item objects are
// validated here: every item must be an object with a non-empty agent
// and task string. The values are returned raw; the caller trims them.
func parseBatchTasks(raw any) ([]batchTask, string) {
	list, ok := raw.([]any)
	if !ok {
		return nil, `tasks must be an array`
	}
	if len(list) == 0 {
		return nil, `tasks array is empty`
	}
	tasks := make([]batchTask, 0, len(list))
	for i, item := range list {
		taskMap, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Sprintf("tasks[%d] must be an object", i)
		}
		agentName := stringArg(taskMap, "agent")
		taskText := stringArg(taskMap, "task")
		if strings.TrimSpace(agentName) == "" {
			return nil, fmt.Sprintf("tasks[%d] missing agent", i)
		}
		if strings.TrimSpace(taskText) == "" {
			return nil, fmt.Sprintf("tasks[%d] missing task", i)
		}
		tasks = append(tasks, batchTask{Agent: agentName, Task: taskText})
	}
	return tasks, ""
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

	// pendingText holds the child's streamed text until it is worth
	// publishing: a chunk is released at a line boundary, at the byte
	// cap, or when the child's turn ends. The buffer is bounded by the
	// same cap, so a runaway stream cannot grow it without limit.
	pendingText strings.Builder

	// Run-level evidence hooks. Emissions are best-effort
	// observability and never touch the child outcome.
	orch       *Orchestrator
	childScope *orchestrationScope
	childRunID string
	childCtx   context.Context

	// Aggregate budget hooks (Contract: orchestration.max_total_child_
	// turns): the shared pool, this limit, and the child context cancel
	// so the offending child stops honestly. overBudget marks that this
	// child's own cancellation came from
	shared       *orchestrationShared
	turnLimit    int
	budgetCancel func()
	overBudget   bool
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
	case EventText:
		r.onChildText(e.Text)
	case EventThinking:
		// A reasoning delta is not a model turn: deltas stream within
		// one inference, and charging them as turns let verbose models
		// consume the aggregate turn budget mid-inference (wave-1
		// evidence: seven "turns" charged inside one 11.4s inference,
		// killing a healthy child before its second real turn).
		// runtime_turn emits one empty-thinking marker per inference
		// attempt start, which is exactly the model-turn unit.
		if e.Thinking != "" {
			return
		}
		// The marker opens a new turn, so the previous turn's text is
		// complete: release whatever is still buffered.
		r.flushChildText()
		r.turns++
		// One throttled lifecycle line per child turn. The scheduler
		// redacts chunks before they reach the transcript.
		if r.onChunk != nil && r.budget > 0 {
			r.budget--
			name := r.agentName
			r.onChunk(tools.StreamChunk{Stream: "progress", Data: fmt.Sprintf("[delegate: agent %q turn %d]", name, r.turns)})
		}
		// Run-level evidence: one bounded-counter line per child turn,
		// capped by the event file's byte cap rather than the transcript
		// budget so evidence counts stay exact.
		if r.orch != nil {
			r.orch.emitChildRun(r.childCtx, r.childScope, orchevents.Event{
				Type:       orchevents.ChildProgress,
				Agent:      r.agentName,
				ChildRunID: r.childRunID,
				Turns:      r.turns,
			})
		}
		// Aggregate budget: every child turn of the top-level run shares
		// one pool. Crossing it cancels this child's context honestly (the
		// child records an actual cancellation, never an invented error)
		// and reports the exhaustion exactly once, on the first crossing.
		if r.shared != nil && r.turnLimit > 0 {
			total, over, fire := r.shared.consumeTurn(r.turnLimit)
			if over {
				r.overBudget = true
				if r.budgetCancel != nil {
					r.budgetCancel()
				}
				if fire && r.orch != nil {
					r.orch.emitChildRun(r.childCtx, r.childScope, orchevents.Event{
						Type:        orchevents.BudgetExhausted,
						Agent:       r.agentName,
						ChildRunID:  r.childRunID,
						Budget:      orchevents.BudgetTotalTurns,
						BudgetLimit: r.turnLimit,
						BudgetValue: total,
						Reason: fmt.Sprintf(
							"aggregate child-turn budget exhausted (%d turns across all children, limit %d)", total, r.turnLimit),
					})
				}
			}
		}
	case EventToolStart:
		r.emitChildToolStarted(e)
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
		r.emitChildToolFinished(e)
	case EventDone, EventError, EventBlocked, EventCancelled:
		// The child's last words are still worth showing: release the
		// buffered text before the terminal outcome is recorded.
		r.flushChildText()
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

// onChildText buffers one streamed text delta and publishes it once it
// reaches a line boundary or the byte cap. Coalescing keeps a
// token-by-token stream from turning into one event per token, while
// the line boundary keeps the published chunk useful to a reader.
func (r *childRunRecorder) onChildText(text string) {
	if text == "" || r.orch == nil {
		return
	}
	r.pendingText.WriteString(text)
	if strings.Contains(text, "\n") || r.pendingText.Len() >= maxChildTextChunkBytes {
		r.flushChildText()
	}
}

// flushChildText publishes the buffered child text as one live-text
// event. The text is scrubbed before it leaves the runtime: the live
// view is downstream of the redaction boundary, and the headless writer
// never persists child text at all.
func (r *childRunRecorder) flushChildText() {
	if r.orch == nil || r.pendingText.Len() == 0 {
		return
	}
	text := r.pendingText.String()
	r.pendingText.Reset()
	r.orch.emitChildRun(r.childCtx, r.childScope, orchevents.Event{
		Type:       orchevents.ChildText,
		Agent:      r.agentName,
		ChildRunID: r.childRunID,
		Text:       redact.Scrub(text),
	})
}

// emitChildToolStarted publishes the child's tool start, so a live
// consumer can name the tool the child is running right now.
func (r *childRunRecorder) emitChildToolStarted(e Event) {
	if r.orch == nil || e.ToolCall == nil || e.ToolCall.Name == "" {
		return
	}
	r.orch.emitChildRun(r.childCtx, r.childScope, orchevents.Event{
		Type:       orchevents.ToolStarted,
		Agent:      r.agentName,
		ChildRunID: r.childRunID,
		CallID:     e.ToolCall.ID,
		Tool:       e.ToolCall.Name,
	})
}

// emitChildToolFinished publishes the child's tool terminal. Only the
// tool's name, duration, and outcome travel: never its arguments and
// never its output.
func (r *childRunRecorder) emitChildToolFinished(e Event) {
	if r.orch == nil || e.ToolResult == nil {
		return
	}
	typ := orchevents.ToolCompleted
	switch e.Type {
	case EventToolFailed:
		typ = orchevents.ToolFailed
	case EventToolDenied:
		typ = orchevents.ToolDenied
	case EventToolCancelled:
		typ = orchevents.ToolCancelled
	}
	if e.Type == EventToolFinish && e.ToolResult.IsError {
		typ = orchevents.ToolFailed
	}
	ev := orchevents.Event{
		Type:       typ,
		Agent:      r.agentName,
		ChildRunID: r.childRunID,
		CallID:     e.ToolResult.ToolCallID,
		Tool:       e.ToolResult.Name,
		DurationMS: e.ToolResult.Duration.Milliseconds(),
		Attempt:    e.ToolResult.Attempt,
	}
	if typ == orchevents.ToolFailed && e.ToolResult.Err != nil {
		ev.Error = redact.Scrub(e.ToolResult.Err.Error())
	}
	r.orch.emitChildRun(r.childCtx, r.childScope, ev)
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
	sort.Strings(names)
	return total, names
}

// runChild builds and drives the child run and joins it before
// returning. There is no detached mode: every exit path waits for the
// child to terminate, and nothing completes after the tool result.
//
// childScope is the scope the child runs under: one level deeper than
// the delegating scope, carrying the child's own chain leaf. The caller
// builds it so a batch delegation can give each task a distinct leaf.
func (o *Orchestrator) runChild(
	ctx context.Context,
	scope *orchestrationScope,
	childScope *orchestrationScope,
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
	asker := o.childAsker(def, childScope.chain)
	scheduler := newScheduler(childMgr, scope.snap.scheduler.permissions, asker, childCfg)
	scheduler.askSerial = o.childAskSerial()

	childSnap := *scope.snap
	// Run-scoped provider isolation: the child gets its own provider
	// instance so its single-request gate and reasoning state never
	// contend with (or are corrupted by) the parent's or a sibling
	// child's. Built-in adapters clone cheaply here (shared HTTP client
	// and configuration, fresh per-run state). A built-in that cannot
	// clone fails the delegation clearly instead of sharing mutable
	// state; external and test providers keep the legacy shared
	// contract and run as-is.
	clone, cloneable, err := providers.CloneForRun(scope.snap.provider)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf(
			"delegation failed: agent %q needs a run-scoped provider and cloning failed: %v", def.Name, err)}, nil
	}
	if cloneable {
		childSnap.provider = clone
	}
	childSnap.agent = o.parent.buildAgent(def)
	childSnap.manager = childMgr
	childSnap.scheduler = scheduler
	childSnap.mode = ModeChat // children always chat: plan overlays never leak into child policy
	if cfg := o.parent.configSnapshot(); cfg != nil {
		base := scope.snap.baseLimits
		if base == (Limits{}) {
			base = scope.snap.limits
		}
		if base == (Limits{}) {
			base = limitsFromConfig(cfg)
		}
		childSnap.baseLimits = base
		childSnap.limits = limitsForAgent(cfg, def.Name, base)
		childSnap.contextBudget = contextBudgetFromConfig(cfg, childSnap.modelName, def.Name, childSnap.caps)
	}
	childScope.snap = &childSnap

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	history := []providers.Message{{Role: providers.UserRole, Content: taskText}}
	initial := buildMessagesWithBudget(history, childSnap.agent, childSnap.contextBudget)

	childRunID := orchevents.ChildUUID()
	childStart := time.Now()
	rec := &childRunRecorder{
		budget:       maxChildProgressLines,
		onChunk:      outerChunk,
		agentName:    def.Name,
		orch:         o,
		childScope:   childScope,
		childRunID:   childRunID,
		childCtx:     childCtx,
		shared:       scope.shared,
		turnLimit:    o.resolved.maxTotalTurns,
		budgetCancel: cancel,
	}
	emit := func(e Event) bool {
		rec.On(e)
		return true
	}
	o.emitChildRun(childCtx, childScope, orchevents.Event{
		Type:       orchevents.AgentSpawned,
		Agent:      def.Name,
		ChildRunID: childRunID,
	})

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

	var childSlotStats *providers.SlotGuardStats
	if rsp, ok := childSnap.provider.(providers.RunSlotStatsProvider); ok {
		s := rsp.RunSlotStats()
		if s.Admitted > 0 || s.Waits > 0 || s.TotalWaitMS > 0 {
			childSlotStats = &s
		}
	}
	o.emitChildCompleted(childCtx, childScope, childRunID, def.Name, rec, childStart, childSlotStats)

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
				"agent": agentName, "status": string(out.status), "turns": turns, "tool_calls": toolCalls, "denied": denied,
			},
		}
	case EventError:
		return tools.Result{IsError: true, Content: head + "\nchild run failed: " + scrubbedContent(out.errText)}
	case EventBlocked:
		return tools.Result{IsError: true, Content: head + "\nchild run blocked: " + scrubbedContent(out.errText)}
	case EventCancelled:
		return tools.Result{IsError: true, Content: head + "\ndelegation cancelled" + rec.budgetNote()}
	default:
		return tools.Result{IsError: true, Content: head + "\nunknown child terminal event"}
	}
}

// outcomeSnapshot copies the single terminal record plus counter
// totals for the event log. Denied totals fold the per-tool map.
func (r *childRunRecorder) outcomeSnapshot() (out childOutcome, seen, turns, toolCalls, failed, denied int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out = r.out
	seen = boolToInt(r.seen)
	turns = r.turns
	toolCalls = r.toolCalls
	failed = r.failed
	for _, n := range r.denied {
		denied += n
	}
	return out, seen, turns, toolCalls, failed, denied
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// emitChildCompleted records the child's final outcome as one
// structured event. Terminal state is whatever the recorder first
// observed (or the defensive cancellation record) — never recomposed
// from the report body.
func (o *Orchestrator) emitChildCompleted(childCtx context.Context, childScope *orchestrationScope, childRunID, agentName string, rec *childRunRecorder, started time.Time, slotStats *providers.SlotGuardStats) {
	out, seen, turns, toolCalls, failed, denied := rec.outcomeSnapshot()
	ev := orchevents.Event{
		Type:       orchevents.ChildCompleted,
		Agent:      agentName,
		ChildRunID: childRunID,
		Turns:      turns,
		ToolCalls:  toolCalls,
		Failed:     failed,
		Denied:     denied,
		DurationMS: time.Since(started).Milliseconds(),
	}
	meta := map[string]any{}
	if slotStats != nil {
		meta["slot_guard_admitted"] = slotStats.Admitted
		meta["slot_guard_waits"] = slotStats.Waits
		meta["slot_wait_ms"] = slotStats.TotalWaitMS
	}
	if out.response != nil {
		if out.response.Usage.CachedTokens > 0 {
			meta["cached_tokens"] = out.response.Usage.CachedTokens
		}
		if out.response.Usage.Timings.CacheN > 0 {
			meta["cache_n"] = out.response.Usage.Timings.CacheN
		}
		if out.response.Usage.Timings.PredictedPerSecond > 0 {
			meta["predicted_tps"] = out.response.Usage.Timings.PredictedPerSecond
		}
	}
	if len(meta) > 0 {
		ev.Meta = meta
	}
	switch {
	case seen == 0:
		ev.Outcome = orchevents.OutcomeError
		ev.Error = "child run ended without a terminal event"
	case out.terminal == EventDone:
		ev.Outcome = orchevents.OutcomeDone
		ev.Status = string(out.status)
	case out.terminal == EventError:
		ev.Outcome = orchevents.OutcomeError
		ev.Error = out.errText
	case out.terminal == EventBlocked:
		ev.Outcome = orchevents.OutcomeBlocked
		ev.Error = out.errText
	default: // EventCancelled
		ev.Outcome = orchevents.OutcomeCancelled
		ev.Error = out.errText
	}
	o.emitChildRun(childCtx, childScope, ev)
}

// budgetNote appends the honest reason when this child's own
// cancellation came from the aggregate child-turn budget.
func (r *childRunRecorder) budgetNote() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.overBudget {
		return ": aggregate child-turn budget exhausted - the orchestrator stopped this child"
	}
	return ""
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
