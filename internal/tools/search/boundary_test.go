package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
)

// TestCheckBoundary_SearchRoots pins the pre-flight contract for the
// three search tools: inside roots resolve to the canonical workspace
// root, while traversal, absolute outside roots, and drive-rooted
// shapes are rejected without walking anything.
func TestCheckBoundary_SearchRoots(t *testing.T) {
	ws := t.TempDir()
	sub := filepath.Join(ws, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	tools := map[string]interface {
		CheckBoundary(map[string]any) (string, error)
	}{
		"search_files": NewSearchFilesWithPolicy(sandbox.Policy{Workspace: ws}),
		"find_files":   NewFindFilesWithPolicy(sandbox.Policy{Workspace: ws}),
		"search_code":  NewSearchCodeWithPolicy(sandbox.Policy{Workspace: ws}),
	}
	for name, tool := range tools {
		for _, req := range []string{".", "sub", sub} {
			resolved, err := tool.CheckBoundary(map[string]any{"path": req})
			if err != nil {
				t.Errorf("%s CheckBoundary(%q) = %v, want acceptance", name, req, err)
				continue
			}
			// Inside-ness, not spelling: hosts may alias temp paths.
			cleanWS, werr := filepath.EvalSymlinks(ws)
			if werr != nil {
				cleanWS = ws
			}
			cleanWS = filepath.Clean(cleanWS)
			cleanResolved := filepath.Clean(resolved)
			same := strings.EqualFold(cleanWS, cleanResolved)
			under := strings.HasPrefix(strings.ToLower(cleanResolved), strings.ToLower(cleanWS+string(filepath.Separator)))
			if !filepath.IsAbs(resolved) || (!same && !under) {
				t.Errorf("%s CheckBoundary(%q) = %q, want an absolute path inside %q", name, req, resolved, cleanWS)
			}
		}
		for _, req := range []string{"..", "sub/../..", outside, `C:\ff-probe-ws-escape-check`, `/go/search-root`} {
			if _, err := tool.CheckBoundary(map[string]any{"path": req}); err == nil {
				t.Errorf("%s CheckBoundary(%q) succeeded, want rejection", name, req)
			}
		}
	}
}
