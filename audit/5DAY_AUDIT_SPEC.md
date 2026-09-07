# Forcefield Deep Engineering Audit Specification

**Document:** `AUDIT_SPEC.md`
**Project:** Forcefield
**Audit Type:** Full-stack engineering, security, reliability, architecture, UX, and long-horizon autonomy audit
**Target Runtime:** 5-day unattended agent tasks
**Auditor:** Muse Spark 1.2
**Mode:** Read-only investigation
**Repository Changes:** Forbidden

---

## 1. Purpose

This audit exists to determine whether Forcefield is actually ready to operate as a reliable local-first agent harness rather than merely appearing functional during short development sessions.

The audit must investigate the repository from multiple perspectives:

* Security
* Agent/tool safety
* Reliability
* Long-horizon execution
* Provider/API resilience
* Rate-limit resilience
* Context management
* Session persistence
* Process management
* Concurrency
* Performance
* Architecture
* Code quality
* Maintainability
* Testing
* Cross-platform behavior
* TUI/UX
* Configuration
* Documentation
* Repository hygiene

The goal is not to produce a generic code review.

The goal is to answer:

> **"If I start a serious autonomous task in Forcefield and leave it running for approximately five days, what can realistically go wrong, why would it happen, and how confident should I be that Forcefield will recover?"**

The audit must also determine whether the current architecture can support future growth without accumulating structural problems.

---

# 2. Audit Principles

The auditor MUST follow these principles.

### 2.1 Evidence over assumptions

Do not report an issue simply because something looks unusual.

Every finding should be supported by one or more of:

* Direct source-code evidence
* Test evidence
* Runtime reproduction
* Static-analysis output
* Dependency information
* Configuration evidence
* Architecture tracing
* Documented behavior

If an issue cannot be confirmed, explicitly label it as a hypothesis.

### 2.2 Severity over quantity

Finding 100 minor style issues is less useful than finding one race condition that can corrupt a long-running session.

Prioritize issues based on actual impact.

### 2.3 Trace root causes

Do not stop at symptoms.

For example:

Bad:

> "Session resume sometimes crashes."

Good:

> "Session resume can panic because `X()` assumes `Y` is non-nil after deserialization, but the session migration path does not populate `Y`. This affects sessions created before version X and can terminate the entire TUI process."

### 2.4 Do not confuse preference with defect

Do not classify subjective preferences as bugs.

For example:

> "I would structure this package differently."

is not a finding.

Instead:

> "Package A imports B, C, and D and owns responsibilities from three separate layers, making provider replacement require changes across unrelated components."

is a legitimate architectural finding.

### 2.5 Inspect before judging

Understand the system before criticizing individual files.

The auditor should first build a mental model of:

```text
User
 ↓
TUI / CLI
 ↓
Session
 ↓
Agent loop
 ↓
Model provider
 ↓
Model response
 ↓
Tool selection
 ↓
Permission layer
 ↓
Tool execution
 ↓
Tool result
 ↓
Agent loop
```

Then trace how state, errors, cancellation, and persistence move through the system.

---

# 3. Audit Scope

The entire repository is in scope unless explicitly excluded.

Inspect at minimum:

* Application source
* CLI
* TUI
* Agent loop
* Providers
* Model adapters
* Tools
* Permission system
* Shell execution
* Session management
* Memory
* Configuration
* Persistence
* Networking
* Streaming
* Retry logic
* Error handling
* Logging
* Tests
* Build scripts
* CI configuration
* Dependencies
* Documentation
* Examples
* Scripts
* Generated code where relevant
* Platform-specific code

Do not limit the audit to `.go` files.

---

# 4. Phase 0: Repository Reconnaissance

Before making findings, map the repository.

Identify:

* Entry points
* Packages
* Modules
* Major types
* Major interfaces
* Persistent data structures
* External integrations
* Providers
* Tool implementations
* TUI components
* Configuration sources
* Test locations
* Build/test commands

Produce an architectural map.

Example:

```text
cmd/
internal/
pkg/
providers/
tools/
session/
memory/
tui/
config/
tests/
```

The actual structure must come from the repository.

Do not invent architecture.

---

# 5. Phase 1: Runtime Architecture Reconstruction

Reconstruct the actual execution lifecycle.

Document:

### Startup

