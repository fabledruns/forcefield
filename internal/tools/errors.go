package tools

import (
	"errors"
	"fmt"
)

// ErrNotFound is wrapped into the error Manager.Execute returns when
// asked to run a tool name that isn't registered.
var ErrNotFound = errors.New("tool not found")

// ErrAlreadyRegistered is wrapped into the error Registry.Register
// returns when a tool name is already taken.
var ErrAlreadyRegistered = errors.New("tool already registered")

// ArgumentError reports a problem with a single argument passed to a tool
type ArgumentError struct {
	Field  string
	Reason string
}

func (e *ArgumentError) Error() string {
	return fmt.Sprintf("argument %q %s", e.Field, e.Reason)
}

// ExecutionError wraps an error returned by a Tool's Execute method with the tool's name
type ExecutionError struct {
	Tool string
	Err  error
}

func (e *ExecutionError) Error() string {
	return fmt.Sprintf("tool %q: %v", e.Tool, e.Err)
}

func (e *ExecutionError) Unwrap() error {
	return e.Err
}
