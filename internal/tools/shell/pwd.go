// Package shell provides built-in tools for reading (never mutating)
// shell/process state.
package shell

import (
	"context"
	"fmt"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// PWD reports the current working directory of the active workspace.
type PWD struct {
	policy sandbox.Policy
}

// NewPWD returns a ready-to-register PWD tool using the default policy.
func NewPWD() *PWD { return &PWD{} }

// NewPWDWithPolicy returns a PWD tool bound to policy.Workspace.
func NewPWDWithPolicy(p sandbox.Policy) *PWD { return &PWD{policy: p} }

func (PWD) Name() string { return "pwd" }
func (PWD) Description() string {
	return "Return the current working directory of the active workspace."
}

func (PWD) InputSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (p PWD) Execute(ctx context.Context, _ map[string]any) (tools.Result, error) {
	if err := sandbox.CheckCtx(ctx); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot determine working directory: %v", err)}, nil
	}

	resolved, err := sandbox.ResolveWithinWorkspace(p.policy.Workspace, "")
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot determine working directory: %v", err)}, nil
	}

	return tools.Result{Content: resolved}, nil
}
