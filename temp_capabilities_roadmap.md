# Forcefield Agent Capability Advancement & Engineering Roadmap

---

## 1. Current Architecture Assessment

Forcefield (`ff`) is currently at version **v1.6.0** (commit `c7fcece`, tag `v1.6.0-6-gc7fcece` on `main`). All automated tests across all 25 internal packages pass cleanly (`go test ./...` and `go build ./...` succeed with zero errors).

### Architectural Strengths & Baseline

```
                 CLI (Cobra) / Interactive TUI (Bubble Tea)
                                      │
                                      ▼
                                Runtime Harness
          ┌───────────────────────────┼───────────────────────────┐
          │                           │                           │
          ▼                           ▼                           ▼
    Agent Registry             Session Storage             Config Manager
  (coding, cyber, docs,       (.forcefield/sessions)     (~/.forcefield/config.yaml)
   legal, research, etc.)             │
          │                           │
          ▼                           ▼
  Provider Registry            Scheduler & Guard
  (Ollama, LM Studio,         (maxConcurrency: 4,
   Anthropic, OpenAI,          context guard 6KB elision,
   Gemini, Bedrock, etc.)      loop detection)
          │                           │
          └─────────────┬─────────────┘
                        ▼
               Tool Manager & Registry
    ┌───────────────────┼───────────────────┐
    ▼                   ▼                   ▼
Native Tools      MCP Host & Subprocess  Orchestrator
(fs, shell, git,  (stdio JSON-RPC       (delegate tool,
 search, security)  mcp__<srv>__<tool>)   batch tasks, child runs)
```

The runtime harness has attained high engineering maturity in execution boundaries, sandboxing, session serialization, and error recovery:
1. **Uncompromising Single-Binary, Pure-Go Baseline:** The project builds cleanly with `CGO_ENABLED=0` across 6 target platforms (`linux-amd64`, `linux-arm64`, `darwin-amd64`, `darwin-arm64`, `windows-amd64`, `windows-arm64`) on GitHub Actions (`.github/workflows/release.yml`). Any capability that compromises `CGO_ENABLED=0` violates Forcefield's core design goal.
2. **Defensive Filesystem Confinement:** Native filesystem tools enforce strict workspace boundary verification (`EnsureWithinWorkspace`), symlink traversal guards, and open descriptor validation (`OpenNoFollowReadWrite`).
3. **Multi-Agent Orchestration:** Native subagent delegation (`internal/runtime/orchestrator.go`) supports single and batch child runs with bounded depth, isolated memory scopes, and permission propagation.
4. **Native MCP Support Already Present:** Stdio MCP client support (`internal/mcp`) is already implemented, exposing external MCP tools as `mcp__<server>__<tool>` with interactive configuration management (`/mcp`).

---

## 2. Existing Capability Inventory

A factual audit of the repository reveals the following active capabilities:

| Component | Package / Path | Current Capabilities | Constraints & Hard Limits |
|---|---|---|---|
| **`read_file`** | `internal/tools/filesystem` | Reads entire text file from workspace. | 5 MiB file cap; **no line ranges**, **no line numbers**, full-file read only. |
| **`write_file`** | `internal/tools/filesystem` | Overwrites whole file or creates new file. | 512 KiB write cap; **destructive overwrite only**; cannot patch or edit sections. |
| **`list_files`** | `internal/tools/filesystem` | Lists immediate directory entries. | 500 entry limit; non-recursive. |
| **`search_files`** | `internal/tools/search` | Text search across workspace files. | Max 100 matches; 2000-byte line elision. |
| **`search_code`** | `internal/tools/search` | Spawns `rg` (ripgrep) argv directly with JSON parsing. | Falls back to `search_files` if `rg` absent. Max 100 matches. |
| **`find_files`** | `internal/tools/search` | Glob and substring file path finder. | Max 50 paths. |
| **`git`** | `internal/tools/git` | Read-only inspection (`status`, `diff`, `staged`, `log`, `changed`). | **Strictly read-only**; no commit, no stash, no rollback, no checkpoint. |
| **`shell` & `shell_job`** | `internal/tools/shell` | Piped command execution (Bash natively on Unix, WSL on Windows). | 2 MiB output cap; interactive commands blocked; 30s-300s timeouts. |
| **`secret_scan`** | `internal/tools/security` | Regex secret detector. | Defensive analysis only. |
| **`delegate`** | `internal/runtime` | Subtask delegation to specialized agent profiles (`coding`, `cyber`, etc.). | Max depth 1-3, isolated permission scope, batch task launch. |
| **MCP Client** | `internal/mcp` | JSON-RPC over stdio client for external MCP servers. | Stdio only; frozen universe at session start; no SSE/remote HTTP. |
| **Scheduler** | `internal/runtime` | Concurrent tool execution up to `maxConcurrency: 4`. | **Zero dependency analysis**; file read/write operations race if batched. |

