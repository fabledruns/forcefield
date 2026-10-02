package collect

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// longLivedChild is how much the helper process allocates and touches,
// giving tests a live process with a resident set measurably larger
// than the test binary's own. Kept modest: several run concurrently.
const longLivedChild = 8 << 20 // 8 MiB

// TestHelperChild re-execs this test binary as a resident-memory child.
// It is not a test: without the environment gate it returns immediately.
//
// With HPOV_COLLECT_GRANDCHILD set it also forks one child of its own
// and reports that pid on stdout, so the parent test owns the whole
// tree's cleanup instead of relying on a killed parent to reap it.
func TestHelperChild(t *testing.T) {
	if os.Getenv("HPOV_COLLECT_CHILD") == "" {
		return
	}
	buf := make([]byte, longLivedChild)
	// Touch every page so the allocation is resident, not reserved.
	for i := 0; i < len(buf); i += 4096 {
		buf[i] = byte(i)
	}
	if os.Getenv("HPOV_COLLECT_GRANDCHILD") != "" {
		self, err := os.Executable()
		if err == nil {
			gc := exec.Command(self, "-test.run=TestHelperChild")
			gc.Env = append(os.Environ(), "HPOV_COLLECT_CHILD=1")
			if err := gc.Start(); err == nil {
				fmt.Fprintf(os.Stdout, "grandchild=%d\n", gc.Process.Pid)
			}
		}
	}
	_, _ = os.Stdout.WriteString("resident\n")
	time.Sleep(60 * time.Second)
}

func startChild(t *testing.T, env ...string) (*exec.Cmd, func()) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=TestHelperChild")
	cmd.Env = append(append(os.Environ(), "HPOV_COLLECT_CHILD=1"), env...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Read the helper's handshake so the allocation has happened
	// before anything is measured: "grandchild=<pid>" when one was
	// requested, then "resident".
	br := bufio.NewReader(stdout)
	wantGrandchild := os.Getenv("HPOV_COLLECT_GRANDCHILD") != ""
	var grandchild int
	ready := false
	for !ready {
		line, rerr := br.ReadString('\n')
		if rerr != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			t.Fatalf("child readiness: %v", rerr)
		}
		switch line = strings.TrimSpace(line); {
		case strings.HasPrefix(line, "grandchild="):
			g, err := strconv.Atoi(strings.TrimPrefix(line, "grandchild="))
			if err != nil {
				t.Fatalf("grandchild pid %q: %v", line, err)
			}
			grandchild = g
		case line == "resident":
			ready = !wantGrandchild || grandchild > 0
		}
	}
	lastChild = cmd
	cleanup := func() {
		// Kill the grandchild first: it outlives a killed parent, and
		// leaving it behind would leak a 8 MiB process per test.
		if grandchild > 0 {
			killPID(grandchild)
		}
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	return cmd, cleanup
}

// killPID terminates a process by id, used only for helper children.
func killPID(pid int) {
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = p.Kill()
}

// lastChild is the most recently started helper child; platform tests
// read its pid without threading it through every helper.
var lastChild *exec.Cmd

func lastPID() int {
	if lastChild == nil || lastChild.Process == nil {
		return 0
	}
	return lastChild.Process.Pid
}

func TestReadingOfSelf(t *testing.T) {
	r, err := ReadingOf(os.Getpid())
	if err != nil {
		t.Fatalf("self reading: %v", err)
	}
	if !r.Ok() {
		t.Fatalf("self unreadable: %+v", r)
	}
	if r.RSSBytes < 1<<20 {
		t.Fatalf("self RSS %d implausibly small for a test binary", r.RSSBytes)
	}
	if r.RSSSemantics == "" {
		t.Fatal("reading must name its semantics")
	}
	// Peak must never be below current RSS when the OS tracks it.
	if r.PeakBytes >= 0 && r.PeakBytes < r.RSSBytes {
		t.Fatalf("peak %d < current %d", r.PeakBytes, r.RSSBytes)
	}
}

