package scrapling_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"forcefield/internal/tools/web"
	"forcefield/internal/tools/web/scrapling"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// scriptDir returns the directory containing this test file, which also
// contains worker.py.
func scriptDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}

// pythonAvailable returns the first python3-or-python executable found in
// PATH, or empty string if none is available.
func pythonAvailable() string {
	candidates := []string{"python3", "python"}
	if runtime.GOOS == "windows" {
		candidates = []string{"python", "python3", "py"}
	}
	for _, name := range candidates {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		out, err := exec.Command(path, "--version").Output()
		if err != nil {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(string(out)), "Python 3") {
			return path
		}
	}
	return ""
}

// scrapling available returns true when both Python and the scrapling library
// are present.
func scraplingAvailable(t *testing.T) bool {
	t.Helper()
	py := pythonAvailable()
	if py == "" {
		return false
	}
	err := exec.Command(py, "-c", "import scrapling").Run()
	return err == nil
}

// newClient builds a Client pointing at the real worker.py in the test dir.
func newClient(t *testing.T) *scrapling.Client {
	t.Helper()
	return scrapling.New(scriptDir(t))
}

// ---------------------------------------------------------------------------
// 1. Backend not found — Python missing
// ---------------------------------------------------------------------------

func TestExtractHTML_PythonUnavailable(t *testing.T) {
	if pythonAvailable() != "" {
		t.Skip("Python is available; skipping Python-missing test")
	}
	c := newClient(t)
	t.Cleanup(func() { _ = c.Close() })

	_, err := c.ExtractHTML(context.Background(), "<html><body>hello</body></html>", "", nil)
	if err == nil {
		t.Fatal("expected error when Python is not installed")
	}
	if !isBackendNotFound(err) {
		t.Fatalf("expected ErrBackendNotFound, got: %v", err)
	}
}

func isBackendNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), web.ErrBackendNotFound.Error()) ||
		isUnwrapped(err)
}

func isUnwrapped(err error) bool {
	var target interface{ Unwrap() error }
	if e, ok := err.(interface{ Is(error) bool }); ok {
		return e.Is(web.ErrBackendNotFound)
	}
	_ = target
	return false
}

// ---------------------------------------------------------------------------
// 2. Backend not found — Scrapling not installed
// ---------------------------------------------------------------------------

