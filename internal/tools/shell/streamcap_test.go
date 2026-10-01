package shell

import (
	"bufio"
	"io"
	"strings"
	"testing"

	"forcefield/internal/tools"
)

func readAllLines(t *testing.T, input string) (lines []string, truncated []bool) {
	t.Helper()
	r := bufio.NewReader(strings.NewReader(input))
	for {
		line, trunc, err := readBoundedLine(r)
		// Mirror streamPipe: the terminal EOF call carries no content
		// and appends nothing.
		if line != "" || err == nil {
			lines = append(lines, line)
			truncated = append(truncated, trunc)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("readBoundedLine error = %v", err)
		}
	}
	return lines, truncated
}

func TestReadBoundedLineWhole(t *testing.T) {
	lines, truncated := readAllLines(t, "a\n\nb\n")
	if len(lines) != 3 || lines[0] != "a" || lines[1] != "" || lines[2] != "b" {
		t.Fatalf("lines = %q", lines)
	}
	for _, tr := range truncated {
		if tr {
			t.Error("short lines must not truncate")
		}
	}
}

func TestReadBoundedLineUnterminatedTail(t *testing.T) {
	lines, truncated := readAllLines(t, "tail")
	if len(lines) != 1 || lines[0] != "tail" || truncated[0] {
		t.Fatalf("tail = %q truncated=%v", lines, truncated)
	}
}

// A newline-less flood stays bounded: content is capped, the remainder
// discarded, and the call completes instead of growing to the input.
func TestReadBoundedLineCapsFlood(t *testing.T) {
	flood := strings.Repeat("A", 5<<20) // 5 MiB, no newline (Phase 0 shape)
	r := bufio.NewReader(strings.NewReader(flood))
	line, trunc, err := readBoundedLine(r)
	if err != io.EOF {
		t.Fatalf("err = %v, want EOF after the tail", err)
	}
	if !trunc {
		t.Fatal("5MB line must report truncated")
	}
	if len(line) != maxStreamLineBytes {
		t.Fatalf("kept %d bytes, want cap %d", len(line), maxStreamLineBytes)
	}
}

func TestReadBoundedLineExactCapWhole(t *testing.T) {
	exact := strings.Repeat("B", maxStreamLineBytes) + "\n"
	r := bufio.NewReader(strings.NewReader(exact))
	line, trunc, err := readBoundedLine(r)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if trunc || len(line) != maxStreamLineBytes {
		t.Fatalf("exact-cap line must stay whole: len=%d trunc=%v", len(line), trunc)
	}
}

// End to end through streamPipe: a capped line carries its head plus
// the marker, later lines still flow, and totals stay bounded.
func TestStreamPipeLineCap(t *testing.T) {
	var input strings.Builder
	input.WriteString("short\n")
	input.WriteString(strings.Repeat("L", 1<<20) + "\n")
	input.WriteString("after\n")
	dst := &shellOutput{max: tools.DefaultShellMaxBytes}
	done := make(chan struct{}, 1)
	streamPipe(strings.NewReader(input.String()), "stdout", dst, nil, done)
	<-done
	out := dst.stdoutString()
	if !strings.Contains(out, "short") || !strings.Contains(out, "after") {
		t.Fatalf("neighboring lines lost:\n%.200s", out)
	}
	if !strings.Contains(out, "[...line truncated at 256 KiB]") {
		t.Fatalf("missing line-truncation marker:\n%.200s", out)
	}
	if dst.totalBytes() > tools.DefaultShellMaxBytes {
		t.Fatalf("total %d exceeds budget %d", dst.totalBytes(), tools.DefaultShellMaxBytes)
	}
}
