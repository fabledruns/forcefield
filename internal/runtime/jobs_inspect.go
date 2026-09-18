package runtime

import (
	"forcefield/internal/tools/shell"
)

// JobSnapshots returns read-only snapshots of every remembered background
// shell job, oldest first. It exposes the shell_job registry's List view
// without starting, polling, or cancelling anything, so /jobs can report
// without affecting job lifecycle guarantees.
func (r *Runtime) JobSnapshots() []shell.Snapshot {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	m := r.fullManager
	r.mu.RUnlock()
	if m == nil {
		return nil
	}
	t, ok := m.Lookup("shell_job")
	if !ok {
		return nil
	}
	job, ok := t.(*shell.ShellJob)
	if !ok || job == nil {
		return nil
	}
	reg := job.Registry()
	if reg == nil {
		return nil
	}
	return reg.List()
}
