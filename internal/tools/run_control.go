package tools

import (
	"context"
	"sync"
)

// RunControl lets tools register cancellation cleanup that must finish before
// an agent run is considered stopped. It is deliberately tiny and lives in
// the tools package so runtime can coordinate cleanup without tools knowing
// about runtime internals.
type RunControl struct {
	ctx context.Context
	wg  sync.WaitGroup
}

func NewRunControl(ctx context.Context) *RunControl {
	return &RunControl{ctx: ctx}
}

// Done is closed when the owning run is cancelled.
func (r *RunControl) Done() <-chan struct{} {
	if r == nil || r.ctx == nil {
		return nil
	}
	return r.ctx.Done()
}

// Add registers cleanup and returns its idempotent completion function.
// Callers register before starting a background process, then invoke the
// returned function after that process has actually exited and its pipes are
// released.
func (r *RunControl) Add() func() {
	if r == nil {
		return func() {}
	}
	r.wg.Add(1)
	var once sync.Once
	return func() {
		once.Do(r.wg.Done)
	}
}

// Wait blocks until every registered cancellation cleanup has completed.
func (r *RunControl) Wait() {
	if r != nil {
		r.wg.Wait()
	}
}

type runControlKey struct{}

// WithRunControl attaches control to a run/tool context. Derived timeout
// contexts preserve this value, so a background-capable tool can listen to
// whole-run cancellation rather than its short-lived per-attempt timeout.
func WithRunControl(ctx context.Context, control *RunControl) context.Context {
	if ctx == nil || control == nil {
		return ctx
	}
	return context.WithValue(ctx, runControlKey{}, control)
}

// RunControlFromContext returns the run cleanup coordinator, when a tool is
// executing as part of an agent run.
func RunControlFromContext(ctx context.Context) *RunControl {
	if ctx == nil {
		return nil
	}
	control, _ := ctx.Value(runControlKey{}).(*RunControl)
	return control
}
