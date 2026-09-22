// Package process owns Forcefield's process-tree lifecycle: cancellation
// or timeout kills the whole tree via Kill, with Track as the Windows Job
// Object backstop. See docs/Recovery.md. WSL-distribution processes
// outlive the wsl.exe relay by platform design (see internal/sandbox).
package process

// ReleaseFunc releases process-tree tracking acquired by Track. It is
// idempotent and must be called once the child has been reaped.
type ReleaseFunc func()