func TestReadingOfChildExceedsSelf(t *testing.T) {
	// The psapi path is checked against a known child, not against ff.
	_, cleanup := startChild(t)
	defer cleanup()

	r, err := ReadingOf(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	c, err := ReadingOf(lastPID())
	if err != nil {
		t.Fatalf("child reading: %v", err)
	}
	// The child holds 24 MiB that this process does not.
	if c.RSSBytes < r.RSSBytes+longLivedChild/2 {
		t.Fatalf("child RSS %d vs self %d: psapi path not reading real memory",
			c.RSSBytes, r.RSSBytes)
	}
}

func TestReadingExitedProcess(t *testing.T) {
	// A reaped PID is unavailable with a reason, never zero.
	pid := startAndReap(t)
	r, err := ReadingOf(pid)
	if err == nil {
		t.Skip("PID was reused by a live process; skip")
	}
	if r.RSSBytes != -1 {
		t.Fatalf("exited process must report -1, got %d", r.RSSBytes)
	}
	if r.Reason == "" {
		t.Fatalf("exited process must carry a reason: %+v", r)
	}
}

func startAndReap(t *testing.T) int {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=TestHelperChild")
	cmd.Env = append(os.Environ(), "HPOV_COLLECT_CHILD=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	return pid
}

func TestReadingNoProcess(t *testing.T) {
	if _, err := ReadingOf(0); err == nil {
		t.Fatal("pid 0 must be unavailable")
	}
	if _, err := ReadingOf(-1); err == nil {
		t.Fatal("negative pid must be unavailable")
	}
}

func TestReadingUnsupportedPlatform(t *testing.T) {
	// The fallback path must report unavailable rather than invent a
	// number. Exercised directly because the host is supported.
	r, err := readProcess(-7)
	if r.RSSBytes != -1 || err == nil {
		t.Skip("this build uses a supported collector")
	}
	if r.RSSSemantics != "" {
		t.Fatalf("unsupported reading must have no semantics: %+v", r)
	}
}

func TestTreeIncludesDescendants(t *testing.T) {
	cmd, cleanup := startChild(t, "HPOV_COLLECT_GRANDCHILD=1")
	defer cleanup()
	lastChild = cmd

	root := cmd.Process.Pid
	kids, err := descendants(root)
	if err != nil {
		t.Fatalf("descendants: %v", err)
	}
	if len(kids) == 0 {
		t.Skip("grandchild exited before enumeration")
	}

	tree, err := TreeOf(root)
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if tree.Descendants < 1 {
		t.Fatalf("descendants=%d, want >=1", tree.Descendants)
	}
	// The summed tree must exceed the root alone by the grandchild's
	// resident set.
	rootOnly, err := ReadingOf(root)
	if err != nil {
		t.Fatal(err)
	}
	if tree.RSSBytes <= rootOnly.RSSBytes {
		t.Fatalf("tree sum %d not greater than root %d: descendants not summed",
			tree.RSSBytes, rootOnly.RSSBytes)
	}
	if tree.Semantics == "" {
		t.Fatal("tree must name its semantics")
	}
	if len(tree.PIDs) != tree.Descendants+1 {
		t.Fatalf("pids=%d descendants=%d", len(tree.PIDs), tree.Descendants)
	}
}

func TestTreeOfNoProcess(t *testing.T) {
	if _, err := TreeOf(0); err == nil {
		t.Fatal("tree of pid 0 must be unavailable")
	}
}

func TestTreeExitedRoot(t *testing.T) {
	pid := startAndReap(t)
	tree, err := TreeOf(pid)
	if tree.RSSBytes != -1 || err == nil {
		t.Skip("pid reused; skip")
	}
	if tree.Reason == "" {
		t.Fatalf("exited root must carry a reason: %+v", tree)
	}
}

func TestSamplerTracksPeak(t *testing.T) {
	_, cleanup := startChild(t)
	defer cleanup()

	s := NewSampler()
	if s.Interval() != DefaultInterval {
		t.Fatalf("interval = %v, want %v", s.Interval(), DefaultInterval)
	}
	var peak int64
	for i := 0; i < 5; i++ {
		if v := s.Sample(os.Getpid()); v > peak {
			peak = v
		}
	}
	rep := s.Report()
	if rep.Samples != 5 {
		t.Fatalf("samples = %d, want 5", rep.Samples)
	}
	if rep.PeakBytes != peak {
		t.Fatalf("reported peak %d != observed %d", rep.PeakBytes, peak)
	}
	if !rep.Sampled {
		t.Fatal("sampler peaks are sampled and must say so")
	}
	if rep.PeakBytes < 0 {
		t.Fatal("peak must be a real value")
	}
}

func TestSamplerNoSamplesReportsUnavailable(t *testing.T) {
	s := NewSampler()
	rep := s.Report()
	if rep.PeakBytes != -1 {
		t.Fatalf("empty sampler must report -1, got %d", rep.PeakBytes)
	}
	if rep.Samples != 0 {
		t.Fatalf("samples = %d, want 0", rep.Samples)
	}
}

func TestSamplerFailedSampleKeepsReason(t *testing.T) {
	pid := startAndReap(t)
	s := NewSampler()
	if v := s.Sample(pid); v != -1 {
		t.Skip("pid reused; skip")
	}
	rep := s.Report()
	if rep.Failures == 0 || rep.Reason == "" {
		t.Fatalf("failed sample must record a reason: %+v", rep)
	}
}

func TestSemanticsNeverVirtualMemory(t *testing.T) {
	// Guards the requirement that no virtual-memory quantity is ever
	// reported as resident memory.
	r, err := ReadingOf(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	switch r.RSSSemantics {
	case "windows:working_set", "linux:vm_rss", "darwin:resident_size":
	default:
		t.Fatalf("unexpected RSS semantics %q", r.RSSSemantics)
	}
	for _, forbidden := range []string{"vmsize", "commit", "private", "peak_pagefile"} {
		if strings.Contains(r.RSSSemantics, forbidden) {
			t.Fatalf("RSS semantics must not be virtual memory: %q", r.RSSSemantics)
		}
	}
}

func TestDarwinPeakUnavailable(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only contract")
	}
	r, err := ReadingOf(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if r.PeakBytes != -1 || r.PeakSemantics != "" {
		t.Fatalf("darwin live peak must be unavailable: %+v", r)
	}
}

func TestErrorsCarryReason(t *testing.T) {
	err := unavailable(ReasonExited, os.ErrNotExist)
	e, ok := err.(*Error)
	if !ok {
		t.Fatal("unavailable must return *Error")
	}
	if reasonOf(err) != ReasonExited {
		t.Fatalf("reason = %q", reasonOf(err))
	}
	if e.Unwrap() != os.ErrNotExist {
		t.Fatal("cause must survive")
	}
	if !strings.Contains(err.Error(), ReasonExited) {
		t.Fatalf("error text must name the reason: %v", err)
	}
}

func TestUnsupportedSemanticsEmpty(t *testing.T) {
	// The fallback collector must not attach semantics to a value it
	// never produced.
	r, err := readProcess(0)
	if err == nil {
		t.Skip("host uses a supported collector")
	}
	if r.RSSSemantics != "" || r.PeakSemantics != "" {
		t.Fatalf("unavailable reading must have no semantics: %+v", r)
	}
	if r.PeakBytes != -1 {
		t.Fatalf("unavailable peak must be -1, got %d", r.PeakBytes)
	}
}
