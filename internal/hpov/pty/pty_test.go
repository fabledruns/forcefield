package pty

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestMain re-execs the test binary as a scripted pty child when
// HPOV_PTY_FAKE=1. Modes:
//
//	__echo            print one line to stdout, exit 0
//	__exit N          exit N
//	__sleep-ms N      sleep, exit 0
//	__hang            sleep 300s (killed by the test)
//	__big             write 2MiB to stdout, exit 0
//	__markers         marker-ish lines to stderr with delays, exit 0
//	__winsize         print "ROWS r COLS c" of stdout, exit 0
func TestMain(m *testing.M) {
	if os.Getenv("HPOV_PTY_FAKE") != "1" {
		os.Exit(m.Run())
	}
	os.Exit(fakeMain(os.Args[1:]))
}

func fakeMain(args []string) int {
	if len(args) == 0 {
		return 2
	}
	getenv := func(k string) string { return os.Getenv(k) }
	_ = getenv
	switch args[0] {
	case "__echo":
		_, _ = os.Stdout.WriteString("hello-pty\n")
		return 0
	case "__exit":
		n, _ := strconv.Atoi(getenv("N"))
		return n
	case "__sleep-ms":
		ms, _ := strconv.Atoi(getenv("MS"))
		time.Sleep(time.Duration(ms) * time.Millisecond)
		return 0
	case "__hang":
		time.Sleep(300 * time.Second)
		return 0
	case "__big":
		chunk := bytes.Repeat([]byte("x"), 1<<20)
		_, _ = os.Stdout.Write(chunk)
		_, _ = os.Stdout.Write(chunk)
		return 0
	case "__markers":
		for _, ev := range []string{"main-entry", "config-loaded", "first-useful-frame"} {
			_, _ = os.Stderr.WriteString("ff-perf " + ev + "\n")
			time.Sleep(20 * time.Millisecond)
		}
		return 0
	case "__winsize":
		r, c, err := queryWinsize()
		if err != nil {
			_, _ = os.Stdout.WriteString("ERR " + err.Error() + "\n")
			return 1
		}
		_, _ = os.Stdout.WriteString("ROWS " + strconv.Itoa(r) + " COLS " + strconv.Itoa(c) + "\n")
		return 0
	case "__consoleinfo":
		return consoleSelfReport()
	}
	return 2
}

func fakeEnv(extra ...string) []string {
	env := []string{"HPOV_PTY_FAKE=1", "PATH=" + os.Getenv("PATH")}
	if os.Getenv("SystemRoot") != "" {
		env = append(env, "SystemRoot="+os.Getenv("SystemRoot"))
	}
	return append(env, extra...)
}

func fakeSubject(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return self
}

// readLine reads one line without waiting for EOF: pty masters
// only report EOF after the console is closed, so tests must consume
// exact content, then Wait, then Close.
func readLine(t *testing.T, f *os.File) string {
	t.Helper()
	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && line == "" {
		t.Fatalf("read: %v", err)
	}
	return line
}

func TestLifecycleEcho(t *testing.T) {
	c, err := Start(Options{Path: fakeSubject(t), Args: []string{"__echo"}, Env: fakeEnv()})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = c.Close() }()
	if c.Pid() <= 0 {
		t.Fatalf("pid = %d", c.Pid())
	}
	if line := readLine(t, c.Output()); !strings.Contains(line, "hello-pty") {
		t.Fatalf("output = %q", line)
	}
	if code, err := c.Wait(); err != nil || code != 0 {
		t.Fatalf("wait = %d, %v", code, err)
	}
	if c.Alive() {
		t.Fatal("child still alive after wait")
	}
}

func TestExitCode(t *testing.T) {
	c, err := Start(Options{Path: fakeSubject(t), Args: []string{"__exit"},
		Env: append(fakeEnv(), "N=3")})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = c.Close() }()
	go func() { _, _ = io.Copy(io.Discard, c.Output()) }()
	if code, err := c.Wait(); err != nil || code != 3 {
		t.Fatalf("wait = %d, %v", code, err)
	}
}

