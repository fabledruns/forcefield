package mcp

import (
	"context"
	"testing"
	"time"
)

// Close on a server that ignores stdin EOF must still return bounded:
// the stdin-EOF grace expires, escalation (Terminate then Kill) reaps
// it, and shutdown never hangs on a stubborn child. Started in the
// background like the tree tests because a non-serving helper never
// reports ready.
func TestHostCloseStubbornServerBounded(t *testing.T) {
	shrinkGrace(t)
	cfg := helperServerConfig(t, map[string]string{"MCP_HELPER_IGNORE_STDIN": "1"})
	cfg.TimeoutSeconds = 60
	h, _ := newTestHost(t, map[string]ServerConfig{"s": cfg})
	ch := make(chan error, 1)
	go func() { ch <- h.Start(context.Background()) }()
	waitForCondition(t, 10*time.Second, "server spawn", func() bool {
		return snapshotOf(h, "s").Started
	})
	start := time.Now()
	_ = h.Close()
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("Close took %s on a stdin-ignoring server", elapsed)
	}
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not abort after Close")
	}
	if ss := snapshotOf(h, "s"); !ss.Exited {
		t.Errorf("stubborn server left unreaped: %+v", ss)
	}
}
