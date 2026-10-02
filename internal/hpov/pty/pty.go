// Package pty spawns interactive children on a fixed-size
// pseudo-terminal with drained output and reliable tree cleanup.
//
// Windows uses ConPTY (CreatePseudoConsole + a process-thread
// attribute) via x/sys/windows; Unix uses /dev/ptmx (openpty style)
// via x/sys/unix. No third-party pty dependency.
//
// Terminal contract (both platforms): 120x40, TERM=xterm-256color,
// COLUMNS/LINES set, window size installed before spawn, output
// drained continuously so a full buffer can never stall the child.
package pty

import (
	"io"
	"sync/atomic"
)

// Dimensions and terminal type are fixed so TUI rendering cost is
// comparable across runs and hosts.
const (
	DefaultCols = 120
	DefaultRows = 40
	Term        = "xterm-256color"
)

// Options controls one pty child.
type Options struct {
	Path string
	Args []string
	Env  []string // complete environment block
	Dir  string
	Cols int // 0 = DefaultCols
	Rows int // 0 = DefaultRows
}

func dims(o Options) (cols, rows int) {
	cols, rows = o.Cols, o.Rows
	if cols <= 0 {
		cols = DefaultCols
	}
	if rows <= 0 {
		rows = DefaultRows
	}
	return cols, rows
}

// countingReader counts drained bytes. HPOV records pty throughput
// as a diagnostic (stalls would otherwise be invisible).
type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.n.Add(int64(n))
	}
	return n, err
}