---

## 3. Identified Capability Gaps

The recent evaluation scores (**Agent capabilities: 5.5/10**, vs **Engineering quality: 7/10** and **Security: 7.5/10**) are substantiated by the codebase. Forcefield is a secure, hardened runtime wrapped around an agent with major coding handicaps:

### Gap 1: Catastrophic Full-File Rewrite Bottleneck (Zero Surgical Editing)
Forcefield has **no `edit_file` tool**. When an agent needs to change a 2-line condition in a 500-line file, its only choice is `write_file`, sending all 500 lines back.
- **Consequence for Local Models:** Models like Qwen 2.5 (7B/14B) or Llama 3.1 (8B) routinely hallucinate, drop functions, truncate closing brackets, or alter unrelated code when regenerating hundreds of lines.
- **Consequence for Frontier Models:** Burns thousands of output tokens per micro-fix, massively increasing latency and API costs.
- **Context Guard Center-Elision Collision:** In `internal/runtime/scheduler.go`, tool results exceeding 6,000 bytes have their middle elided (`... [N bytes elided] ...`). If an agent reads a 300-line file with `read_file`, it cannot even see the middle of the file to prepare the full-file rewrite.

### Gap 2: Blind Code Navigation (Zero Semantic Repository Intelligence)
The agent possesses no semantic or structural awareness of code:
- No AST-based symbol index (cannot find `type Scheduler struct` or `func (s *scheduler) Run` without raw text grep).
- No repository map: When given a new repository, the agent must run multiple `list_files`, `find_files`, and `search_code` rounds just to discover the high-level layout.
- Text search matches string literals, comments, and variable names indiscriminately, polluting the 6,000-byte output budget with noise.

### Gap 3: Destructive Operation Risk (Zero Git Checkpoint or Rollback Safety)
The `git` tool is hardcoded as read-only.
- If an agent performs a 4-file edit and breaks tests, it cannot revert to a pre-task checkpoint.
- The human operator must manually intervene outside the harness (`git restore .` or `git checkout .`), destroying any unstaged work the operator had prior to the agent run.
- There is no automated snapshotting before multi-file tool batches.

### Gap 4: Scheduler Concurrency Race Hazards
In `scheduler.go`, tool calls in a batch run concurrently across goroutines bounded only by a worker semaphore (`chan struct{}`).
- If the model emits:
  1. `write_file("internal/foo.go", ...)`
  2. `read_file("internal/foo.go")`
  both run in parallel. The read will read partial or pre-write content depending on OS thread scheduling.
- The scheduler has no resource-locking mechanism or dependency DAG.

### Gap 5: Blind to External Documentation & Web Resources
The agent has no native web fetching or documentation querying capability. To inspect an API doc or GitHub issue, the agent must execute `shell: "curl ..."` or `shell: "python -c ..."`, which prompts for permission, requires external binaries, and returns raw unparsed HTML.

---

## 4. Ranked Feature Roadmap