```text
process startup
→ config loading
→ provider initialization
→ tool registration
→ session initialization
→ TUI/CLI initialization
```

### Agent request

```text
user input
→ session update
→ context construction
→ provider request
→ streaming
→ model output parsing
→ tool detection
```

### Tool execution

```text
tool request
→ validation
→ permission decision
→ execution
→ output collection
→ result normalization
→ session update
→ next model request
```

### Shutdown

```text
cancel
→ agent cancellation
→ tool cancellation
→ process cleanup
→ session persistence
→ TUI shutdown
→ process exit
```

Identify where reality differs from this conceptual flow.

---

# 6. Phase 2: Security Audit

Forcefield must be treated as a privileged agent execution environment.

The model must be considered untrusted.

Audit all paths where model-generated information can affect:

* Shell commands
* Filesystem paths
* Files
* Processes
* Environment variables
* Network requests
* Configuration
* Sessions
* Tool arguments
* Provider requests

Investigate:

* Command injection
* Argument injection
* Path traversal
* Symlink attacks
* Arbitrary file access
* Arbitrary process execution
* Environment leakage
* Credential exposure
* Unsafe deserialization
* Unsafe parsing
* Permission bypasses
* TOCTOU vulnerabilities
* Race conditions
* Workspace escapes
* Dangerous defaults
* Secret leakage through logs
* Secret leakage through model context
* Secret leakage through tool output
* Session data exposure

For every security boundary, ask:

> Can an untrusted model output cross this boundary and cause unintended behavior?

---

# 7. Agent Security Model

Audit the complete:

```text
Model
 ↓
Tool Call
 ↓
Validation
 ↓
Permission
 ↓
Execution
```

pipeline.

Determine whether validation occurs:

1. Before parsing
2. After parsing
3. Before permission
4. After permission
5. Before execution

Identify whether there are gaps between those stages.

Test conceptual attacks including:

* Malformed tool calls
* Unexpected arguments
* Extra arguments
* Missing arguments
* Dangerous paths
* Relative paths
* Absolute paths
* Symlinks
* Shell metacharacters
* Environment manipulation
* Prompt injection from files
* Prompt injection from tool output
* Malicious repository contents

Do not provide exploit instructions in the final report if doing so would create unnecessary risk. Describe the vulnerability and impact clearly.

---

# 8. Permission System Audit

The permission system receives special scrutiny.

Determine:

* What actions require permission
* How permissions are represented
* How decisions are persisted
* Whether permissions are scoped
* Whether permissions expire
* Whether permissions can be bypassed
* Whether approval applies to one action or many
* Whether the displayed action matches the actual action
* Whether users can understand what they are approving
* Whether dangerous actions are visually distinct
* Whether keyboard navigation works correctly
* Whether Enter confirms the selected option
* Whether arrow keys change the selected option
* Whether mouse interaction works where supported
* Whether cancellation is safe
* Whether default selections are safe

The UI must not expose raw internal representations when a user-facing representation is possible.

---

# 9. Provider and API Audit

Every provider implementation must be inspected independently.

For each provider determine:

* Request construction
* Authentication
* Headers
* Streaming
* Timeouts
* Cancellation
* Error handling
* Retry behavior
* Rate-limit handling
* Backoff
* Connection handling
* Response parsing
* Partial response behavior
* Context limits
* Output limits
* Provider-specific quirks

Create a provider resilience matrix.

| Failure              | Expected behavior | Actual behavior | Safe? |
| -------------------- | ----------------- | --------------- | ----- |
| HTTP 429             | Backoff           | ...             | ...   |
| HTTP 500             | Retry             | ...             | ...   |
| Timeout              | Recover/cancel    | ...             | ...   |
| Stream disconnect    | Recover           | ...             | ...   |
| Malformed response   | Fail safely       | ...             | ...   |
| Invalid credentials  | Stop clearly      | ...             | ...   |
| Provider unavailable | Recover/fallback  | ...             | ...   |

---

# 10. Rate-Limit Audit

Determine whether Forcefield can survive five days without accidentally destroying its own availability through excessive requests.

Calculate or estimate:

* Requests per agent turn
* Requests per tool cycle
* Retry amplification
* Maximum retry frequency
* Backoff duration
* Context growth
* Token growth
* Tool-loop amplification

Inspect for:

