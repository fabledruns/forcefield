package hardening

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools/builtin"
)

// allowedSpawnSites is the explicit allow-list of non-test files that
// may construct child processes via os/exec. Every entry is a
// slash-separated repo-relative path. A new exec.Command/CommandContext
// outside this list (and the dev-only list below) fails the test so the
// new spawn path gets a conscious, reviewed allow-list edit (P0-A).
//
// The list mirrors the audited spawn table: shell backends (sandbox),
// fixed-argv helpers (git, ripgrep), MCP servers (unsandboxed by
// decision), the git-root fallback (memory), the supervised runner
// (process), and the Windows taskkill fallback.
var allowedSpawnSites = map[string]bool{
	"internal/sandbox/native_unix.go":         true,
	"internal/sandbox/native_windows.go":      true,
	"internal/sandbox/isolated_linux.go":      true,
	"internal/sandbox/wsl_windows.go":         true,
	"internal/sandbox/wsl_shared_windows.go":  true,
	"internal/sandbox/wsl_interop_windows.go": true,
	"internal/tools/git/git.go":               true,
	"internal/tools/search/ripgrep.go":        true,
	"internal/mcp/host.go":                    true,
	"internal/memory/project.go":              true,
	"internal/process/run.go":                 true,
	"internal/process/process_windows.go":     true,
}

// allowedDevSpawnSites lists non-test files that construct child
// processes but are never reachable by model output: the benchmark
// measurement harness spawns an operator-configured subject binary.
// They are allow-listed separately so the security-reviewed,
// agent-reachable set above stays distinct from dev-only tooling.
var allowedDevSpawnSites = map[string]bool{
	"benchmarks/pkg/perf/tuisession.go":     true,
	"benchmarks/pkg/runner/kill_windows.go": true,
	"benchmarks/pkg/runner/rss_windows.go":  true,
	"benchmarks/pkg/runner/runner.go":       true,
}

// TestSpawnSitesAreAllowlisted parses every non-test Go file in the
// repository and fails when os/exec construction appears outside the
// allow-lists above. Test files (*_test.go) are excluded: tests spawn
// helper processes by design.
func TestSpawnSitesAreAllowlisted(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var violations []string
	var allowed []string

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			// Skip vendored or irrelevant trees if they ever appear.
			rel := filepath.ToSlash(filepathRel(t, root, path))
			if strings.HasPrefix(rel, ".git/") || strings.HasPrefix(rel, "bin/") {
				return nil
			}
			if usesExecCommand(fset, path) {
				switch {
				case allowedSpawnSites[rel]:
					allowed = append(allowed, rel)
				case allowedDevSpawnSites[rel]:
					allowed = append(allowed, rel+" (dev-only)")
				default:
					violations = append(violations, rel)
				}
			}
			return nil
		}
		// Prune top-level directories that never hold production Go
		// sources. Matched on the repo-relative path, not the basename,
		// so a nested directory named "bin" stays audited.
		if d.IsDir() {
			if rel := filepath.ToSlash(filepathRel(t, root, path)); rel == ".git" || rel == "bin" || rel == ".forcefield" {
				return filepath.SkipDir
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo %s: %v", root, err)
	}
	sort.Strings(violations)
	sort.Strings(allowed)
	if len(violations) > 0 {
		t.Fatalf("new process-spawn site(s) outside the allow-list:\n  %s\n"+
			"Add the file to allowedSpawnSites in spawn_audit_test.go only after security review, "+
			"or route the spawn through an allow-listed site.",
			strings.Join(violations, "\n  "))
	}
	t.Logf("audited spawn sites (%d allow-listed files use os/exec): %s", len(allowed), strings.Join(allowed, ", "))
}

