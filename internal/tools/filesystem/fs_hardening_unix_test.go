//go:build !windows

package filesystem

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"forcefield/internal/sandbox"
)

func mkfifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
}

// FIFOs must be refused without blocking: the no-follow open uses
// O_NONBLOCK and the descriptor check rejects before any read, so even
// a cancelled-timeout walk cannot wedge on open.
func TestReadFile_FIFORefusedWithoutBlocking(t *testing.T) {
	ws := t.TempDir()
	mkfifo(t, filepath.Join(ws, "pipe"))
	rf := NewReadFileWithPolicy(testPolicy(ws))
	done := make(chan struct{})
	go func() {
		defer close(done)
		r, err := rf.Execute(context.Background(), map[string]any{"path": "pipe"})
		if err != nil {
			t.Errorf("Execute error = %v", err)
			return
		}
		if !r.IsError || !strings.Contains(r.Content, "not a regular file") {
			t.Errorf("FIFO read = %+v, want not-a-regular-file refusal", r)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("FIFO read blocked past 10s; open must not wait for a writer")
	}
}

// Writes to FIFOs are refused before any byte is written.
func TestWriteFile_FIFORefused(t *testing.T) {
	ws := t.TempDir()
	mkfifo(t, filepath.Join(ws, "pipe"))
	wf := NewWriteFileWithPolicy(testPolicy(ws))
	done := make(chan struct{})
	go func() {
		defer close(done)
		r, err := wf.Execute(context.Background(), map[string]any{"path": "pipe", "content": "x"})
		if err != nil {
			t.Errorf("Execute error = %v", err)
			return
		}
		if !r.IsError {
			t.Errorf("FIFO write must be denied, got %+v", r)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("FIFO write blocked past 10s")
	}
}

// Devices and sockets are not readable content.
func TestReadFile_SpecialFilesRefused(t *testing.T) {
	ws := t.TempDir()
	rf := NewReadFileWithPolicy(testPolicy(ws))

	if res, err := rf.Execute(context.Background(), map[string]any{"path": "/dev/null"}); err == nil {
		// /dev/null is outside the workspace, so it is refused at the
		// boundary; either way it must never succeed.
		if !res.IsError {
			t.Errorf("/dev/null read must not succeed: %+v", res)
		}
	}

	sock := filepath.Join(ws, "s.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	defer l.Close()
	if res, err := rf.Execute(context.Background(), map[string]any{"path": "s.sock"}); err != nil {
		t.Fatal(err)
	} else if !res.IsError {
		t.Errorf("socket read must be denied, got %+v", res)
	}
}

// Hard-link policy: writes to multi-link files are refused (Unix can
// count links); reads stay allowed and unchanged.
func TestWriteFile_HardLinkRefused(t *testing.T) {
	ws := t.TempDir()
	orig := filepath.Join(ws, "orig.txt")
	if err := os.WriteFile(orig, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ws, "link.txt")
	if err := os.Link(orig, link); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	wf := NewWriteFileWithPolicy(testPolicy(ws))
	res, err := wf.Execute(context.Background(), map[string]any{"path": "link.txt", "content": "v2"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "hard link") {
		t.Fatalf("multi-link write must be refused, got %+v", res)
	}
	if got, _ := os.ReadFile(orig); string(got) != "v1" {
		t.Fatal("refused write still modified the shared inode")
	}
}

// A hard link to outside content reads through the shared inode by
// platform design; the write refusal above is the enforced half.
// This test pins the documented read behavior so it cannot drift
// silently into a claimed guarantee.
func TestReadFile_HardLinkAllowedDocumented(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("s3cr3t"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ws, "h.txt")
	if err := os.Link(secret, link); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	rf := NewReadFileWithPolicy(testPolicy(ws))
	res, err := rf.Execute(context.Background(), map[string]any{"path": "h.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("hard-link read is allowed by policy, got refusal: %s", res.Content)
	}
	_ = sandbox.ErrMultiLink // policy anchor: writes refuse, reads allow
}
