//go:build !linux

package landlock

import "errors"

// ErrUnsupported is returned by every Landlock operation off Linux.
// Landlock is a Linux-only mechanism; other platforms must not claim
// support, mirroring sandbox.ErrUnsupported for the wsl backend.
var ErrUnsupported = errors.New("landlock requires Linux")

// QueryABI reports Landlock unavailable off Linux without probing.
func QueryABI() (int, error) { return -1, ErrUnsupported }

// SetNoNewPrivs reports Landlock unavailable off Linux.
func SetNoNewPrivs() error { return ErrUnsupported }

// InstallRules reports Landlock unavailable off Linux.
func InstallRules(Policy) error { return ErrUnsupported }

// ApplyPolicy reports Landlock unavailable off Linux.
func ApplyPolicy(Policy) error { return ErrUnsupported }