func TestExtractHTML_ScraplingNotInstalled(t *testing.T) {
	py := pythonAvailable()
	if py == "" {
		t.Skip("Python not available; cannot test Scrapling-missing path")
	}
	// Check whether scrapling IS installed; if so skip.
	if err := exec.Command(py, "-c", "import scrapling").Run(); err == nil {
		t.Skip("scrapling is installed; skipping not-installed test")
	}

	c := newClient(t)
	t.Cleanup(func() { _ = c.Close() })

	_, err := c.ExtractHTML(context.Background(), "<html><body>hello</body></html>", "", nil)
	if err == nil {
		t.Fatal("expected error when scrapling is not installed")
	}
	// Should mention ErrBackendNotFound (wraps it).
	if !strings.Contains(err.Error(), "not") {
		t.Logf("error message: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 3. Worker timeout — subprocess hangs
// ---------------------------------------------------------------------------

func TestExtractHTML_WorkerTimeout(t *testing.T) {
	py := pythonAvailable()
	if py == "" {
		t.Skip("Python not available")
	}

	// Write a fake worker that blocks forever.
	dir := t.TempDir()
	workerPath := filepath.Join(dir, "worker.py")
	err := os.WriteFile(workerPath, []byte(`import sys, time
for line in sys.stdin:
    time.sleep(9999)
`), 0o644)
	if err != nil {
		t.Fatalf("write fake worker: %v", err)
	}

	c := scrapling.NewWithPython(py, workerPath)
	t.Cleanup(func() { _ = c.Close() })

	ctx := context.Background()
	start := time.Now()
	_, err = c.ExtractHTML(ctx, "<html><body>test</body></html>", "", nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error from hanging worker")
	}
	// Should have returned within a reasonable window around the 10 s timeout.
	if elapsed > 15*time.Second {
		t.Errorf("took %s, expected roughly 10 s", elapsed)
	}
}

// ---------------------------------------------------------------------------
// 4. Malformed JSON from worker
// ---------------------------------------------------------------------------

func TestExtractHTML_MalformedWorkerResponse(t *testing.T) {
	py := pythonAvailable()
	if py == "" {
		t.Skip("Python not available")
	}

	dir := t.TempDir()
	workerPath := filepath.Join(dir, "worker.py")
	// Worker that echoes garbage regardless of input.
	err := os.WriteFile(workerPath, []byte(`import sys
for line in sys.stdin:
    sys.stdout.write("not valid json\n")
    sys.stdout.flush()
`), 0o644)
	if err != nil {
		t.Fatalf("write fake worker: %v", err)
	}

	c := scrapling.NewWithPython(py, workerPath)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.ExtractHTML(context.Background(), "<html><body>test</body></html>", "", nil)
	if err == nil {
		t.Fatal("expected error from malformed JSON response")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Logf("error (acceptable): %v", err)
	}
}

// ---------------------------------------------------------------------------
// 5. Oversized output
// ---------------------------------------------------------------------------

func TestExtractHTML_OversizedOutput(t *testing.T) {
	py := pythonAvailable()
	if py == "" {
		t.Skip("Python not available")
	}

	dir := t.TempDir()
	workerPath := filepath.Join(dir, "worker.py")
	// Worker that returns a line larger than 2 MiB.
	err := os.WriteFile(workerPath, []byte(`import sys
for line in sys.stdin:
    # write 3 MiB of 'x' as a JSON string value
    big = '{"version":"1.0","id":"req-1","success":true,"data":{"title":"","article":"' + 'x' * (3 * 1024 * 1024) + '","headings":[],"links":[],"tables":[],"metadata":{}}}'
    sys.stdout.write(big + "\n")
    sys.stdout.flush()
`), 0o644)
	if err != nil {
		t.Fatalf("write fake worker: %v", err)
	}

	c := scrapling.NewWithPython(py, workerPath)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.ExtractHTML(context.Background(), "<html><body>test</body></html>", "", nil)
	if err == nil {
		t.Fatal("expected error for oversized output")
	}
}

// ---------------------------------------------------------------------------
// 6. Oversized input payload
// ---------------------------------------------------------------------------

func TestExtractHTML_OversizedInput(t *testing.T) {
	py := pythonAvailable()
	if py == "" {
		t.Skip("Python not available")
	}
	c := newClient(t)
	t.Cleanup(func() { _ = c.Close() })

	// Create a payload that serialises to > 5 MiB.
	bigHTML := strings.Repeat("a", 6*1024*1024)
	_, err := c.ExtractHTML(context.Background(), bigHTML, "", nil)
	if err == nil {
		t.Fatal("expected error for oversized input")
	}
	if !strings.Contains(err.Error(), "5 MiB") {
		t.Logf("error (acceptable): %v", err)
	}
}

// ---------------------------------------------------------------------------
// 7. Normal extraction — integration test (requires scrapling)
// ---------------------------------------------------------------------------

func TestExtractHTML_NormalExtraction(t *testing.T) {
	if !scraplingAvailable(t) {
		t.Skip("scrapling not installed; skipping integration test")
	}

	c := newClient(t)
	t.Cleanup(func() { _ = c.Close() })

	const sampleHTML = `<!DOCTYPE html>
<html>
<head>
  <title>Test Page</title>
  <meta name="description" content="A test page">
  <meta name="author" content="Tester">
  <link rel="canonical" href="https://example.com/test">
</head>
<body>
  <article>
    <h1>Main Heading</h1>
    <h2>Sub Heading</h2>
    <p>Some article content here.</p>
    <a href="https://example.com/link">Example Link</a>
    <table>
      <thead><tr><th>Col A</th><th>Col B</th></tr></thead>
      <tbody><tr><td>val1</td><td>val2</td></tr></tbody>
    </table>
  </article>
</body>
</html>`

	doc, err := c.ExtractHTML(context.Background(), sampleHTML, "https://example.com/test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Title
	if doc.Title != "Test Page" {
		t.Errorf("title = %q, want %q", doc.Title, "Test Page")
	}
	// Backend tag
	if doc.Backend != "scrapling" {
		t.Errorf("backend = %q, want %q", doc.Backend, "scrapling")
	}
	// URL round-trip
	if doc.URL != "https://example.com/test" {
		t.Errorf("URL = %q, want %q", doc.URL, "https://example.com/test")
	}
	// At least one heading
	if len(doc.Headings) == 0 {
		t.Error("expected at least one heading")
	} else {
		found := false
		for _, h := range doc.Headings {
			if h.Text == "Main Heading" && h.Level == 1 {
				found = true
			}
		}
		if !found {
			t.Errorf("expected h1 'Main Heading' in headings, got %+v", doc.Headings)
		}
	}
	// At least one link
	if len(doc.Links) == 0 {
		t.Error("expected at least one link")
	}
	// At least one table
	if len(doc.Tables) == 0 {
		t.Error("expected at least one table")
	} else {
		tbl := doc.Tables[0]
		if len(tbl.Headers) != 2 {
			t.Errorf("table headers = %v, want 2", tbl.Headers)
		}
	}
	// Metadata round-trip
	if doc.Description == "" {
		t.Error("expected description from meta tag")
	}
	// Article / Markdown non-empty
	if doc.Markdown == "" {
		t.Error("expected non-empty markdown/article text")
	}
}

// ---------------------------------------------------------------------------
// 8. Worker crash detection (stdout close mid-session)
// ---------------------------------------------------------------------------

func TestExtractHTML_WorkerCrashDetected(t *testing.T) {
	py := pythonAvailable()
	if py == "" {
		t.Skip("Python not available")
	}

	dir := t.TempDir()
	workerPath := filepath.Join(dir, "worker.py")
	// Worker that exits immediately after first line.
	err := os.WriteFile(workerPath, []byte(`import sys
line = sys.stdin.readline()
# crash without writing a response
sys.exit(1)
`), 0o644)
	if err != nil {
		t.Fatalf("write fake worker: %v", err)
	}

	c := scrapling.NewWithPython(py, workerPath)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.ExtractHTML(context.Background(), "<html><body>test</body></html>", "", nil)
	if err == nil {
		t.Fatal("expected error when worker crashes without responding")
	}
}

// ---------------------------------------------------------------------------
// 9. Clean shutdown and process cleanup
// ---------------------------------------------------------------------------

func TestClose_CleanShutdown(t *testing.T) {
	py := pythonAvailable()
	if py == "" {
		t.Skip("Python not available")
	}

	dir := t.TempDir()
	workerPath := filepath.Join(dir, "worker.py")
	// A well-behaved worker that exits on EOF.
	err := os.WriteFile(workerPath, []byte(`import sys, json
for line in sys.stdin:
    req = json.loads(line)
    sys.stdout.write(json.dumps({"version":"1.0","id":req.get("id",""),"success":True,"data":{"title":"","article":"","headings":[],"links":[],"tables":[],"metadata":{}}}) + "\n")
    sys.stdout.flush()
`), 0o644)
	if err != nil {
		t.Fatalf("write fake worker: %v", err)
	}

	c := scrapling.NewWithPython(py, workerPath)

	// Force spawn by doing one extraction.
	_, _ = c.ExtractHTML(context.Background(), "<html><body>x</body></html>", "", nil)

	// Close should not block.
	done := make(chan error, 1)
	go func() { done <- c.Close() }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close() error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("Close() blocked for >5s — process not cleaned up")
	}
}

// ---------------------------------------------------------------------------
// 10. Worker.py protocol unit tests (run worker.py as subprocess directly)
// ---------------------------------------------------------------------------

func TestWorkerProtocol_ValidExtract(t *testing.T) {
	py := pythonAvailable()
	if py == "" {
		t.Skip("Python not available")
	}
	if !scraplingAvailable(t) {
		t.Skip("scrapling not installed")
	}

	wPath := filepath.Join(scriptDir(t), "worker.py")
	cmd := exec.Command(py, wPath)
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	})

	req := map[string]any{
		"version": "1.0",
		"id":      "test-1",
		"action":  "extract",
		"html":    "<html><head><title>Hello</title></head><body><h1>Hi</h1></body></html>",
		"url":     "https://example.com",
		"targets": []string{"article", "headings", "metadata"},
	}
	payload, _ := json.Marshal(req)
	_, _ = fmt.Fprintf(stdin, "%s\n", payload)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	if !scanner.Scan() {
		t.Fatalf("no response from worker: %v", scanner.Err())
	}

	var resp map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if v, _ := resp["success"].(bool); !v {
		t.Fatalf("worker returned success=false: %v", resp["error"])
	}
	data, _ := resp["data"].(map[string]any)
	if data == nil {
		t.Fatal("missing data field")
	}
	if title, _ := data["title"].(string); title != "Hello" {
		t.Errorf("title = %q, want %q", title, "Hello")
	}
}

func TestWorkerProtocol_MalformedJSON(t *testing.T) {
	py := pythonAvailable()
	if py == "" {
		t.Skip("Python not available")
	}

	wPath := filepath.Join(scriptDir(t), "worker.py")
	cmd := exec.Command(py, wPath)
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	})

	_, _ = fmt.Fprintf(stdin, "this is not json\n")
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("no response: %v", scanner.Err())
	}

	var resp map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if success, _ := resp["success"].(bool); success {
		t.Error("expected success=false for malformed JSON input")
	}
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, "JSON") && !strings.Contains(errMsg, "json") {
		t.Errorf("error message should mention JSON: %q", errMsg)
	}
}