```text
429
 ↓
retry
 ↓
429
 ↓
retry
 ↓
429
 ↓
...
```

Identify retry storms.

Determine whether `Retry-After` is respected when available.

Determine whether retries are:

* bounded
* cancellable
* jittered
* idempotent where necessary
* provider-aware

---

# 11. Five-Day Long-Horizon Audit

This is a primary objective.

Evaluate Forcefield as a system that must operate continuously for approximately:

```text
5 days
= 120 hours
= 7,200 minutes
```

The audit must identify every resource that can grow with runtime.

Inspect:

* Memory
* Goroutines
* File descriptors
* Child processes
* Open connections
* Logs
* Session size
* Context size
* Tool output
* Temporary files
* Cache size
* Queues
* Buffers
* Database connections
* Redis connections
* Network connections

For each resource determine:

```text
Does it grow?
    ↓
Is growth bounded?
    ↓
Is it cleaned up?
    ↓
What happens at the limit?
```

---

# 12. Long-Horizon Failure Model

Model these scenarios:

### Provider failure

* 429
* 500
* 502
* 503
* Timeout
* Connection reset
* Stream interruption

### Local failure

* Tool crash
* Shell hang
* Process termination
* Filesystem error
* Disk full
* Permission change
* Missing executable

### Application failure

* Panic
* Deadlock
* Goroutine leak
* Memory exhaustion
* Corrupted state
* Unexpected shutdown

### Environment failure

* Terminal closes
* Network disappears
* Computer reboots
* Ollama stops
* LM Studio stops
* Environment variables change

For every scenario determine:

```text
Detection
Recovery
State preservation
User visibility
Retry behavior
Final outcome
```

---

# 13. Context Management Audit

Long-running agents often fail because context grows until the model becomes unusable.

Inspect:

* Conversation history
* Tool output
* System prompts
* Memory
* Session metadata
* Summaries
* Context compaction
* Token estimation
* Context truncation
* Tool-result truncation

Determine:

* Whether context growth is bounded
* What gets removed
* Whether important information can disappear
* Whether tool outputs dominate context
* Whether compaction is deterministic
* Whether compaction can fail
* Whether the agent can continue after compaction

Identify the approximate point where a 5-day task becomes impractical.

---

# 14. Session and Persistence Audit

Sessions must survive realistic failure.

Inspect:

* Serialization
* Deserialization
* Atomic writes
* File locking
* Versioning
* Migration
* Corruption handling
* Partial writes
* Concurrent access
* Resume behavior

Simulate:

```text
normal session
→ write
→ crash during write
→ restart
→ resume
```

Determine whether the session remains usable.

Also test:

```text
two Forcefield processes
→ same session
→ concurrent writes
```

if the architecture allows it.

---

# 15. Concurrency Audit

Inspect all concurrent code.

Look for:

* Shared mutable state
* Data races
* Unsynchronized maps
* Incorrect channel usage
* Deadlocks
* Goroutine leaks
* Incorrect cancellation
* Context misuse
* Shutdown races
* Concurrent session writes
* Provider stream races
* TUI state races

Run the race detector where practical.

Do not assume passing tests means race-free code.

---

# 16. Process and Shell Audit

Because Forcefield executes local commands, process management receives dedicated scrutiny.

Inspect:

* Process creation
* Working directory
* Environment
* stdin
* stdout
* stderr
* cancellation
* timeout
* process groups
* child processes
* cleanup
* output limits

Determine:

> What happens when a command never exits?

Determine:

> What happens when a command creates child processes?

Determine:

> What happens when Forcefield is terminated while a tool is running?

Determine:

> Can a failed tool leave background processes behind?

---

# 17. Performance Audit

Search for actual inefficiencies.

Inspect:

* Algorithmic complexity
* Allocations
* Copies
* Serialization
* Parsing
* Filesystem operations
* Network calls
* Lock contention
* TUI rendering
* Context construction
* Logging

Look specifically for unbounded work.

Prioritize issues using:

```text
Impact × Frequency × Runtime exposure
```

Do not recommend micro-optimizations that have no meaningful impact.

---

# 18. Code Quality Audit

Assess:

* Readability
* Maintainability
* Naming
* Complexity
* Coupling
* Cohesion
* Duplication
* Abstraction quality
* Error handling
* Testability

Identify:

