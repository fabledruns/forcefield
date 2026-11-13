// Package scrapling provides an HTML extraction backend that delegates to a
// Python subprocess running the Scrapling library for intelligent DOM parsing.
//
// The client manages the subprocess lifecycle, communicates over NDJSON
// (newline-delimited JSON) on stdin/stdout, and enforces strict I/O budgets.
// If Python or Scrapling is unavailable, it returns [web.ErrBackendNotFound]
// with actionable instructions instead of panicking.
package scrapling

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
	"time"

	"forcefield/internal/tools/web"
)

const (
	// extractionTimeout is the maximum wall time for one extract call.
	extractionTimeout = 10 * time.Second

	// inputSizeLimit caps the JSON payload sent to the worker (5 MiB).
	inputSizeLimit = 5 * 1024 * 1024

	// outputSizeLimit caps the JSON response read from the worker (2 MiB).
	outputSizeLimit = 2 * 1024 * 1024

	workerVersion = "1.0"
)

// workerRequest is the NDJSON line sent to worker.py.
type workerRequest struct {
	Version string   `json:"version"`
	ID      string   `json:"id"`
	Action  string   `json:"action"`
	HTML    string   `json:"html"`
	URL     string   `json:"url"`
	Targets []string `json:"targets"`
}

// workerHeading mirrors the Python worker heading struct.
type workerHeading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
}

// workerLink mirrors the Python worker link struct.
type workerLink struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

// workerTable mirrors the Python worker table struct.
type workerTable struct {
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows"`
}

// workerData holds the payload inside a successful worker response.
type workerData struct {
	Title    string            `json:"title"`
	Article  string            `json:"article"`
	Headings []workerHeading   `json:"headings"`
	Links    []workerLink      `json:"links"`
	Tables   []workerTable     `json:"tables"`
	Metadata map[string]string `json:"metadata"`
}

// workerResponse is the NDJSON line received from worker.py.
type workerResponse struct {
	Version string     `json:"version"`
	ID      string     `json:"id"`
	Success bool       `json:"success"`
	Error   string     `json:"error,omitempty"`
	Data    workerData `json:"data"`
}

// Client is an Extractor backed by a Python Scrapling subprocess.
// Use [New] to construct one.
type Client struct {
	mu       sync.Mutex
	python   string // resolved Python executable path
	verified bool   // true once python and scrapling have been verified
	script   string // absolute path to worker.py
	proc     *exec.Cmd
	stdin    io.WriteCloser
	scanner  *bufio.Scanner
	reqID    uint64
	dead     bool // true after crash / forced kill
}

// New creates a Client. It does not spawn the Python process yet; spawning is
// lazy (first call to ExtractHTML). scriptDir should be the directory
// containing worker.py; pass an empty string to use the directory derived from
// the running executable.
func New(scriptDir string) *Client {
	script := workerScript(scriptDir)
	return &Client{script: script}
}

// NewWithPython creates a Client with an explicit Python interpreter and worker script path.
func NewWithPython(python, scriptPath string) *Client {
	return &Client{
		python: python,
		script: scriptPath,
	}
}

// Name implements web.Extractor.
func (c *Client) Name() string { return "scrapling" }

// ExtractHTML implements web.Extractor.
//
// It sends rawHTML and baseURL to the Python worker and translates the
// response into a [web.Document].  Targets selects which sections to extract;
// pass nil or an empty slice to extract everything.
func (c *Client) ExtractHTML(ctx context.Context, rawHTML string, baseURL string, targets []string) (web.Document, error) {
	if len(rawHTML) == 0 {
		return web.Document{}, fmt.Errorf("scrapling: empty HTML input")
	}

	// Resolve Python once; fail fast with actionable message if missing.
	python, err := c.resolvePython()
	if err != nil {
		return web.Document{}, err
	}
	_ = python // stored in c.python after resolve

	c.mu.Lock()
	if err := c.ensureWorker(); err != nil {
		c.mu.Unlock()
		return web.Document{}, err
	}
	c.mu.Unlock()

	if len(targets) == 0 {
		targets = []string{"article", "headings", "links", "tables", "metadata"}
	}

	c.mu.Lock()
	c.reqID++
	id := fmt.Sprintf("req-%d", c.reqID)
	c.mu.Unlock()

	req := workerRequest{
		Version: workerVersion,
		ID:      id,
		Action:  "extract",
		HTML:    rawHTML,
		URL:     baseURL,
		Targets: targets,
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return web.Document{}, fmt.Errorf("scrapling: marshal request: %w", err)
	}
	if len(payload) > inputSizeLimit {
		return web.Document{}, fmt.Errorf("scrapling: input payload %d bytes exceeds 5 MiB limit", len(payload))
	}

	// Apply timeout via a context-aware wrapper.
	tctx, cancel := context.WithTimeout(ctx, extractionTimeout)
	defer cancel()

	type result struct {
		resp workerResponse
		err  error
	}
	done := make(chan result, 1)

	go func() {
		r, e := c.roundTrip(payload)
		done <- result{r, e}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			return web.Document{}, res.err
		}
		return toDocument(res.resp, baseURL), nil
	case <-tctx.Done():
		c.killWorker()
		return web.Document{}, fmt.Errorf("scrapling: extraction timed out after %s: %w", extractionTimeout, tctx.Err())
	}
}