| Feature | Priority | Impact | Complexity | Dependencies | Architecture | Testing | Release |
|---|---|---|---|---|---|---|---|
| **Surgical File Editing (`edit_file`)** | **P0** | **Transformative (+2.5 Capability score)**: Eliminates file rewrite failures; enables small local models to edit code accurately; cuts token usage by 80%. | Medium | `internal/tools/filesystem`, `internal/sandbox` | SEARCH/REPLACE block replacement engine with exact/fuzzy match, multi-edit batching, atomic temp-file write, and Go syntax dry-run verification. | Unit tests for exact, duplicate, missing matches, multi-hunks, CRLF handling, symlinks. | **v1.7.0** |
| **Line-Aware File Reading (`read_file` line ranges)** | **P0** | **High**: Solves 6KB context truncation blindness; enables reading targeted 50-line windows with 1-based line numbers. | Low | `internal/tools/filesystem` | Add optional `start_line` / `end_line` parameters to `read_file`; format output with line numbers (`12: func ...`). | Unit tests for boundary conditions, invalid ranges, EOF handling, empty files. | **v1.7.0** |
| **Git Checkpoint & Safe Rollback (`git_checkpoint`, `/rollback`)** | **P0** | **High**: Zero-risk agent experimentation; instant recovery from hallucinated or broken multi-file edits. | Medium | `internal/tools/git`, `internal/runtime` | Uses git plumbing (`git stash create` / `git write-tree`) to create ephemeral snapshots in `refs/forcefield/checkpoints/` without modifying user branch history. | Unit tests for dirty working tree, untracked files, merge conflicts, non-git workspace fallback. | **v1.7.0** |
| **Repository Map & Structural Code Search (`repo_map`, `search_symbol`)** | **P1** | **High**: Instant repository layout orientation in <1.5K tokens; jump-to-definition and symbol references without CGO. | Medium | `internal/tools/search`, `go/parser`, `go/ast` | Pure-Go AST indexing for Go, fast regex/tree symbol extraction for other languages, PageRank/import reference graph ranking. | Unit tests for symbol extraction accuracy, token budget clamping, large repo performance. | **v1.8.0** |
| **Scheduler Resource Dependency Graph** | **P1** | **High**: Prevents data races and silent corruption during concurrent tool batches. | Medium | `internal/runtime/scheduler.go` | Read/Write path lock graph. Independent reads run parallel; writes to overlapping paths or workspace-wide shell commands sequence automatically. | Concurrency race tests with `-race`, barrier synchronization verification. | **v1.8.0** |
| **Native Documentation & Web Fetcher (`fetch_web`)** | **P1** | **Medium-High**: Allows reading docs/URLs without shell permission prompts or raw curl dependencies. | Low | `net/http`, HTML-to-Markdown | Pure Go HTTP client, HTML-to-Markdown converter, SSRF protection (private IP blocking), 100KB budget. | Unit tests for markdown extraction, timeout enforcement, loopback/private IP blocking. | **v1.8.0** |
| **In-Session Read Cache & Deduplication** | **P2** | **Medium**: Saves 15-30% context tokens by eliding re-reads of unchanged files. | Low | `internal/runtime`, `internal/tools/filesystem` | Tracks file mtime/hash per session; returns "file unchanged since turn X (sha256:...)" on redundant identical reads. | Cache invalidation on `write_file`/`edit_file`, dirty detection tests. | **v1.9.0** |
| **Curated External Ecosystem Wrappers (Nuclei, DuckDB, Playwright)** | **P2** | **Medium**: Expands `cyber` agent and data analysis capabilities without bloating binary size. | Low | `internal/tools/security`, `internal/mcp` | External CLI wrappers with PATH discovery and strict escalation, paired with pre-tested MCP template configs for Playwright/DuckDB. | CLI execution mock tests, missing binary degradation tests. | **v1.9.0** |
| **MCP Dynamic Reload & Tool Progress** | **P2** | **Low-Medium**: Allows adding/restarting MCP servers without dropping session context; streams progress. | Medium | `internal/mcp`, `internal/runtime` | Live client reconnect; maps MCP `notifications/progress` to Forcefield's `EventToolProgress`. | Dynamic re-listing mock tests, stream cancellation tests. | **v1.9.0** |