func TestWorkerProtocol_UnknownAction(t *testing.T) {
	py := pythonAvailable()
	if py == "" {
		t.Skip("Python not available")
	}

	wPath := filepath.Join(scriptDir(t), "worker.py")
	cmd := exec.Command(py, wPath)
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	})

	req := map[string]any{
		"version": "1.0",
		"id":      "x",
		"action":  "fetch",  // not a valid action
		"html":    "<html/>",
		"url":     "",
		"targets": []string{},
	}
	payload, _ := json.Marshal(req)
	_, _ = fmt.Fprintf(stdin, "%s\n", payload)

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("no response")
	}
	var resp map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if success, _ := resp["success"].(bool); success {
		t.Error("expected success=false for unknown action")
	}
}

func TestWorkerProtocol_GracefulEOF(t *testing.T) {
	py := pythonAvailable()
	if py == "" {
		t.Skip("Python not available")
	}

	wPath := filepath.Join(scriptDir(t), "worker.py")
	cmd := exec.Command(py, wPath)
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	stdin, _ := cmd.StdinPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}

	// Close stdin → EOF → worker should exit cleanly.
	_ = stdin.Close()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
		// Good — worker exited.
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Error("worker did not exit after stdin EOF within 5 s")
	}
}

func TestWorkerProtocol_UnsupportedVersion(t *testing.T) {
	py := pythonAvailable()
	if py == "" {
		t.Skip("Python not available")
	}

	wPath := filepath.Join(scriptDir(t), "worker.py")
	cmd := exec.Command(py, wPath)
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	})

	req := map[string]any{
		"version": "9.9",
		"id":      "x",
		"action":  "extract",
		"html":    "<html/>",
		"url":     "",
		"targets": []string{},
	}
	payload, _ := json.Marshal(req)
	_, _ = fmt.Fprintf(stdin, "%s\n", payload)

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("no response")
	}
	var resp map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if success, _ := resp["success"].(bool); success {
		t.Error("expected success=false for unsupported version")
	}
}