// roundTrip sends one NDJSON line and reads one NDJSON response.
// Must be called with the worker alive (ensureWorker already done).
func (c *Client) roundTrip(payload []byte) (workerResponse, error) {
	c.mu.Lock()
	stdin := c.stdin
	scanner := c.scanner
	c.mu.Unlock()

	if stdin == nil || scanner == nil {
		return workerResponse{}, fmt.Errorf("scrapling: worker not running")
	}

	// Send request line.
	line := append(payload, '\n')
	if _, err := stdin.Write(line); err != nil {
		c.markDead()
		return workerResponse{}, fmt.Errorf("scrapling: write to worker: %w", err)
	}

	// Read response line.
	if !scanner.Scan() {
		err := scanner.Err()
		c.markDead()
		if err != nil {
			return workerResponse{}, fmt.Errorf("scrapling: read from worker: %w", err)
		}
		return workerResponse{}, fmt.Errorf("scrapling: worker closed stdout unexpectedly")
	}

	raw := scanner.Bytes()
	if len(raw) > outputSizeLimit {
		c.killWorker()
		return workerResponse{}, fmt.Errorf("scrapling: response %d bytes exceeds 2 MiB limit", len(raw))
	}

	var resp workerResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return workerResponse{}, fmt.Errorf("scrapling: malformed worker response: %w", err)
	}

	if !resp.Success {
		return workerResponse{}, fmt.Errorf("scrapling: worker error: %s", resp.Error)
	}

	return resp, nil
}

// ensureWorker starts the Python subprocess if it is not running.
// Must be called with c.mu held.
func (c *Client) ensureWorker() error {
	if c.proc != nil && !c.dead {
		return nil
	}
	// Reset dead flag before attempting fresh start.
	c.dead = false
	return c.startWorker()
}

// startWorker launches worker.py. Must be called with c.mu held.
func (c *Client) startWorker() error {
	cmd := exec.Command(c.python, c.script)
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("scrapling: create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("scrapling: create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found") {
			return fmt.Errorf("%w: python executable %q not found (%v)\n\nInstall Python 3.8+ and run: pip install scrapling",
				web.ErrBackendNotFound, c.python, err)
		}
		return fmt.Errorf("scrapling: start worker: %w", err)
	}

	// Use a size-limited scanner to cap output at outputSizeLimit per line.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, outputSizeLimit+1), outputSizeLimit+1)

	c.proc = cmd
	c.stdin = stdin
	c.scanner = scanner
	return nil
}

// killWorker forcefully terminates the subprocess and cleans up.
func (c *Client) killWorker() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forceKill()
}

// forceKill is the internal killer. Must be called with c.mu held.
func (c *Client) forceKill() {
	if c.proc != nil && c.proc.Process != nil {
		_ = c.proc.Process.Kill()
		_ = c.proc.Wait()
	}
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	c.proc = nil
	c.stdin = nil
	c.scanner = nil
	c.dead = false
}

// markDead signals that the worker process has died unexpectedly.
func (c *Client) markDead() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dead = true
}

// Close shuts down the worker subprocess gracefully.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stdin != nil {
		// Close stdin to trigger EOF shutdown in the Python process.
		_ = c.stdin.Close()
		c.stdin = nil
	}
	if c.proc != nil && c.proc.Process != nil {
		// Give it a moment to exit cleanly, then kill.
		done := make(chan struct{})
		go func() {
			_ = c.proc.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = c.proc.Process.Kill()
			<-done
		}
		c.proc = nil
	}
	c.scanner = nil
	c.dead = false
	return nil
}