---

## 5. Detailed Architecture for the Top Three Priorities

### Priority 1: Surgical File Editing Engine (`edit_file`)

#### Why SEARCH/REPLACE Blocks Over Unified Diff
- **Unified Diff Flaw:** Unified diffs require LLMs to generate precise line offsets and hunk headers (`@@ -45,12 +45,14 @@`). Small models (7B/8B/14B) fail at line-counting in ~40% of diff attempts. A single line mismatch causes standard patch engines to reject the entire operation.
- **Search/Replace Block Advantage:** LLMs are natural text-completion engines. They excel at quoting the exact unique snippet of code to be replaced and writing the replacement. If the search block matches uniquely, the edit applies with 100% mathematical precision.

#### Package and File Modifications
- New file: `internal/tools/filesystem/edit_file.go`
- New file: `internal/tools/filesystem/edit_file_test.go`
- Registration: `internal/tools/builtin/builtin.go`
- Operating contract: Update `internal/agent/contract.go` to prioritize `edit_file` over `write_file`.

#### Struct & Interface Sketch

```go
package filesystem

import (
	"context"
	"fmt"
	"os"
	"strings"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// EditSpec represents a single search-and-replace block.
type EditSpec struct {
	Search  string `json:"search"`
	Replace string `json:"replace"`
}

// EditFile performs surgical, verified search-and-replace edits on existing files.
type EditFile struct {
	policy sandbox.Policy
}

func NewEditFileWithPolicy(p sandbox.Policy) *EditFile {
	return &EditFile{policy: p}
}

func (EditFile) Name() string { return "edit_file" }

func (EditFile) Description() string {
	return "Surgically edit an existing text file by replacing exact code blocks. " +
		"Each edit replaces an exact, unique occurrence of 'search' with 'replace'. " +
		"The file is checked for syntax validity before changes are committed."
}

func (EditFile) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to edit, absolute or workspace-relative.",
			},
			"edits": map[string]any{
				"type": "array",
				"description": "Sequential list of search/replace blocks to apply atomically.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"search": map[string]any{
							"type":        "string",
							"description": "Exact text to find and replace. Must match uniquely in the file.",
						},
						"replace": map[string]any{
							"type":        "string",
							"description": "Replacement text.",
						},
					},
					"required": []string{"search", "replace"},
				},
			},
		},
		"required": []string{"path", "edits"},
	}
}
```

#### Execution Flow & Safety Pipeline

```
Agent issues edit_file(path, edits)
           │
           ▼
1. Boundary Check: EnsureWithinWorkspace (reject symlink escapes)
           │
           ▼
2. Open & Read: Read existing content under NoFollow descriptor
           │
           ▼
3. Normalization: Detect line endings (CRLF vs LF), normalize search blocks
           │
           ▼
4. Dry-Run Replacement on In-Memory Buffer:
   For each edit in edits:
     ├── count == 0: FAIL with actionable error + fuzzy context lines
     ├── count > 1: FAIL with ambiguity error (report line numbers of each match)
     └── count == 1: Apply replacement to in-memory buffer
           │
           ▼
5. Post-Edit Syntax Verification Hook:
   If Go file (.go): Run go/parser.ParseFile on buffer
   If syntax error: REJECT edit, return line-specific parse error to model
           │
           ▼
6. Atomic Commit:
   Write buffer to temporary file in same dir -> Fsync -> Atomic Rename
           │
           ▼
Return Success: "successfully applied N edits to <path> (+X/-Y lines)"
```

#### Ambiguous & Missing Match Handling
- **Ambiguous Match Error (count > 1):**
  `"search block matched 3 times (at lines 24, 78, 142). Please provide additional surrounding lines of code in 'search' to make the match unique."`
