package shell

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	"forcefield/internal/process"
	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// Background job bounds. Jobs are in-process only: a crashed Forcefield
// cannot reattach to them, and every job carries an absolute deadline
// (never beyond tools.MaxTimeout), so even an orphaned process dies on
// its own within minutes. No job state touches disk.
const (
	// maxActiveJobs caps concurrently running jobs. Starting beyond it
	// fails soft with the limit named instead of queueing.
	maxActiveJobs = 4
	// maxRetainedJobs caps remembered terminal job records; older ones
	// are evicted first-in-first-out.
	maxRetainedJobs = 32
	// defaultJobTTL prunes jobs (killing them if still running) that no
	// turn has polled, listed, or cancelled within the window.
	defaultJobTTL = 10 * time.Minute
	// defaultPollTailLines bounds one poll's output section.
	defaultPollTailLines = 100
)

// ErrTooManyJobs reports a start refused by the active-job cap. Tools
// map it to a soft error so the model can poll or cancel first.
var ErrTooManyJobs = fmt.Errorf("too many running jobs")

// ErrUnknownJob reports poll/cancel for an ID the registry never held
// (or already pruned). Tools map it to a soft error; it never guesses.
var ErrUnknownJob = fmt.Errorf("unknown job")

// JobState is a background job's lifecycle state. Terminal states are
// final; only running jobs still hold a process.
type JobState string

const (
	JobRunning   JobState = "running"
	JobDone      JobState = "done"
	JobFailed    JobState = "failed"
	JobCancelled JobState = "cancelled"
	JobTimeout   JobState = "timeout"
)

func (s JobState) terminal() bool { return s != JobRunning }

// Job is one background shell command.
type Job struct {
	ID        string
	Command   string
	Cwd       string
	StartedAt time.Time

	mu       sync.Mutex
	state    JobState
	exitCode int
	hasExit  bool
	output   *shellOutput
	note     string
	accessed time.Time
	kill     func()
	done     chan struct{}
	// reaped closes only after cmd.Wait has returned and both output pipes
	// have been drained/closed. It is stricter than done, which records the
	// user-visible terminal state as soon as cancellation claims it.
	reaped   chan struct{}
	timer    *time.Timer
	cleanups []func()
}

// Snapshot is a pollable, race-free view of a job.
type Snapshot struct {
	ID        string
	Command   string
	Cwd       string
	State     JobState
	ExitCode  int
	HasExit   bool
	Output    string
	Truncated bool
	Note      string
	Started   time.Time
}

// JobRegistry tracks background shell jobs for one tool manager's
// lifetime. It is safe for concurrent use and holds no persistent
// state: jobs live and die with the process.
type JobRegistry struct {
	mu       sync.Mutex
	jobs     map[string]*Job
	seq      int
	executor sandbox.Executor
	// execOnce guards lazy default-executor construction, mirroring Shell.
	execOnce sync.Once
	// maxOutput caps one job's captured stdout+stderr.
	maxOutput int
	// ttl prunes jobs no turn has touched within the window.
	ttl time.Duration
}

// NewJobRegistry returns an empty registry. A nil executor resolves
// lazily to native execution on first use.
func NewJobRegistry(executor sandbox.Executor) *JobRegistry {
	return &JobRegistry{jobs: make(map[string]*Job), executor: executor}
}

func (r *JobRegistry) executorFor() sandbox.Executor {
	r.execOnce.Do(func() {
		if r.executor != nil {
			return
		}
		e, err := sandbox.NewExecutor(sandbox.DefaultPolicy())
		if err != nil {
			panic("shell jobs: default policy must always construct: " + err.Error())
		}
		r.executor = e
	})
	return r.executor
}

func (r *JobRegistry) outputCap() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.maxOutput > 0 {
		return r.maxOutput
	}
	return tools.DefaultJobMaxBytes
}

func (r *JobRegistry) jobTTL() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ttl > 0 {
		return r.ttl
	}
	return defaultJobTTL
}

// setMaxOutput overrides the per-job output cap (tool configuration).
func (r *JobRegistry) setMaxOutput(n int) {
	if n <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxOutput = n
}

