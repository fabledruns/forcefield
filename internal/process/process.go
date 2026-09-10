// Package process owns Forcefield's process-tree lifecycle: every
// external command Forcefield spawns must die as a tree — not just the
// direct child — when its context is cancelled, its timeout fires, or
// Forcefield itself goes away.
//
// Two complementary mechanisms cover the tree, because neither alone
// closes every window:
//
//   - Kill (synchronous tree kill at cancel/timeout time) terminates
//     everything enumerated at that moment: taskkill /T /F on Windows,
//     SIGKILL to the process group on Unix.
//   - Track (Windows Job Object with KILL_ON_JOB_CLOSE, released after
//     Wait) is the backstop: the OS kills the whole tree — including
//     processes spawned between Start and Track, or after Kill ran — no
//     matter how Forcefield itself dies (return, panic, SIGKILL; only a
//     reboot or power loss escapes it). It is also what finally reaps a
//     tree when the harness is killed outright.
//
// On Unix, Track is a no-op: process-group membership is established
// before Start (Configure) and inherited reliably, so Kill already
// reaches the full tree including future grandchildren.
//
// Scope and non-goals: this package manages OS processes Forcefield
// spawns directly (shell foreground/background jobs, supervised
// children). Processes inside the WSL distribution are Linux processes,
// not Windows children: killing the wsl.exe relay does not reach them,
// and no Windows primitive can — that residue is documented in
// internal/sandbox, not fixed here. Short-lived probers with no known
// descendants (version checks, health probes, read-only git) intentionally
// skip Track: the direct-child kill already covers them.
package process

// ReleaseFunc releases process-tree tracking acquired by Track. It is
// idempotent and must be called once the child has been reaped.
type ReleaseFunc func()