- **Missing Match Error (count == 0):**
  The engine runs a normalized whitespace comparison (ignoring indentation changes). If a match is found with different indentation, it returns:
  `"search block not found. However, a near-match with different indentation was found at line 56. Ensure exact leading whitespace matches the file."`

#### Concurrency & Failure Modes
- **Failure Mode 1 (Mid-batch failure):** Atomic. All edits apply in-memory first; if edit 3 of 4 fails, the file on disk remains 100% untouched.
- **Failure Mode 2 (Syntax corruption):** Go files are verified with `go/parser.ParseFile` before touching disk. Syntax errors are returned directly to the model in the tool result, allowing immediate self-correction without breaking the test suite.

---

### Priority 2: Line-Aware File Reader (`read_file` Line Ranges & Numbering)

#### Problem Being Solved
`read_file` currently dumps the entire file string. When files exceed 150 lines, the runtime's 6,000-byte context guard truncates the middle. The agent cannot inspect code between lines 100 and 300, leading to hallucinations.

#### Modifications to `read_file.go`
- File: `internal/tools/filesystem/read_file.go`
- Add optional parameters to `InputSchema`:
  - `start_line` (integer, 1-based, default 1)
  - `end_line` (integer, 1-based, optional)
  - `show_line_numbers` (boolean, default true)

#### Implementation Logic Sketch

```go
func formatLines(content string, startLine, endLine int, showLineNumbers bool) (string, int) {
	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	if startLine < 1 {
		startLine = 1
	}
	if endLine <= 0 || endLine > totalLines {
		endLine = totalLines
	}
	if startLine > totalLines {
		return fmt.Sprintf("[file has %d lines; requested start_line %d is out of range]", totalLines, startLine), 0
	}

	var sb strings.Builder
	padWidth := len(strconv.Itoa(endLine))
	for i := startLine; i <= endLine; i++ {
		line := lines[i-1]
		if showLineNumbers {
			fmt.Fprintf(&sb, "%*d | %s\n", padWidth, i, line)
		} else {
			sb.WriteString(line)
			sb.WriteByte('\n')
		}
	}
	return sb.String(), totalLines
}
```

#### Tool Output Example
```text
[Showing lines 45 to 55 of 320 lines in internal/runtime/scheduler.go]
45 | type scheduler struct {
46 |     managerMu   sync.RWMutex
47 |     manager     *tools.Manager
48 |     permissions *permissions.Manager
49 |     asker       permissions.Asker
50 |     cfg         SchedulerConfig
51 |     sessionMu   sync.Mutex
52 |     sessionAllow map[string]permissions.Decision
53 | }
```
Now, when the agent prepares an `edit_file` call, it has the exact line numbers and exact character formatting required for zero-error search blocks.

---

### Priority 3: Git Checkpoint & Safe Rollback Engine

#### Architecture & Isolation from User Branch History
Rather than creating dirty commits on the user's active branch or modifying the git reflog, the checkpoint engine uses **pure git plumbing objects**:
1. Checkpoints create tree objects using `git write-tree` or stash commit objects using `git stash create`.
2. Checkpoint refs are stored in a dedicated, isolated namespace:
   `refs/forcefield/checkpoints/<session_id>/<checkpoint_id>`
3. The user's active branch (`HEAD`, current branch pointer, index, and commit history) is **never touched**.
4. When a rollback occurs, git working directory restoration uses `git read-tree` or scoped file checkout from the checkpoint ref.

#### Component Location & Integration
- Package: `internal/checkpoint`
- Tools exposed to agent:
  - `create_checkpoint`: Saves current working tree state with a label.
  - `rollback_checkpoint`: Reverts working tree to a named or latest checkpoint.
  - `diff_checkpoint`: Returns diff of uncommitted changes since a checkpoint.
- TUI / CLI Commands:
  - `/checkpoint [name]`: Manual checkpoint by operator.
  - `/rollback [name]`: Instant rollback of agent changes.
  - `/diff`: Enhanced to show diff against latest checkpoint.