// resolvePython resolves and verifies the Python executable and Scrapling dependency.
// Returns [web.ErrBackendNotFound] if Python is not found or Scrapling is not installed.
func (c *Client) resolvePython() (string, error) {
	c.mu.Lock()
	if c.verified && c.python != "" {
		py := c.python
		c.mu.Unlock()
		return py, nil
	}
	py := c.python
	c.mu.Unlock()

	if py == "" {
		found, err := findPython()
		if err != nil {
			return "", fmt.Errorf("%w: %s\n\nInstall Python 3.8+ and run: pip install scrapling",
				web.ErrBackendNotFound, err)
		}
		py = found
	} else {
		resolved, err := exec.LookPath(py)
		if err != nil {
			return "", fmt.Errorf("%w: configured Python executable %q not found (%v)\n\nInstall Python 3.8+ and run: pip install scrapling",
				web.ErrBackendNotFound, py, err)
		}
		py = resolved
	}

	// Probe: does `python -c "import scrapling"` succeed?
	probe := exec.Command(py, "-c", "import scrapling")
	var stderr bytes.Buffer
	probe.Stderr = &stderr
	if err := probe.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf(
			"%w: scrapling Python library is not installed (%s)\n\n"+
				"Install it with: pip install scrapling",
			web.ErrBackendNotFound, msg,
		)
	}

	c.mu.Lock()
	c.python = py
	c.verified = true
	c.mu.Unlock()
	return py, nil
}

// findPython locates a Python 3 interpreter.
func findPython() (string, error) {
	candidates := []string{"python3", "python"}
	if runtime.GOOS == "windows" {
		candidates = []string{"python", "python3", "py"}
	}
	for _, name := range candidates {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		// Verify it is Python 3.
		out, err := exec.Command(path, "--version").Output()
		if err != nil {
			continue
		}
		ver := strings.TrimSpace(string(out))
		if strings.HasPrefix(ver, "Python 3") {
			return path, nil
		}
	}
	return "", errors.New("python3 not found in PATH")
}

// workerScript returns the absolute path to worker.py. If scriptDir is empty,
// it falls back to the directory of the running executable.
func workerScript(scriptDir string) string {
	if scriptDir != "" {
		return filepath.Join(scriptDir, "worker.py")
	}
	exe, err := os.Executable()
	if err != nil {
		// Best-effort fallback.
		return "worker.py"
	}
	return filepath.Join(filepath.Dir(exe), "internal", "tools", "web", "scrapling", "worker.py")
}

// toDocument converts a workerResponse into a web.Document.
func toDocument(resp workerResponse, baseURL string) web.Document {
	d := resp.Data

	headings := make([]web.Heading, 0, len(d.Headings))
	for _, h := range d.Headings {
		headings = append(headings, web.Heading{Level: h.Level, Text: h.Text})
	}

	links := make([]web.Link, 0, len(d.Links))
	for _, l := range d.Links {
		links = append(links, web.Link{Text: l.Text, URL: l.URL})
	}

	tables := make([]web.Table, 0, len(d.Tables))
	for _, t := range d.Tables {
		tables = append(tables, web.Table{Headers: t.Headers, Rows: t.Rows})
	}

	title := d.Title
	description := ""
	author := ""
	published := ""
	canonicalURL := ""

	if d.Metadata != nil {
		if v, ok := d.Metadata["description"]; ok {
			description = v
		}
		if v, ok := d.Metadata["author"]; ok {
			author = v
		}
		if v, ok := d.Metadata["article:published_time"]; ok {
			published = v
		} else if v, ok := d.Metadata["published"]; ok {
			published = v
		}
		if v, ok := d.Metadata["canonical"]; ok {
			canonicalURL = v
		}
		if v, ok := d.Metadata["og:title"]; ok && title == "" {
			title = v
		}
		if v, ok := d.Metadata["og:description"]; ok && description == "" {
			description = v
		}
	}

	markdown := d.Article
	byteCount := len(d.Article)

	return web.Document{
		URL:          baseURL,
		CanonicalURL: canonicalURL,
		Title:        title,
		Description:  description,
		Author:       author,
		Published:    published,
		Markdown:     markdown,
		Headings:     headings,
		Links:        links,
		Tables:       tables,
		Metadata:     d.Metadata,
		ByteCount:    byteCount,
		Backend:      "scrapling",
	}
}