// Start launches command in the background and returns immediately.
// The working directory is validated against the workspace policy
// before anything spawns (same boundary as foreground shell). The job
// outlives the starting tool call by design; it ends on completion,
// its absolute deadline, cancellation, or idle TTL — never silently.
func (r *JobRegistry) Start(ctx context.Context, command, cwd string, env []string, timeout time.Duration) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = tools.DefaultJobTimeout
	}
	timeout = tools.ClampTimeout(timeout, tools.DefaultJobTimeout)

	r.mu.Lock()
	active := 0
	for _, j := range r.jobs {
		j.mu.Lock()
		running := !j.state.terminal()
		j.mu.Unlock()
		if running {
			active++
		}
	}
	if active >= maxActiveJobs {
		r.mu.Unlock()
		return nil, fmt.Errorf("%w (%d max); poll or cancel one first", ErrTooManyJobs, maxActiveJobs)
	}
	r.seq++
	id := fmt.Sprintf("job-%d", r.seq)
	r.mu.Unlock()

	r.sweep()

	var completeRunCleanup func()
	if control := tools.RunControlFromContext(ctx); control != nil {
		completeRunCleanup = control.Add()
	}
	cleanupRegistration := func() {
		if completeRunCleanup != nil {
			completeRunCleanup()
		}
	}

	prepared, err := r.executorFor().Prepare(ctx, sandbox.Request{
		Command:  command,
		Dir:      cwd,
		ExtraEnv: env,
	})
	if err != nil {
		cleanupRegistration()
		return nil, err
	}
	cmd := prepared.Cmd
	cmd.Stdin = nil
	process.Configure(cmd)
	cmd.Cancel = func() error { return process.Kill(cmd) }
	cmd.WaitDelay = waitDelay

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		cleanupRegistration()
		return nil, fmt.Errorf("shell job: create stdout pipe: %w", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		cleanupRegistration()
		return nil, fmt.Errorf("shell job: create stderr pipe: %w", err)
	}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	if err := cmd.Start(); err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
		if prepared.Cleanup != nil {
			prepared.Cleanup()
		}
		cleanupRegistration()
		return nil, fmt.Errorf("shell job: start command: %w", err)
	}
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()

	// Track the tree for the job's lifetime; released exactly once by
	// the finish path alongside the other cleanups below.
	release := process.Track(cmd)

	now := time.Now()
	job := &Job{
		ID:        id,
		Command:   command,
		Cwd:       prepared.Dir,
		StartedAt: now,
		state:     JobRunning,
		output:    &shellOutput{max: r.outputCap()},
		accessed:  now,
		done:      make(chan struct{}),
		reaped:    make(chan struct{}),
		kill:      func() { _ = process.Kill(cmd) },
	}
	job.cleanups = append(job.cleanups, func() { release() })
	if prepared.Cleanup != nil {
		job.cleanups = append(job.cleanups, prepared.Cleanup)
	}

	r.mu.Lock()
	r.jobs[id] = job
	r.mu.Unlock()

	pipeDone := make(chan struct{}, 2)
	pipesFinished := make(chan struct{})
	go func() {
		<-pipeDone
		<-pipeDone
		close(pipesFinished)
	}()
	go streamPipe(stdoutReader, "stdout", job.output, nil, pipeDone)
	go streamPipe(stderrReader, "stderr", job.output, nil, pipeDone)

	// Absolute deadline: even a forgotten or orphaned job dies on time.
	// terminate() claims the timeout status before killing, so the
	// outcome is deterministic even though the kill itself may block
	// while the waiter wakes.
	job.timer = time.AfterFunc(timeout, func() {
		job.terminate(JobTimeout, -1, fmt.Sprintf("exceeded %s time limit", timeout))
	})

	go func() {
		defer close(job.reaped)
		waitErr := cmd.Wait()
		// Bounded drain so an inherited pipe handle cannot wedge the
		// reaper; then close readers to unblock the streamers.
		select {
		case <-pipesFinished:
		case <-time.After(waitDelay):
			_ = stdoutReader.Close()
			_ = stderrReader.Close()
			<-pipesFinished
		}
		_ = stdoutReader.Close()
		_ = stderrReader.Close()
		code := 0
		state := JobDone
		if waitErr != nil {
			state = JobFailed
			code = -1
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			}
		}
		job.finish(state, code, "")
	}()

	if control := tools.RunControlFromContext(ctx); control != nil {
		go func() {
			defer cleanupRegistration()
			select {
			case <-control.Done():
				job.terminate(JobCancelled, -1, "cancelled with agent run")
				<-job.reaped
			case <-job.reaped:
			}
		}()
	} else {
		// No agent-run owner (for example a direct tool test): there is no
		// cancellation coordinator to wait on.
		cleanupRegistration()
	}

	snap := job.snapshot()
	return &snap, nil
}

// terminate kills the process group (best effort) and records the
// terminal outcome. The status is claimed BEFORE killing so the outcome
// is deterministic even when the waiter's exit races the kill: exactly
// one outcome wins and cleanup runs once. The deadline timer is left
// running as a backstop (it fires into a no-op once terminal).
func (j *Job) terminate(state JobState, code int, note string) {
	won, timer, cleanups, done, kill := j.claimTerminal(state, code, note)
	if !won {
		return
	}
	_ = timer // backstop stays armed; see above
	if kill != nil {
		kill()
	}
	for _, fn := range cleanups {
		fn()
	}
	select {
	case <-done:
	default:
		close(done)
	}
}

// Poll returns the job's current snapshot and marks it accessed
// (refreshing its idle TTL). Unknown IDs are an error, never a guess.
func (r *JobRegistry) Poll(id string) (Snapshot, error) {
	r.sweep()
	r.mu.Lock()
	j, ok := r.jobs[id]
	r.mu.Unlock()
	if !ok {
		return Snapshot{}, fmt.Errorf("%w %q", ErrUnknownJob, id)
	}
	j.mu.Lock()
	j.accessed = time.Now()
	j.mu.Unlock()
	return j.snapshot(), nil
}

