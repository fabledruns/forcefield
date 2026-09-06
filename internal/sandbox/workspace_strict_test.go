package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// execCommand runs a helper process, returning combined output.
func execCommand(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// strictPolicy returns a native+strict policy rooted at dir: the exact
// shape P0.8 wires in when workspace.mode=strict.
func strictPolicy(dir string) Policy {
	return Policy{Mode: ModeNative, Workspace: dir, Strict: true}
}

func TestConfines(t *testing.T) {
	if (Policy{}).Confines() {
		t.Error("zero Policy must not confine (permissive default)")
	}
	if (Policy{Mode: ModeNative}).Confines() {
		t.Error("native must not confine without strict")
	}
	if !(Policy{Mode: ModeWSL}).Confines() {
		t.Error("wsl must always confine")
	}
	if !(Policy{Mode: ModeNative, Strict: true}).Confines() {
		t.Error("native+strict must confine")
	}
	if !(Policy{Mode: ModeWSL, Strict: true}).Confines() {
		t.Error("wsl+strict must confine")
	}
}

// TestStrictBoundaryAdversarial is the core P0.8 matrix: every escape
// shape must fail against a strict native policy, on every platform.
// Cases that only have meaning on Windows drive-letter hosts are gated
// by forcing case-insensitive semantics; acceptance cases always use
// real OS paths so they hold everywhere.
func TestStrictBoundaryAdversarial(t *testing.T) {
	ws := t.TempDir()
	// Real entries for acceptance cases.
	inside := filepath.Join(ws, "sub", "dir")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inside, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := strictPolicy(ws)

	accept := []string{"", ".", "sub", "sub/dir", "sub/dir/f.txt", "sub/../sub/dir"}
	for _, req := range accept {
		if _, err := ResolveWithinWorkspace(p.Workspace, req); err != nil {
			t.Errorf("ResolveWithinWorkspace(%q) = %v, want acceptance", req, err)
		}
		if _, err := EnsureWithinWorkspace(p.Workspace, req); err != nil {
			t.Errorf("EnsureWithinWorkspace(%q) = %v, want acceptance", req, err)
		}
	}

	// outside is a real sibling directory (portable absolute escape).
	outside := t.TempDir()
	reject := []string{
		"..",
		"../..",
		"sub/../../..",
		"sub/../..",
		outside,
		filepath.Join(outside, "f.txt"),
	}
	// Textual-prefix confusion: shares a prefix but is not inside.
	prefixSibling := ws + "-evil"
	if err := os.MkdirAll(prefixSibling, 0o755); err != nil {
		t.Fatal(err)
	}
	reject = append(reject, prefixSibling, filepath.Join(prefixSibling, "f.txt"))
	// Nested dot-mix that still climbs out.
	reject = append(reject, filepath.Join("sub", "dir", "..", "..", "..", "evil"))

	for _, req := range reject {
		if _, err := ResolveWithinWorkspace(p.Workspace, req); !errors.Is(err, ErrWorkspaceEscape) && !errors.Is(err, ErrInvalidDir) {
			t.Errorf("ResolveWithinWorkspace(%q) = %v, want escape/invalid rejection", req, err)
		}
		if _, err := EnsureWithinWorkspace(p.Workspace, req); !errors.Is(err, ErrWorkspaceEscape) && !errors.Is(err, ErrInvalidDir) {
			t.Errorf("EnsureWithinWorkspace(%q) = %v, want escape/invalid rejection", req, err)
		}
	}
}

// TestStrictWindowsPathShapes forces case-insensitive (Windows) path
// semantics and asserts drive-letter, UNC, and drive-relative shapes
// can never escape, on any host OS.
func TestStrictWindowsPathShapes(t *testing.T) {
	old := runtimeCaseInsensitive
	runtimeCaseInsensitive = func() bool { return true }
	defer func() { runtimeCaseInsensitive = old }()

	ws := t.TempDir()
	p := strictPolicy(ws)

	// Drive-relative and bare-drive forms are ambiguous: always invalid.
	for _, req := range []string{`C:foo`, `C:`, `D:bar\baz`} {
		if _, err := ResolveWithinWorkspace(p.Workspace, req); !errors.Is(err, ErrInvalidDir) {
			t.Errorf("ResolveWithinWorkspace(%q) = %v, want ErrInvalidDir", req, err)
		}
		if _, err := EnsureWithinWorkspace(p.Workspace, req); !errors.Is(err, ErrInvalidDir) {
			t.Errorf("EnsureWithinWorkspace(%q) = %v, want ErrInvalidDir", req, err)
		}
	}
	// UNC paths address other machines/volumes: always escapes.
	for _, req := range []string{`\\server\share`, `\\server\share\dir`, `//server/share`} {
		if _, err := ResolveWithinWorkspace(p.Workspace, req); !errors.Is(err, ErrWorkspaceEscape) {
			t.Errorf("ResolveWithinWorkspace(%q) = %v, want ErrWorkspaceEscape", req, err)
		}
		if _, err := EnsureWithinWorkspace(p.Workspace, req); !errors.Is(err, ErrWorkspaceEscape) {
			t.Errorf("EnsureWithinWorkspace(%q) = %v, want ErrWorkspaceEscape", req, err)
		}
	}
	// A Linux-absolute path cannot live inside a Windows workspace.
	if _, err := ResolveWithinWorkspace(p.Workspace, "/home/user/x"); !errors.Is(err, ErrWorkspaceEscape) {
		t.Errorf("linux-abs = %v, want ErrWorkspaceEscape", err)
	}
}

// TestStrictWindowsDriveAcceptance runs only on Windows, where drive
// letters are real: same-drive paths inside resolve, other drives fail.
func TestStrictWindowsDriveAcceptance(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drive-letter acceptance needs a Windows host")
	}
	ws := t.TempDir()
	p := strictPolicy(ws)
	vol := filepath.VolumeName(ws)

	inside := filepath.Join(ws, "sub")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWithinWorkspace(p.Workspace, inside); err != nil {
		t.Errorf("same-drive inside = %v, want acceptance", err)
	}
	// Case-variant spelling of the workspace itself must be accepted
	// (Windows is case-insensitive).
	if _, err := ResolveWithinWorkspace(p.Workspace, strings.ToUpper(ws)); err != nil {
		t.Errorf("case-variant root = %v, want acceptance", err)
	}
	other := vol
	if vol == `C:` {
		other = `D:`
	} else {
		other = `C:`
	}
	if _, err := ResolveWithinWorkspace(p.Workspace, other+`\outside`); !errors.Is(err, ErrWorkspaceEscape) {
		t.Errorf("other-drive = %v, want ErrWorkspaceEscape", err)
	}
}