* Dead code
* Dead interfaces
* Unused functions
* Unused fields
* Deprecated paths
* Commented-out implementations
* Temporary hacks
* TODO/FIXME debt

Classify each finding as:

```text
Bug
Security issue
Reliability issue
Architectural debt
Maintainability issue
Performance issue
Style only
```

Style-only issues should not dominate the report.

---

# 19. Architecture Audit

Evaluate whether the architecture supports:

* Additional providers
* Additional tools
* Additional permission policies
* Additional session backends
* Additional memory systems
* Additional UIs
* Long-running execution
* Testing
* Observability

Ask:

> What becomes painful if Forcefield grows 10x?

Identify architectural bottlenecks.

For each major architectural problem include:

```text
Current design
Why it exists
Observed problem
Impact
Better boundary
Migration difficulty
Priority
```

Do not recommend rewriting the entire system unless the evidence supports it.

---

# 20. TUI / UX Audit

Audit the application as a user would experience it.

Inspect:

* Message hierarchy
* Conversation grouping
* Tool-call presentation
* Tool results
* Errors
* Permission prompts
* Loading states
* Streaming
* Keyboard interaction
* Mouse interaction
* Selection
* Arrow navigation
* Enter behavior
* Escape behavior
* Scrolling
* Resizing
* Copy/paste
* Markdown
* Code blocks
* Long output
* Session switching
* Session resuming
* Configuration
* Help
* Empty states

Pay special attention to:

> Does this feel like a polished terminal application or like internal JSON/debug information dumped into a terminal?

Identify confusing or cognitively expensive interactions.

---

# 21. Cross-Platform Audit

Windows is a first-class target.

Inspect:

* PowerShell
* CMD
* Bash/WSL
* PATH resolution
* Executable discovery
* Filesystem semantics
* Signals
* Process termination
* File locking
* Paths
* Unicode
* ANSI
* Terminal behavior
* Environment variables
* Home directories
* Temporary directories

Test platform-specific behavior where possible.

---

# 22. Testing Audit

Determine whether tests provide meaningful confidence.

Audit:

* Unit tests
* Integration tests
* End-to-end tests
* Provider tests
* Tool tests
* TUI tests
* Session tests
* Concurrency tests
* Failure tests
* Recovery tests

Identify missing tests for:

* 429 responses
* Provider outages
* Stream failures
* Cancellation
* Tool hangs
* Session corruption
* Process crashes
* Disk failures
* Concurrent sessions
* Long-running execution
* Windows behavior
* Security boundaries

Run:

```text
go test ./...
go test -race ./...
```

where practical.

Also run configured lint/static-analysis/vulnerability tooling.

Do not modify code simply to make tests pass.

---

# 23. Dependency Audit

Inspect:

* Direct dependencies
* Transitive dependencies
* Version age
* Known vulnerabilities
* Abandoned libraries
* Unnecessary dependencies
* Duplicate functionality
* Heavy dependencies

Determine whether any dependency introduces meaningful operational or security risk.

---

# 24. Configuration Audit

Inspect:

* Defaults
* YAML
* Environment variables
* CLI flags
* Provider configuration
* Tool configuration
* Permission configuration

Ask:

> What happens when configuration is missing, malformed, partially specified, duplicated, or contradictory?

Look for dangerous defaults.

---

# 25. Observability Audit

Determine whether Forcefield can explain its own failures.

Audit:

* Logs
* Error messages
* Debugging output
* Provider errors
* Tool errors
* Session errors
* Request lifecycle visibility
* Task lifecycle visibility

A technically correct error that leaves the user unable to recover should be treated as a UX/reliability issue.

---

# 26. Documentation Audit

Compare documentation against actual behavior.

Look for:

* Broken commands
* Missing setup steps
* Incorrect assumptions
* Outdated architecture
* Undocumented configuration
* Undocumented limitations
* Missing troubleshooting
* Missing security warnings
* Missing provider limitations
* Features that exist but aren't documented
* Features documented but no longer implemented

---

# 27. Repository Hygiene Audit

Inspect:

* TODOs
* FIXMEs
* Debug statements
* Temporary files
* Build artifacts
* Generated artifacts
* Commented-out code
* Stale documentation
* Unused scripts
* Accidental secrets
* Test leftovers