// TestBuiltinFallbackIsExplicit pins the current executor-wiring
// contract: builtin.NewManager without WithExecutor still yields working
// shell/shell_job tools (native execution), and Shell/JobRegistry zero
// values lazily resolve to the native backend. This is the documented
// compatibility default, not a silent downgrade: sandbox.NewExecutor
// never substitutes a backend, and an injected executor that refuses is
// surfaced, never bypassed (see shell.sandbox_gate_test.go).
//
// If a future change makes the executor mandatory on the runtime path,
// this test must be updated in the same PR to assert the new refusal.
func TestBuiltinFallbackIsExplicit(t *testing.T) {
	// Behavioral pin: builtin.NewManager without WithExecutor still
	// yields working shell/shell_job tools in native mode. This is the
	// documented compatibility default. ExecutionEnforcement describes
	// the zero-value executor without spawning anything, so this holds
	// on every platform (no WSL/distribution needed).
	mgr, err := builtin.NewManager()
	if err != nil {
		t.Fatalf("NewManager() without options: %v", err)
	}
	for _, name := range []string{"shell", "shell_job"} {
		tool, ok := mgr.Lookup(name)
		if !ok {
			t.Fatalf("tool %q not registered by NewManager() without options", name)
		}
		enforcer, ok := tool.(interface {
			ExecutionEnforcement(context.Context) (sandbox.Enforcement, bool)
		})
		if name == "shell" && !ok {
			t.Fatalf("shell tool does not expose ExecutionEnforcement")
		}
		if ok {
			enc, has := enforcer.ExecutionEnforcement(context.Background())
			if !has {
				t.Fatalf("%s: ExecutionEnforcement reports no enforcement story", name)
			}
			if enc.Mode != sandbox.ModeNative {
				t.Fatalf("%s: enforcement mode = %q, want native fallback", name, enc.Mode)
			}
		}
	}
	// Structural pin: the production runtime path must inject the sandbox
	// executor explicitly. Matched as a call expression on the builtin
	// package (resolved through the import block), so a comment merely
	// mentioning builtin.WithExecutor cannot spoof it.
	root := repoRoot(t)
	if !callsBuiltinWithExecutor(t, filepath.Join(root, "internal", "runtime", "runtime.go")) {
		t.Fatal("runtime.go has no builtin.WithExecutor(...) call: the production path must inject the sandbox executor explicitly")
	}
}

// callsBuiltinWithExecutor reports whether file calls WithExecutor on
// the package imported as forcefield/internal/tools/builtin, resolving
// the local package name through the import block.
func callsBuiltinWithExecutor(t *testing.T, file string) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	builtinName := ""
	for _, imp := range f.Imports {
		p, uerr := strconv.Unquote(imp.Path.Value)
		if uerr != nil || p != "forcefield/internal/tools/builtin" {
			continue
		}
		builtinName = "builtin"
		if imp.Name != nil {
			builtinName = imp.Name.Name
		}
	}
	if builtinName == "" {
		return false
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "WithExecutor" {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != builtinName {
			return true
		}
		found = true
		return false
	})
	return found
}

func usesExecCommand(fset *token.FileSet, path string) bool {
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		// A file that does not parse (e.g. build-tag edge) must not
		// silently pass the audit; surface it as a violation.
		return true
	}
	// Resolve the local names for os/exec from the import block so an
	// aliased import (import e "os/exec") cannot evade the audit. A
	// dot-import exposes bare Command/CommandContext identifiers.
	execNames := make(map[string]bool)
	dotExec := false
	importsExec := false
	for _, imp := range f.Imports {
		p, uerr := strconv.Unquote(imp.Path.Value)
		if uerr != nil || p != "os/exec" {
			continue
		}
		importsExec = true
		switch {
		case imp.Name == nil:
			execNames["exec"] = true
		case imp.Name.Name == ".":
			dotExec = true
		case imp.Name.Name != "_":
			execNames[imp.Name.Name] = true
		}
	}
	if !importsExec {
		return false
	}
	// Re-parse with bodies: the imports-only pass has no call sites.
	f, err = parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return true
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			pkg, ok := fun.X.(*ast.Ident)
			if !ok || !execNames[pkg.Name] {
				return true
			}
			if fun.Sel.Name == "Command" || fun.Sel.Name == "CommandContext" {
				found = true
				return false
			}
		case *ast.Ident:
			if dotExec && (fun.Name == "Command" || fun.Name == "CommandContext") {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("repo root (go.mod) not found above %s", file)
	return ""
}

func filepathRel(t *testing.T, root, path string) string {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("filepath.Rel(%q, %q): %v", root, path, err)
	}
	return rel
}