// TestStrictSymlinkEscape pins that links inside the workspace pointing
// outside resolve as escapes (both files and directories), including
// reparse points where the platform supports creating them.
func TestStrictSymlinkEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ws, "link.txt")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	p := strictPolicy(ws)
	if _, err := ResolveWithinWorkspace(p.Workspace, "link.txt"); !errors.Is(err, ErrWorkspaceEscape) {
		t.Errorf("symlinked file = %v, want ErrWorkspaceEscape", err)
	}
	if _, err := EnsureWithinWorkspace(p.Workspace, "link.txt"); !errors.Is(err, ErrWorkspaceEscape) {
		t.Errorf("ensure symlinked file = %v, want ErrWorkspaceEscape", err)
	}

	// Directory symlink (junction/reparse on Windows where permitted).
	dirLink := filepath.Join(ws, "dirlink")
	if err := os.Symlink(outside, dirLink); err != nil {
		t.Skipf("dir symlinks unavailable: %v", err)
	}
	if _, err := ResolveWithinWorkspace(p.Workspace, filepath.Join("dirlink", "secret.txt")); !errors.Is(err, ErrWorkspaceEscape) {
		t.Errorf("through-dir-link = %v, want ErrWorkspaceEscape", err)
	}

	// Inward link (alias of the workspace itself) is not an escape.
	inner := filepath.Join(ws, "real")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(ws, "alias")
	if err := os.Symlink(inner, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ResolveWithinWorkspace(p.Workspace, "alias"); err != nil {
		t.Errorf("inward alias = %v, want acceptance", err)
	}
}

