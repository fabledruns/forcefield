package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Filesystem access hardening (Phase 2).
//
// All filesystem/search/security tools compose these primitives so there
// is exactly one hardened path from workspace resolution to an open file
// descriptor:
//
//	resolve (Resolve/EnsureWithinWorkspace) -> open no-follow ->
//	fstat the open descriptor -> regular/link/size checks ->
//	ctx-aware bounded read/write -> fchmod (writes)
//
// Nothing after open re-resolves by path: fstat/fchmod operate on the
// open descriptor, closing the final-path and chmod races. Windows has
// no O_NOFOLLOW equivalent (see fsaccess_windows.go); there the Lstat
// pre-check plus pre-resolution is mitigation, not equivalence, and is
// stated honestly.

// ErrNotRegular means the open descriptor is not a regular file
// (directory, FIFO/named pipe, socket, device, or other irregular file).
// Reads and writes refuse these instead of blocking on a FIFO or
// dumping a device.
var ErrNotRegular = errors.New("not a regular file")

// ErrMultiLink means the target has more than one hard link. Writes
// refuse these on platforms that can count links (Unix): a hard link
// inside the workspace to an outside file would otherwise let a write
// corrupt data outside the boundary. Reads allow multi-link files;
// Windows cannot count links and documents the gap instead.
var ErrMultiLink = errors.New("file has multiple hard links")

// CheckCtx reports ctx cancellation. Call it before every blocking
// filesystem step so a cancelled scheduler timeout never silently
// proceeds into an open/read/write.
func CheckCtx(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// AssertRegular rejects directories and non-regular files based on the
// open descriptor's FileInfo (never a path re-stat).
func AssertRegular(info os.FileInfo) error {
	if info == nil {
		return fmt.Errorf("%w: nil file info", ErrNotRegular)
	}
	if info.IsDir() {
		return fmt.Errorf("%w: is a directory", ErrNotRegular)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: refusing special file (mode %s)", ErrNotRegular, info.Mode().Type())
	}
	return nil
}

// AssertWriteLinkCount enforces the hard-link write policy: refuse
// writes to multi-link files where the platform can count links.
// Platforms without link counting return nil and must document the gap.
func AssertWriteLinkCount(info os.FileInfo) error {
	n, ok := linkCount(info)
	if !ok {
		return nil
	}
	if n > 1 {
		return fmt.Errorf("%w: refusing write to file with %d links", ErrMultiLink, n)
	}
	return nil
}

// AssertReadSize refuses over-limit reads before allocating, using the
// descriptor's size. Callers still cap the actual read with ReadCapped
// because sizes can change between fstat and read.
func AssertReadSize(info os.FileInfo, maxBytes int64) error {
	if maxBytes <= 0 {
		return nil
	}
	if info.Size() > maxBytes {
		return fmt.Errorf("file is %d bytes, which exceeds the %d byte limit", info.Size(), maxBytes)
	}
	return nil
}

// readChunkSize bounds each Read syscall inside ReadCapped so ctx
// cancellation is observed promptly on large files.
const readChunkSize = 32 << 10

// ReadCapped reads up to limit+1 bytes (the extra byte detects
// overflow), checking ctx between chunks. It never grows the buffer
// past limit+1 even if the file grows mid-read.
func ReadCapped(ctx context.Context, f *os.File, limit int64) ([]byte, error) {
	if err := CheckCtx(ctx); err != nil {
		return nil, err
	}
	if limit < 0 {
		limit = 0
	}
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, readChunkSize)
	remaining := limit + 1
	for remaining > 0 {
		if err := CheckCtx(ctx); err != nil {
			return nil, err
		}
		n := len(tmp)
		if int64(n) > remaining {
			n = int(remaining)
		}
		m, err := f.Read(tmp[:n])
		if m > 0 {
			buf = append(buf, tmp[:m]...)
			remaining -= int64(m)
		}
		if err == io.EOF {
			return buf, nil
		}
		if err != nil {
			return nil, err
		}
		if m == 0 {
			// No progress without EOF: avoid a hot spin; re-check ctx.
			if err := CheckCtx(ctx); err != nil {
				return nil, err
			}
		}
	}
	return buf, nil
}

// WriteCapped writes all of data in bounded chunks, checking ctx
// between chunks so cancellation stops large writes promptly.
func WriteCapped(ctx context.Context, f *os.File, data []byte) error {
	if err := CheckCtx(ctx); err != nil {
		return err
	}
	for len(data) > 0 {
		if err := CheckCtx(ctx); err != nil {
			return err
		}
		n := len(data)
		if n > readChunkSize {
			n = readChunkSize
		}
		m, err := f.Write(data[:n])
		if m > 0 {
			data = data[m:]
		}
		if err != nil {
			return err
		}
		if m == 0 {
			if err := CheckCtx(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// EnsureParentDirs creates missing parents of an already-resolved file
// path and re-validates the result, so a concurrent writer cannot swap
// a parent for a symlink between validation and creation without
// detection. The directory creation itself is the residual TOCTOU
// window (documented); the re-validation turns silent escape into an
// error before anything is opened or written.
func EnsureParentDirs(workspace, resolved string) error {
	dir := filepath.Dir(resolved)
	if dir == "." || dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if _, err := EnsureWithinWorkspace(workspace, resolved); err != nil {
		return err
	}
	if realDir, err := EvalLinks(dir); err == nil {
		if _, err := EnsureWithinWorkspace(workspace, realDir); err != nil {
			return err
		}
	}
	return nil
}
