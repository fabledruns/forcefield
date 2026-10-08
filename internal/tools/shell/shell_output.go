package shell

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"

	"forcefield/internal/tools"
)

// shellOutput holds capped stdout/stderr buffers with a shared byte budget.
// The same budget also bounds live streaming (see emitChunk): a chatty
// process must not be able to push unbounded bytes into the event stream
// and TUI transcript just because the accumulated result is capped.
type shellOutput struct {
	mu        sync.Mutex
	stdout    strings.Builder
	stderr    strings.Builder
	total     int
	dropped   int
	truncated bool
	// streamed counts bytes already forwarded via onChunk, using the same
	// per-line accounting as total. streamCapped latches once the budget
	// is spent, after a single truncation marker is emitted.
	streamed     int
	streamCapped bool
	// max is the shared byte budget; values <= 0 resolve to
	// tools.DefaultShellMaxBytes on first append.
	max int
}

func (o *shellOutput) append(stream, line string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.max <= 0 {
		o.max = tools.DefaultShellMaxBytes
	}
	// line is already sanitized, we store it plus a newline
	n := len(line) + 1
	if o.truncated {
		o.dropped += n
		return
	}
	if o.total+n > o.max {
		// Cap reached: store as much of this line as fits, then mark truncated.
		remaining := o.max - o.total
		stored := 0
		if remaining > 1 {
			// Reserve 1 byte for newline, store prefix of line.
			prefix := line
			if len(prefix) > remaining-1 {
				prefix = prefix[:remaining-1]
			}
			if stream == "stdout" {
				o.stdout.WriteString(prefix)
				o.stdout.WriteByte('\n')
			} else {
				o.stderr.WriteString(prefix)
				o.stderr.WriteByte('\n')
			}
			stored = len(prefix) + 1
			o.total = o.max
		}
		o.dropped += n - stored
		o.truncated = true
		return
	}
	o.total += n
	if stream == "stdout" {
		o.stdout.WriteString(line)
		o.stdout.WriteByte('\n')
	} else {
		o.stderr.WriteString(line)
		o.stderr.WriteByte('\n')
	}
}

func (o *shellOutput) stdoutString() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.stdout.String()
}

func (o *shellOutput) stderrString() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.stderr.String()
}

func (o *shellOutput) isTruncated() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.truncated
}

// streamTruncateMarker is the single bounded chunk emitted once when live
// streaming spends the output budget. It makes the cut observable on the
// stream path itself (not just in the final result metadata) while adding
// only a constant number of bytes.
const streamTruncateMarker = "[stream truncated: live output budget reached; further progress suppressed, see result]"

// emitChunk forwards one sanitized line to onChunk unless the shared byte
// budget is already spent on streaming. The first line past the budget is
// replaced by a single truncation marker; everything after is dropped.
// Accounting matches append (line bytes plus newline) so the stream cap
// and the result cap trip on the same line. The mutex is never held
// across the onChunk call: chunk delivery can block on event backpressure
// and must not stall the sibling pipe reader.
func (o *shellOutput) emitChunk(stream, line string, onChunk func(tools.StreamChunk)) {
	if onChunk == nil {
		return
	}
	o.mu.Lock()
	if o.max <= 0 {
		o.max = tools.DefaultShellMaxBytes
	}
	n := len(line) + 1
	if o.streamCapped {
		o.mu.Unlock()
		return
	}
	if o.streamed+n > o.max {
		o.streamCapped = true
		o.mu.Unlock()
		onChunk(tools.StreamChunk{Stream: stream, Data: streamTruncateMarker})
		return
	}
	o.streamed += n
	o.mu.Unlock()
	onChunk(tools.StreamChunk{Stream: stream, Data: line})
}

// streamBytes returns the total bytes forwarded via onChunk so far.
func (o *shellOutput) streamBytes() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.streamed
}

// streamTruncated reports whether the stream budget was spent (marker sent).
func (o *shellOutput) streamTruncated() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.streamCapped
}

// totalBytes returns the capped total for markers and metadata.
func (o *shellOutput) totalBytes() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.total
}

// droppedBytes returns bytes discarded after the cap was reached.
func (o *shellOutput) droppedBytes() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.dropped
}

// streamPipe copies r line-by-line into both dst (the accumulated buffer
// returned in the Result) and onChunk (live streaming), sanitizing ANSI
// escape and other control sequences out of each line first so nothing
// that could move the cursor, clear the screen, or switch to the
// alternate screen buffer ever reaches the TUI's rendered content. Live
// streaming shares dst's byte budget (see emitChunk): once the budget is
// spent the stream carries one truncation marker and then goes quiet, so
// a chatty process cannot grow the event stream without bound. It
// signals done when r is exhausted (EOF or the pipe was closed because
// the process was killed).
//
// It uses bufio.Reader rather than bufio.Scanner so lines of any length
// are captured whole: a Scanner's max token size would silently drop the
// remainder of any line past the cap, losing output.
func streamPipe(r io.Reader, stream string, dst *shellOutput, onChunk func(tools.StreamChunk), done chan<- struct{}) {
	defer func() { done <- struct{}{} }()

	reader := bufio.NewReader(r)
	for {
		raw, truncated, err := readBoundedLine(reader)
		if raw != "" || truncated {
			line := sanitizeOutput(raw)
			if truncated {
				line += lineTruncateSuffix
			}
			dst.append(stream, line)
			dst.emitChunk(stream, line, onChunk)
		}
		if err != nil {
			if err != io.EOF {
				line := sanitizeOutput(fmt.Sprintf("read error: %v", err))
				dst.append(stream, line)
				dst.emitChunk(stream, line, onChunk)
			}
			return
		}
	}
}