// Cancel terminates a running job and marks it cancelled. Terminal jobs
// report their recorded state (idempotent).
func (r *JobRegistry) Cancel(id string) (Snapshot, error) {
	r.sweep()
	r.mu.Lock()
	j, ok := r.jobs[id]
	r.mu.Unlock()
	if !ok {
		return Snapshot{}, fmt.Errorf("%w %q", ErrUnknownJob, id)
	}
	j.terminate(JobCancelled, -1, "cancelled on request")
	return j.snapshot(), nil
}

// List snapshots every remembered job, oldest first. The list itself is
// bounded by retention.
func (r *JobRegistry) List() []Snapshot {
	r.sweep()
	r.mu.Lock()
	all := make([]*Job, 0, len(r.jobs))
	for _, j := range r.jobs {
		all = append(all, j)
	}
	r.mu.Unlock()
	sort.Slice(all, func(i, j int) bool { return all[i].StartedAt.Before(all[j].StartedAt) })
	out := make([]Snapshot, 0, len(all))
	for _, j := range all {
		out = append(out, j.snapshot())
	}
	return out
}

// sweep prunes jobs idle past the TTL (terminating the running ones)
// and evicts terminal records beyond retention. It runs on every
// registry operation, so no background goroutine is needed.
func (r *JobRegistry) sweep() {
	ttl := r.jobTTL()
	now := time.Now()
	r.mu.Lock()
	var reap []*Job
	for id, j := range r.jobs {
		j.mu.Lock()
		idle := now.Sub(j.accessed)
		running := !j.state.terminal()
		j.mu.Unlock()
		if idle <= ttl {
			continue
		}
		if running {
			reap = append(reap, j)
		}
		delete(r.jobs, id)
	}
	// Retention: keep only the newest terminal records.
	type rec struct {
		id      string
		started time.Time
	}
	var terminal []rec
	for id, j := range r.jobs {
		j.mu.Lock()
		done := j.state.terminal()
		started := j.StartedAt
		j.mu.Unlock()
		if done {
			terminal = append(terminal, rec{id: id, started: started})
		}
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].started.Before(terminal[j].started) })
	if len(terminal) > maxRetainedJobs {
		for _, rec := range terminal[:len(terminal)-maxRetainedJobs] {
			delete(r.jobs, rec.id)
		}
	}
	r.mu.Unlock()
	for _, j := range reap {
		j.terminate(JobCancelled, -1, "reaped after idle TTL with no poll")
	}
}

// finish records the terminal outcome exactly once; concurrent paths
// (exit, deadline, cancel, TTL reap) race to claim and the rest are
// no-ops. Only the natural-exit path stops the deadline timer (external
// terminations leave it armed as a backstop).
func (j *Job) finish(state JobState, code int, note string) {
	won, timer, cleanups, done, _ := j.claimTerminal(state, code, note)
	if !won {
		return
	}
	if timer != nil {
		timer.Stop()
	}
	for _, fn := range cleanups {
		fn()
	}
	select {
	case <-done:
	default:
		close(done)
	}
}

// claimTerminal sets the terminal outcome if the job is still running,
// reporting whether this caller won. The winner releases the timer
// handle, staging cleanups, kill closure, and done channel exactly once.
func (j *Job) claimTerminal(state JobState, code int, note string) (won bool, timer *time.Timer, cleanups []func(), done chan struct{}, kill func()) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state.terminal() {
		return false, nil, nil, nil, nil
	}
	j.state = state
	j.exitCode = code
	j.hasExit = code >= 0
	if note != "" {
		j.note = note
	}
	timer, cleanups, done, kill = j.timer, j.cleanups, j.done, j.kill
	j.timer, j.cleanups = nil, nil
	return true, timer, cleanups, done, kill
}

func (j *Job) snapshot() Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	stdout := j.output.stdoutString()
	stderr := j.output.stderrString()
	out := stdout
	if stderr != "" {
		if out == "" {
			out = "stderr:\n" + stderr
		} else {
			out += "\nstderr:\n" + stderr
		}
	}
	if j.output.isTruncated() {
		out += "\n[...job output truncated; poll tail_lines for slices]"
	}
	return Snapshot{
		ID: j.ID, Command: j.Command, Cwd: j.Cwd,
		State: j.state, ExitCode: j.exitCode, HasExit: j.hasExit,
		Output: out, Truncated: j.output.isTruncated(), Note: j.note, Started: j.StartedAt,
	}
}

// Close terminates every running job; terminal records stay readable
// for the process lifetime.
func (r *JobRegistry) Close() {
	r.mu.Lock()
	jobs := make([]*Job, 0, len(r.jobs))
	for _, j := range r.jobs {
		jobs = append(jobs, j)
	}
	r.mu.Unlock()
	for _, j := range jobs {
		j.terminate(JobCancelled, -1, "registry closed")
	}
}