func TestDrainBigOutput(t *testing.T) {
	// 2MiB >> 64KiB pipe/ConPTY buffers: proves continuous draining.
	// Draining runs concurrently with Wait: ConPTY framing bytes mean
	// the stream is slightly larger than the payload, so an exact
	// read would stop early and wedge the child mid-write.
	c, err := Start(Options{Path: fakeSubject(t), Args: []string{"__big"}, Env: fakeEnv()})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = c.Close() }()
	var drained atomic.Int64
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		buf := make([]byte, 32*1024)
		for {
			n, rerr := c.Output().Read(buf)
			if n > 0 {
				drained.Add(int64(n))
			}
			if rerr != nil {
				return
			}
		}
	}()
	if code, err := c.Wait(); err != nil || code != 0 {
		t.Fatalf("wait = %d, %v", code, err)
	}
	// Console teardown unblocks the drainer with EOF.
	_ = c.Close()
	<-drainDone
	if got := drained.Load(); got < 2*(1<<20) {
		t.Fatalf("drained %d bytes, want >= %d", got, 2*(1<<20))
	}
}

func TestKillHang(t *testing.T) {
	c, err := Start(Options{Path: fakeSubject(t), Args: []string{"__hang"}, Env: fakeEnv()})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = c.Close() }()
	go func() { _, _ = io.Copy(io.Discard, c.Output()) }()
	if !c.Alive() {
		t.Fatal("child not alive after start")
	}
	start := time.Now()
	if err := c.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if _, err := c.Wait(); err != nil {
		t.Fatalf("wait after kill: %v", err)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatal("kill did not terminate promptly")
	}
	if c.Alive() {
		t.Fatal("child alive after kill+wait")
	}
}

func TestMarkerStream(t *testing.T) {
	c, err := Start(Options{Path: fakeSubject(t), Args: []string{"__markers"}, Env: fakeEnv()})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = c.Close() }()
	// Markers must surface on Stderr() on every platform, as their own
	// line, in emission order. Every platform therefore uses a separate
	// stderr pipe: the ConPTY console stream reorders writes that arrive
	// milliseconds apart.
	br := bufio.NewReader(c.Stderr())
	var got strings.Builder
	for i := 0; i < 3; i++ {
		line, rerr := br.ReadString('\n')
		got.WriteString(line)
		if rerr != nil {
			t.Fatalf("marker line %d: %v (read so far: %q)", i, rerr, got.String())
		}
	}
	lines := strings.Split(strings.TrimRight(got.String(), "\n"), "\n")
	want := []string{"ff-perf main-entry", "ff-perf config-loaded", "ff-perf first-useful-frame"}
	for i, w := range want {
		if strings.TrimRight(lines[i], "\r") != w {
			t.Fatalf("line %d = %q, want %q", i, lines[i], w)
		}
	}
	// TUI output stays on the console stream, separate from markers.
	if c.Stderr() == c.Output() {
		t.Fatal("markers must not share the console output stream")
	}
	if code, err := c.Wait(); err != nil || code != 0 {
		t.Fatalf("wait = %d, %v", code, err)
	}
}

func TestWinsize(t *testing.T) {
	c, err := Start(Options{Path: fakeSubject(t), Args: []string{"__winsize"}, Env: fakeEnv()})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = c.Close() }()
	if line := readLine(t, c.Output()); !strings.Contains(line, "ROWS 40 COLS 120") {
		t.Fatalf("winsize = %q, want ROWS 40 COLS 120", line)
	}
	if code, err := c.Wait(); err != nil || code != 0 {
		t.Fatalf("wait = %d, %v", code, err)
	}
}

func TestChildConsoleAttached(t *testing.T) {
	// Regression guard: a silently un-attached child (missing
	// STARTF_USESTDHANDLES / inherited handles) reports err6 here
	// while its output leaks to the parent instead of the pty.
	c, err := Start(Options{Path: fakeSubject(t), Args: []string{"__consoleinfo"}, Env: fakeEnv()})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = c.Close() }()
	line := readLine(t, c.Output())
	if !strings.Contains(line, "REPORT") ||
		strings.Contains(line, "err6") || strings.Contains(line, "tty=err") {
		t.Fatalf("child has no console: %q", line)
	}
	if code, err := c.Wait(); err != nil || code != 0 {
		t.Fatalf("wait = %d, %v", code, err)
	}
}