// maxStreamLineBytes bounds one output line held in memory. The total
// budget (shellOutput.max) already caps accumulation, but ReadString
// allocates the whole line first: a newline-less 100MB write would grow
// transient memory until timeout. Lines past this cap keep their head,
// carry a marker, and have the remainder discarded (verified Phase 0:
// a 5MB single line allocated 5MB before any cap could apply).
const maxStreamLineBytes = 256 << 10

// lineTruncateSuffix marks a line cut at maxStreamLineBytes. Wording is
// distinct from the total-budget markers so tests and readers can tell
// per-line truncation from budget truncation.
const lineTruncateSuffix = "[...line truncated at 256 KiB]"

// readBoundedLine reads one \n-terminated line, returning its content
// without the terminator. Content past maxStreamLineBytes is discarded
// (truncated=true) instead of accumulated, so transient memory stays
// bounded by the cap plus one read fragment no matter how long the
// line is. A final unterminated tail reports io.EOF with its content.
func readBoundedLine(reader *bufio.Reader) (line string, truncated bool, err error) {
	var b []byte
	for {
		frag, ferr := reader.ReadSlice('\n')
		if ferr == nil {
			// Complete line: drop the terminator before accounting so
			// a line of exactly maxStreamLineBytes is kept whole.
			frag = frag[:len(frag)-1]
			if len(b)+len(frag) > maxStreamLineBytes {
				b = append(b, frag[:maxStreamLineBytes-len(b)]...)
				return string(b), true, nil
			}
			b = append(b, frag...)
			return string(b), truncated, nil
		}
		if ferr == bufio.ErrBufferFull {
			// Mark truncation only when fragment bytes are actually
			// discarded, so a line of exactly maxStreamLineBytes
			// still reports whole.
			if len(b) < maxStreamLineBytes {
				room := maxStreamLineBytes - len(b)
				if len(frag) > room {
					b = append(b, frag[:room]...)
					truncated = true
				} else {
					b = append(b, frag...)
				}
			} else {
				truncated = true
			}
			continue
		}
		// EOF or a real read error: frag holds the untimed tail, if any.
		if len(frag) > 0 && len(b) < maxStreamLineBytes {
			n := len(frag)
			if len(b)+n > maxStreamLineBytes {
				n = maxStreamLineBytes - len(b)
				truncated = true
			}
			b = append(b, frag[:n]...)
			if len(frag) > n {
				truncated = true
			}
		} else if len(frag) > 0 {
			truncated = true
		}
		return string(b), truncated, ferr
	}
}

// ansiCSI matches "Control Sequence Introducer" sequences (ESC '['
// followed by parameter/intermediate bytes and a final byte), which cover
// cursor movement, screen clearing, and SGR color codes.
var ansiCSI = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// ansiOSC matches "Operating System Command" sequences (ESC ']' ... BEL or
// ST), used for things like setting the terminal title.
var ansiOSC = regexp.MustCompile("\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")

// ansiSimple matches the remaining short two-byte escape sequences (e.g.
// ESC 'c' full reset, ESC '7'/'8' save/restore cursor) that aren't CSI or
// OSC sequences.
var ansiSimple = regexp.MustCompile("\x1b[@-Z\\\\\\]^_0-9=>]")

// sanitizeOutput strips ANSI escape sequences and other control
// characters from a line of subprocess output before it's allowed to flow
// into the runtime event system. Bubble Tea composes its own escape
// sequences into every frame it writes to the terminal; if a shell
// command's raw output (which may contain arbitrary bytes) is embedded
// into a rendered string unsanitized, the terminal can't tell the
// difference and will happily execute a stray "clear screen" or
// "switch to alternate buffer" sequence, corrupting the whole UI. This is
// the runtime-side counterpart to never writing subprocess output to
// os.Stdout/os.Stderr directly.
func sanitizeOutput(line string) string {
	line = ansiOSC.ReplaceAllString(line, "")
	line = ansiCSI.ReplaceAllString(line, "")
	line = ansiSimple.ReplaceAllString(line, "")

	var b strings.Builder
	b.Grow(len(line))
	for _, r := range line {
		switch {
		case r == '\t':
			b.WriteRune(r)
		case r < 0x20, r == 0x7f:
			// Drop remaining control characters, including bare ESC
			// (0x1b) left over from a malformed/partial sequence and \r,
			// which terminals treat as "return to column 0" and which
			// could otherwise be used to overwrite adjacent text once
			// rendered.
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