Do not recommend deletion unless the artifact is genuinely unnecessary.

---

# 28. Static Analysis and Tooling

Use appropriate tooling already available in the environment.

Possible tools include:

```text
go test
go test -race
go vet
staticcheck
golangci-lint
govulncheck
gosec
```

Only use tools that are available or can be run safely.

Do not waste significant time installing an enormous toolchain for marginal benefit.

Record:

* Tool
* Version
* Command
* Result
* Relevant findings

---

# 29. Severity Model

Every confirmed finding must receive a severity.

### CRITICAL

A realistic issue that can:

* compromise the host
* bypass a major security boundary
* corrupt important persistent state
* make long-running execution fundamentally unsafe
* cause catastrophic repeated failures

### HIGH

A serious issue that can:

* crash Forcefield
* corrupt sessions
* bypass meaningful permissions
* cause major resource exhaustion
* make long-running tasks unreliable
* cause substantial data loss

### MEDIUM

A meaningful issue that:

* causes recurring failures
* reduces reliability
* creates significant maintenance cost
* causes noticeable UX problems
* creates performance problems under realistic workloads

### LOW

A limited issue with:

* narrow impact
* uncommon reproduction
* minor maintainability consequences
* minor UX friction

### INFORMATIONAL

Useful observation that does not represent a defect.

---

# 30. Confidence Model

Each finding must also have:

* Confirmed
* Highly likely
* Probable
* Possible
* Speculative

Never present speculation as fact.

---

# 31. Finding Format

Every finding must use this structure:

```text
ID:
Severity:
Confidence:
Category:

Title:

Location:
file:line

Summary:

Evidence:

Root Cause:

Impact:

Reproduction / Trigger:

Why Existing Protection Fails:

Recommended Direction:

Priority:

Related Findings:
```

Example:

```text
ID: FF-REL-014
Severity: HIGH
Confidence: CONFIRMED
Category: Reliability

Title:
Session writes can corrupt state during process termination.

Location:
internal/session/store.go:142

Summary:
...

Evidence:
...

Root Cause:
...

Impact:
...

Reproduction / Trigger:
...

Why Existing Protection Fails:
...

Recommended Direction:
...

Priority:
P0
```

---

# 32. Priority Model

Use:

### P0

Fix before serious autonomous usage.

### P1

Fix before production-scale usage.

### P2

Fix during normal hardening.

### P3

Nice-to-have improvement.

Security and long-horizon reliability findings should generally outrank cosmetic improvements.

---

# 33. Five-Day Reliability Score

Produce a final score from 0 to 100.

Score independently:

| Category                    | Weight |
| --------------------------- | -----: |
| Reliability                 |     15 |
| Provider resilience         |     10 |
| Rate-limit resilience       |     10 |
| Context management          |     10 |
| Session durability          |     10 |
| Process/resource management |     10 |
| Tool safety                 |     10 |
| Security                    |     10 |
| Recovery                    |      5 |
| Observability               |      5 |
| TUI/UX                      |      3 |
| Cross-platform reliability  |      2 |

Total:

```text
100
```

Explain every score.

Do not simply average subjective impressions.

---

# 34. Five-Day Verdict

The final report MUST explicitly answer:

## Can Forcefield safely run a five-day autonomous task?

Choose exactly one:

### YES

No known major blockers were identified.

### YES, WITH CONDITIONS

The system can plausibly run five days, but specific safeguards or operational constraints are required.

### NOT YET

Important reliability/security issues make unattended five-day operation unsafe or unreliable.

### NO

The current architecture or implementation fundamentally prevents reliable five-day execution.

Then explain why.

---

# 35. Maximum Expected Runtime

Estimate:

```text
Best case:
Expected case:
Failure-prone case:
```

Do not invent precision.

If the evidence only supports a qualitative estimate, say so.

Identify the first likely bottleneck:

```text
Provider rate limit
Context growth
Memory
Disk
Session size
Process leak
Network
Model availability
Application crash
Other
```

---

# 36. Long-Horizon Failure Budget

Estimate how many failures the system can survive before human intervention becomes necessary.

For example:

```text
Provider timeout: recoverable
Provider 429: recoverable
Tool failure: recoverable
Tool timeout: partially recoverable
Process crash: recoverable
Forcefield crash: resume uncertain
Machine reboot: unsupported
Session corruption: fatal
```

