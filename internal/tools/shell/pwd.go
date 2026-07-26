// Package shell provides built-in tools for reading (never mutating)
// shell/process state.
package shell

import (
	"context"
	"fmt"
	"os"

	"forcefield/internal/tools"
)

// PWD reports the process's current working directory.
type PWD struct{}

// NewPWD returns a ready-to-register PWD tool.
func NewPWD() *PWD { return &PWD{} }

func (PWD) Name() string { return "pwd" }
func (PWD) Description() string {
	return "Return the current working directory of the Forcefield process."
}

func (PWD) InputSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (PWD) Execute(_ context.Context, _ map[string]any) (tools.Result, error) {
	wd, err := os.Getwd()
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot determine working directory: %v", err)}, nil
	}

	return tools.Result{Content: wd}, nil
}