- Runtime Auto-Checkpoint Hook:
  - The runtime scheduler automatically creates an implicit checkpoint before executing any tool batch containing `write_file` or `edit_file`.

#### Struct & Core Methods Sketch

```go
package checkpoint

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Checkpoint struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Label     string    `json:"label"`
	TreeHash  string    `json:"tree_hash"`
	CreatedAt time.Time `json:"created_at"`
}

type Manager struct {
	workspaceRoot string
}

func NewManager(workspaceRoot string) *Manager {
	return &Manager{workspaceRoot: workspaceRoot}
}

// Create captures the entire working directory state without committing to any branch.
func (m *Manager) Create(ctx context.Context, sessionID, label string) (*Checkpoint, error) {
	// git stash create produces a dangling commit object containing staged + unstaged changes
	cmd := exec.CommandContext(ctx, "git", "stash", "create")
	cmd.Dir = m.workspaceRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git stash create failed: %w", err)
	}

	commitHash := strings.TrimSpace(string(out))
	if commitHash == "" {
		// Working tree is clean; capture current HEAD commit
		headCmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
		headCmd.Dir = m.workspaceRoot
		headOut, err := headCmd.Output()
		if err != nil {
			return nil, fmt.Errorf("resolve HEAD: %w", err)
		}
		commitHash = strings.TrimSpace(string(headOut))
	}

	cpID := fmt.Sprintf("cp_%d", time.Now().UnixNano())
	refName := fmt.Sprintf("refs/forcefield/checkpoints/%s/%s", sessionID, cpID)

	// Update ref in dedicated namespace
	refCmd := exec.CommandContext(ctx, "git", "update-ref", refName, commitHash)
	refCmd.Dir = m.workspaceRoot
	if err := refCmd.Run(); err != nil {
		return nil, fmt.Errorf("record checkpoint ref: %w", err)
	}

	return &Checkpoint{
		ID:        cpID,
		SessionID: sessionID,
		Label:     label,
		TreeHash:  commitHash,
		CreatedAt: time.Now(),
	}, nil
}

// Rollback restores the working directory to the exact tree state of the checkpoint.
func (m *Manager) Rollback(ctx context.Context, sessionID, cpID string) error {
	refName := fmt.Sprintf("refs/forcefield/checkpoints/%s/%s", sessionID, cpID)
	
	// Checkout files from checkpoint ref to working directory
	cmd := exec.CommandContext(ctx, "git", "checkout", refName, "--", ".")
	cmd.Dir = m.workspaceRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("restore checkpoint %s: %s (%w)", cpID, strings.TrimSpace(string(out)), err)
	}
	return nil
}
```

#### Concurrency & Safety
- **Untracked File Safety:** Before restoring, any new untracked files created by the agent during the failed step are detected via `git ls-files --others --exclude-standard` and safely removed.
- **Operator Safety:** The checkpoint mechanism works even if the user has unstaged changes before starting Forcefield; `git stash create` captures both staged and unstaged state without touching index pointers.

---

## 6. Engineering Tool Ecosystem Evaluation

An objective evaluation of the candidate tools against Forcefield's architectural principles:

```
┌─────────────────┬──────────────┬──────────────┬────────────────────────────────────────────────────────┐
│ Candidate Tool  │ Local First? │ Requires Key?│ Architectural Decision & Integration Recommendation   │
├─────────────────┼──────────────┼──────────────┼────────────────────────────────────────────────────────┤
│ Firecrawl       │ Self-host/No │ Cloud: Yes   │ DO NOT embed. Recommend official Firecrawl MCP server. │
│ Scrapling       │ Yes (Python) │ No           │ DO NOT embed. External executable/MCP. Go fetcher best.│
│ Nuclei          │ Yes (Go)     │ No           │ DO NOT embed into binary. Integrate as CLI tool.       │
│ DuckDB          │ Yes (C++)    │ No           │ DO NOT link via CGO. Integrate via DuckDB CLI or MCP.  │
│ Playwright      │ Yes (Node/Py)│ No           │ DO NOT bundle Chromium. Use Puppeteer/Playwright MCP.  │
│ Native WebFetch │ Yes (Pure Go)│ No           │ EMBED NATIVELY (P1). Lightweight URL & doc reader.     │
└─────────────────┴──────────────┴──────────────┴────────────────────────────────────────────────────────┘
```