// TestStrictAncestorSymlinkEscape pins EnsureWithinWorkspace's ancestor
// walk: a nonexistent file under a symlinked directory pointing outside
// is refused (creation path).
func TestStrictAncestorSymlinkEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	dirLink := filepath.Join(ws, "dirlink")
	if err := os.Symlink(outside, dirLink); err != nil {
		t.Skipf("dir symlinks unavailable: %v", err)
	}
	p := strictPolicy(ws)
	if _, err := EnsureWithinWorkspace(p.Workspace, filepath.Join("dirlink", "newfile.txt")); !errors.Is(err, ErrWorkspaceEscape) {
		t.Errorf("create-through-link = %v, want ErrWorkspaceEscape", err)
	}
	// Genuine creation inside is allowed.
	if _, err := EnsureWithinWorkspace(p.Workspace, filepath.Join("newdir", "newfile.txt")); err != nil {
		t.Errorf("create-inside = %v, want acceptance", err)
	}
}

// TestStrictJunctionEscape uses a real NTFS junction (mklink /J, which
// unlike symlinks needs no privilege) where supported: junctions are
// reparse points, and the boundary must resolve through them exactly
// like symlinks.
func TestStrictJunctionEscape(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("NTFS junctions need a Windows host")
	}
	ws := t.TempDir()
	outside := t.TempDir()
	junction := filepath.Join(ws, "junct")
	if out, err := execCommand("cmd", "/c", "mklink", "/J", junction, outside); err != nil {
		t.Skipf("mklink /J unavailable: %v (%s)", err, out)
	}
	p := strictPolicy(ws)
	if _, err := ResolveWithinWorkspace(p.Workspace, "junct"); !errors.Is(err, ErrWorkspaceEscape) {
		t.Errorf("junction = %v, want ErrWorkspaceEscape", err)
	}
	if _, err := EnsureWithinWorkspace(p.Workspace, filepath.Join("junct", "new.txt")); !errors.Is(err, ErrWorkspaceEscape) {
		t.Errorf("create-through-junction = %v, want ErrWorkspaceEscape", err)
	}
}

// mode Prepare rejects an outside Dir before any backend work (no Bash
// needed); inside resolves.
func TestNativePrepareEnforcesStrictDir(t *testing.T) {
	ws := t.TempDir()
	inside := filepath.Join(ws, "work")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	ex, err := NewExecutor(strictPolicy(ws))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	prep, err := ex.Prepare(t.Context(), Request{Command: "true", Dir: "work"})
	if errors.Is(err, ErrBackendUnavailable) {
		t.Skipf("backend unavailable (no Bash/WSL): boundary rejections above already passed: %v", err)
	}
	if err != nil {
		t.Fatalf("inside Dir Prepare = %v, want success", err)
	}
	if prep.Dir == "" {
		t.Error("prepared Dir empty")
	}
	if _, err := ex.Prepare(t.Context(), Request{Command: "true", Dir: t.TempDir()}); !errors.Is(err, ErrWorkspaceEscape) {
		t.Errorf("outside Dir Prepare = %v, want ErrWorkspaceEscape", err)
	}
	if _, err := ex.Prepare(t.Context(), Request{Command: "true", Dir: ".."}); !errors.Is(err, ErrWorkspaceEscape) && !errors.Is(err, ErrInvalidDir) {
		t.Errorf("traversal Dir Prepare = %v, want rejection", err)
	}
}

// TestNativePreparePermissiveUnchanged pins that without strict, Prepare
// keeps historical existence-only behavior (outside dirs allowed).
func TestNativePreparePermissiveUnchanged(t *testing.T) {
	outside := t.TempDir()
	ex, err := NewExecutor(Policy{Mode: ModeNative, Network: NetworkHost})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	prep, err := ex.Prepare(t.Context(), Request{Command: "true", Dir: outside})
	if errors.Is(err, ErrBackendUnavailable) {
		t.Skipf("backend unavailable (no Bash/WSL): %v", err)
	}
	if err != nil {
		t.Fatalf("permissive outside Dir Prepare = %v, want historical acceptance", err)
	}
	if prep.Dir == "" {
		t.Error("prepared Dir empty")
	}
}