Use actual repository behavior.

---

# 37. Top 10 Problems

End the report with the ten most important problems.

Rank using:

```text
Severity
×
Likelihood
×
Impact
×
Long-horizon exposure
```

For each:

```text
Rank
ID
Problem
Impact
Why it matters
Recommended next step
```

---

# 38. Quick Wins

Identify improvements that provide unusually high value for low implementation effort.

Examples:

* Missing timeout
* Missing retry bound
* Missing cleanup
* Missing validation
* Missing test
* Better error message
* Better permission UI
* Bounded buffer
* Atomic state write

Do not include cosmetic changes merely because they are easy.

---

# 39. Architecture Recommendations

Provide recommendations at three levels.

### Immediate

Things that should be fixed now.

### Near-term

Things that should be addressed before major feature expansion.

### Long-term

Structural improvements that become relevant as Forcefield grows.

Avoid recommending a rewrite unless there is strong evidence that incremental changes cannot solve the underlying problem.

---

# 40. Audit Quality Gate

The audit is incomplete if:

* Only obvious files were inspected.
* Only tests were run.
* Only static analysis was run.
* No runtime behavior was investigated.
* No long-horizon analysis was performed.
* Provider failure behavior was ignored.
* Session persistence was ignored.
* Tool execution was ignored.
* Windows behavior was ignored.
* TUI/UX was ignored.
* Findings have no evidence.
* Speculation is presented as fact.
* The report consists primarily of style complaints.

The audit should favor depth over speed.

---

# 41. Final Deliverable

Produce one comprehensive audit report containing:

```text
1. Executive Summary

2. Repository / Architecture Overview

3. Critical Findings

4. High-Severity Findings

5. Medium/Low Findings

6. Security Audit

7. Agent Safety Audit

8. Provider / Rate-Limit Audit

9. Five-Day Long-Horizon Audit

10. Context / Memory Audit

11. Session / Persistence Audit

12. Concurrency Audit

13. Process / Shell Audit

14. Performance Audit

15. Code Quality Audit

16. Architecture Audit

17. TUI / UX Audit

18. Windows / Cross-Platform Audit

19. Testing Audit

20. Dependency Audit

21. Configuration Audit

22. Observability Audit

23. Documentation Audit

24. Repository Hygiene Audit

25. Failure Scenario Matrix

26. Top 10 Problems

27. Quick Wins

28. Recommended Roadmap

29. Five-Day Readiness Score

30. Final Verdict
```

---

# 42. Failure Scenario Matrix

Include a final matrix:

| Scenario                | Detected? | Recovered? | State Safe? | User Informed? | Long-Horizon Safe? |
| ----------------------- | --------- | ---------- | ----------- | -------------- | ------------------ |
| 429                     |           |            |             |                |                    |
| Provider timeout        |           |            |             |                |                    |
| Stream disconnect       |           |            |             |                |                    |
| Tool crash              |           |            |             |                |                    |
| Tool hang               |           |            |             |                |                    |
| Forcefield crash        |           |            |             |                |                    |
| Terminal close          |           |            |             |                |                    |
| Machine reboot          |           |            |             |                |                    |
| Session corruption      |           |            |             |                |                    |
| Disk full               |           |            |             |                |                    |
| Network loss            |           |            |             |                |                    |
| Local model unavailable |           |            |             |                |                    |
| Huge tool output        |           |            |             |                |                    |
| Huge user input         |           |            |             |                |                    |
| Infinite tool loop      |           |            |             |                |                    |
| Malicious repository    |           |            |             |                |                    |

---

# 43. Final Question

The entire audit ultimately exists to answer these questions:

> **Is Forcefield reliable enough to trust with a five-day autonomous task?**

> **If not, what specifically prevents that?**

> **What is the smallest set of changes required to make it trustworthy?**

> **What failure modes remain even after those changes?**

> **Which problems are architectural and which can be fixed incrementally?**

> **What could still kill a five-day task due to rate limits, context growth, provider failures, process leaks, session corruption, or other "dumb" operational failures?**

Do not hide uncertainty.

Do not inflate the score.

Do not optimize for making the project look good.

The purpose of this audit is to find problems while they are still cheap to fix.

**Audit Forcefield as if someone will depend on it for five uninterrupted days.**
