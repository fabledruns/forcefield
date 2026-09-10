// Package recovery holds the Phase 0/1 run-recovery contract and the
// headless session driver.
//
// The session file is the only persistent run state Forcefield keeps:
// the runtime loop is stateless across processes and the TUI used to be
// the sole persister of its event stream. This package extracts that
// event→session persistence behavior verbatim so headless resume
// (`ff run --resume`) and the interactive TUI share one implementation
// with identical crash semantics:
//
//  1. Assistant batch + pending tool calls persist BEFORE tools run.
//  2. Tool results + pending-call resolution persist together.
//  3. Turn close (EndTurn) persists on every terminal event.
//  4. Adoption heals stranded turns via the existing session recovery.
//  5. Replay reads ProviderMessages from the persisted session.
//  6. Recorded calls are never re-executed.
//
// Crash honesty is load-bearing: if the process dies after a tool side
// effect but before its result persists, recovery marks the call
// interrupted and synthesizes a cancelled result. It never claims to
// know whether the side effect happened, never re-executes recorded
// calls, and offers no exactly-once guarantee. Side effects are
// at-most-once recorded and may remain at-least-once executed.
//
// There is no supervisor here on purpose: exit codes (exit.go) tell a
// future supervisor what happened, and Budget (budget.go) bounds how
// often it may retry. Nothing in this package restarts anything.
package recovery