### 1. Firecrawl (Web Crawling & Extraction)
- **Capability:** Turns web pages or full websites into clean Markdown.
- **Constraints:** Cloud version requires `FIRECRAWL_API_KEY`. Self-hosted version requires a Docker cluster (Node, Redis, Playwright, PostgreSQL).
- **Decision:** **Do not embed into Forcefield binary.** Forcefield already supports stdio MCP servers. Users who need Firecrawl can configure `@modelcontextprotocol/server-firecrawl` with one command: `/mcp add firecrawl npx -y @modelcontextprotocol/server-firecrawl`.

### 2. Scrapling (Web Scraping)
- **Capability:** Python library for stealth scraping and dynamic page evaluation.
- **Constraints:** Written in Python; requires Python virtual environment and browser drivers.
- **Decision:** **Do not integrate as native code.** Embedding Python in a Go binary violates the single-binary principle. A native Go HTTP fetcher (`fetch_web`) covers standard documentation retrieval without Python dependencies.

### 3. Nuclei (Vulnerability Assessment)
- **Capability:** Fast, template-based security vulnerability scanner written in Go by ProjectDiscovery.
- **Constraints:** Nuclei binary is 50+ MB and requires hundreds of community YAML templates. Bundling it would more than double Forcefield's binary size and create severe template maintenance overhead. Running scans against arbitrary targets also incurs network security and legal liability.
- **Decision:** **Support via external CLI integration for the `cyber` agent.** When the `cyber` agent runs, if `nuclei` is detected on PATH, expose a hardened `nuclei_scan` tool. The tool must enforce:
  1. Target restricted to `localhost`, local IP ranges, or explicitly approved domains.
  2. Mandatory `Ask` permission escalation on every execution.
  3. Bounded execution timeouts and structured JSON output parsing.

### 4. DuckDB (Analytical Queries)
- **Capability:** In-process SQL OLAP query engine for Parquet, CSV, JSON, and trace analysis.
- **Constraints:** DuckDB is written in C++. Using the Go driver (`go-duckdb`) requires CGO and C++ linking, which breaks cross-compilation across Forcefield's 6 target operating systems.
- **Decision:** **External CLI or MCP integration.** The standalone `duckdb` binary is available on user systems. Forcefield can expose a `duckdb_query` tool that shells out to `duckdb -json -c "<query>"` if available on PATH, or users can use the DuckDB MCP server.

### 5. Playwright (Browser Automation)
- **Capability:** Full browser interaction, screenshot capture, DOM navigation, and E2E testing.
- **Constraints:** Requires Node.js/Python and ~300MB of browser binaries (Chromium, Firefox, WebKit).
- **Decision:** **Leverage existing native MCP support.** The official Playwright MCP server (`@playwright/mcp-server` or `@modelcontextprotocol/server-puppeteer`) gives the agent browser automation without adding a single byte to the Forcefield binary.

---

## 7. Suggested Release Sequencing

```
 v1.7.0: "Surgical Precision"
  ├── edit_file (SEARCH/REPLACE, multi-edit, Go syntax verification)
  ├── read_file range support (start_line, end_line, line numbers)
  └── git checkpoint engine (safe multi-file rollback, /rollback)
         │
         ▼
 v1.8.0: "Repository Intelligence"
  ├── repo_map (pure-Go AST + lexical symbol map, import dependency graph)
  ├── search_symbol (targeted symbol definition and reference lookup)
  ├── scheduler resource graph (read/write conflict prevention)
  └── fetch_web (native pure-Go URL and doc reader with SSRF guard)
         │
         ▼
 v1.9.0: "Ecosystem & Efficiency"
  ├── in-session read deduplication & file mtime caching
  ├── cyber agent tooling (external Nuclei CLI wrapper with scope bounds)
  ├── MCP enhancements (dynamic reload, progress notification forwarding)
  └── model-aware tool schema optimization for 7B/8B local models
```