// ---------------------------------------------------------------------------
// 11. Concurrent extraction safety
// ---------------------------------------------------------------------------

func TestExtractHTML_ConcurrentSafe(t *testing.T) {
	if !scraplingAvailable(t) {
		t.Skip("scrapling not installed")
	}

	c := newClient(t)
	t.Cleanup(func() { _ = c.Close() })

	const workers = 5
	var wg sync.WaitGroup
	errs := make([]error, workers)

	for i := range workers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			html := fmt.Sprintf("<html><body><h1>Page %d</h1></body></html>", idx)
			_, err := c.ExtractHTML(context.Background(), html, "", nil)
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("worker %d: %v", i, err)
		}
	}
}

// ---------------------------------------------------------------------------
// 12. Empty HTML input is rejected before subprocess
// ---------------------------------------------------------------------------

func TestExtractHTML_EmptyHTML(t *testing.T) {
	c := newClient(t)
	t.Cleanup(func() { _ = c.Close() })

	_, err := c.ExtractHTML(context.Background(), "", "", nil)
	if err == nil {
		t.Fatal("expected error for empty HTML")
	}
}

// ---------------------------------------------------------------------------
// 13. Explicit Python path validation tests
// ---------------------------------------------------------------------------

func TestClient_MissingExplicitPythonPath(t *testing.T) {
	c := scrapling.NewWithPython("non_existent_python_binary_xyz_12345", "/fake/worker.py")
	t.Cleanup(func() { _ = c.Close() })

	_, err := c.ExtractHTML(context.Background(), "<html><body>test</body></html>", "", nil)
	if err == nil {
		t.Fatal("expected error for non-existent explicit Python binary")
	}
	if !errors.Is(err, web.ErrBackendNotFound) {
		t.Errorf("expected ErrBackendNotFound, got: %v", err)
	}
	if !strings.Contains(err.Error(), "pip install scrapling") {
		t.Errorf("expected actionable guidance 'pip install scrapling', got: %v", err)
	}
}

func TestClient_ValidConfiguredInterpreter(t *testing.T) {
	py := pythonAvailable()
	if py == "" {
		t.Skip("no Python in PATH; skipping valid interpreter test")
	}

	c := scrapling.NewWithPython(py, "")
	t.Cleanup(func() { _ = c.Close() })

	_, err := c.ExtractHTML(context.Background(), "<html><body><h1>Title</h1></body></html>", "", nil)
	if err != nil {
		// If scrapling is not installed, it must be wrapped with ErrBackendNotFound
		if !errors.Is(err, web.ErrBackendNotFound) {
			t.Fatalf("expected ErrBackendNotFound when scrapling not installed, got: %v", err)
		}
		if !strings.Contains(err.Error(), "pip install scrapling") {
			t.Errorf("expected guidance 'pip install scrapling', got: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// helpers — unexported for test package only
// ---------------------------------------------------------------------------

// forceWorkerRead reads the entire stdout of a command into a buffer for
// inspection.  Used in a few one-shot worker-protocol tests.
func forceWorkerRead(r io.Reader, maxBytes int) ([]byte, error) {
	buf := make([]byte, maxBytes)
	n, err := r.Read(buf)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf[:n], nil
}

// Ensure unused import does not fail compilation.
var _ = bytes.NewBuffer