### Milestone v1.7.0: Surgical Precision (Next Immediate Release)
- Deliver `edit_file` to eliminate full-file rewrites.
- Deliver `read_file` line ranges to eliminate context-guard center-elision blindness.
- Deliver `git_checkpoint` to give the agent a safety net to undo mistakes.
- **Outcome:** Solves the core criticism in the benchmark evaluation. Local models (Ollama, LM Studio) become immediately viable for multi-step coding tasks.

### Milestone v1.8.0: Repository Intelligence & Safe Concurrency
- Deliver `repo_map` and `search_symbol` to give the agent instant comprehension of large repos.
- Deliver scheduler resource DAG to prevent concurrency races during batch execution.
- Deliver `fetch_web` for offline/online documentation reading.
- **Outcome:** Eliminates blind search loops; cuts exploration turns by ~50%.

### Milestone v1.9.0: Ecosystem & Efficiency
- Deliver read deduplication and file caching to preserve context windows.
- Deliver `cyber` agent CLI integrations (Nuclei) and MCP templates (Playwright, DuckDB).
- Deliver MCP live reloading and streaming progress.
- **Outcome:** Expands agent specialization into testing, security auditing, and data tasks.

---

## 8. Measurable Acceptance Criteria

To guarantee that capability improvements produce demonstrable, objective benchmark improvements:

### Criterion 1: Surgical File Editing Accuracy
- **Test Metric:** In an automated evaluation of 100 sequential file modifications across Go, Python, and TypeScript files (ranging from 50 to 1,500 lines):
  - `edit_file` must achieve $\ge 95\%$ success on first attempt with local models (Qwen 2.5 14B / Llama 3.1 8B).
  - Out-of-target line preservation must be $100\%$ (zero dropped functions or altered unrelated lines).
  - Ambiguous matches must fail cleanly with zero modifications to disk.

### Criterion 2: Token & Context Efficiency
- **Test Metric:** On standard single-function bug fixes in files >300 lines:
  - Output token consumption per edit must drop by at least $75\%$ compared to `write_file`.
  - Tool execution duration must drop by at least $50\%$ on local inference providers.

### Criterion 3: Rollback Reliability
- **Test Metric:** For any multi-file task that fails verification or is cancelled:
  - Executing `rollback_checkpoint` must restore the working tree to byte-for-byte identity with the pre-task git status.
  - Zero unstaged files from prior to the run may be modified or deleted.

### Criterion 4: Zero-CGO Build Integrity
- **Test Metric:** Running:
  ```bash
  CGO_ENABLED=0 go build -trimpath ./...
  ```
  must continue to pass with zero warnings on all target architectures (Linux, macOS, Windows). Binary size must remain under 35 MB.

---

## 9. Clear Recommendation: The Single Best Feature to Implement Next

### Implement **`edit_file` (Surgical Search/Replace Block Editing)** First.

#### Why This Above Everything Else:
1. **It directly cures the root cause of the 5.5/10 capability score.** The single largest failure mode of Forcefield today is that any file modification forces a complete rewrite via `write_file`.
2. **It unlocks local LLMs immediately.** Local models like Qwen 2.5 7B/14B and Llama 3.1 8B have decent reasoning capabilities but collapse when forced to generate 500 lines of boilerplate to change 3 lines. `edit_file` turns local inference from an exercise in frustration into a genuinely usable daily driver.
3. **It requires zero external dependencies, zero CGO, and zero architecture rewrites.** It fits directly into the existing `internal/tools/filesystem` package, honors the existing `sandbox.Policy` workspace confinement, and immediately integrates with the active agent registry and tool manager.
4. **It sets the foundation for everything that follows.** Once surgical editing is in place, line-aware reading and git checkpoints complete a rock-solid, production-grade coding harness.
