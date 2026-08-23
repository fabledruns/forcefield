package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{
		"":       ModeNative,
		"native": ModeNative,
		"wsl":    ModeWSL,
	} {
		got, err := ParseMode(in)
		if err != nil || got != want {
			t.Errorf("ParseMode(%q) = (%v, %v), want %q", in, got, err, want)
		}
	}
	if _, err := ParseMode("docker"); err == nil {
		t.Error(`ParseMode("docker") = nil error, want rejection`)
	}
}

func TestParseNetwork(t *testing.T) {
	for in, want := range map[string]Network{
		"":         NetworkDisabled,
		"disabled": NetworkDisabled,
		"host":     NetworkHost,
	} {
		got, err := ParseNetwork(in)
		if err != nil || got != want {
			t.Errorf("ParseNetwork(%q) = (%v, %v), want %q", in, got, err, want)
		}
	}
	if _, err := ParseNetwork("filtered"); err == nil {
		t.Error(`ParseNetwork("filtered") = nil error, want rejection`)
	}
}

func TestValidDistroName(t *testing.T) {
	valid := []string{"Ubuntu", "Ubuntu-22.04", "debian", "my_distro2"}
	for _, name := range valid {
		if !ValidDistroName(name) {
			t.Errorf("ValidDistroName(%q) = false, want true", name)
		}
	}
	// Every one of these could become a flag or metacharacter on the
	// wsl.exe command line if accepted.
	invalid := []string{"", "-d", "--exec", "a b", "a;b", "a;b rm", "../x", `..\x`, "a/b", strings.Repeat("x", maxDistroNameLen+1), "a\tb", "$(x)"}
	for _, name := range invalid {
		if ValidDistroName(name) {
			t.Errorf("ValidDistroName(%q) = true, want false", name)
		}
	}
}

func TestPolicyValidate(t *testing.T) {
	if err := (Policy{}).Validate(); err != nil {
		t.Errorf("zero policy invalid: %v", err)
	}
	if err := (Policy{Mode: ModeWSL, Distro: "-evil"}).Validate(); err == nil {
		t.Error("flag-like distribution name accepted")
	}
	if err := (Policy{Mode: ModeWSL, Network: "sometimes"}).Validate(); err == nil {
		t.Error("invalid network policy accepted")
	}
	if err := (Policy{Mode: "container"}).Validate(); err == nil {
		t.Error("invalid mode accepted")
	}
}

func TestResolveWithinDefaultsAndRelative(t *testing.T) {
	ws := t.TempDir()
	sub := filepath.Join(ws, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// The contract returns the CANONICAL (symlink-resolved) form, which
	// differs from the raw path on macOS (/var -> /private/var) and
	// Windows (long user names -> 8.3 short names).
	wantRoot, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatalf("EvalSymlinks(workspace) error = %v", err)
	}

	got, err := resolveWithinWorkspace(ws, "")
	if err != nil {
		t.Fatalf("empty dir error = %v", err)
	}
	if !samePath(got, wantRoot) {
		t.Errorf("empty dir resolved to %q, want canonical workspace %q", got, wantRoot)
	}

	// Relative requests anchor at the workspace, not at the process cwd.
	got, err = resolveWithinWorkspace(ws, filepath.Join("..", filepath.Base(ws), "sub"))
	if err != nil {
		t.Fatalf("relative dir error = %v", err)
	}
	if !within(got, sub) && !within(sub, got) && !strings.EqualFold(got, sub) {
		t.Errorf("relative resolution = %q, want %q", got, sub)
	}
}

// TestResolveWithinMixedWorkspaceSpellings reproduces the CI-only failure
// class: on macOS the temp root lives behind /var -> /private/var and on
// Windows behind long-vs-short user names. A workspace handed in under
// one spelling must still accept targets that resolve under another.
// Skipped on Windows, where creating directory symlinks needs privileges.
func TestResolveWithinMixedWorkspaceSpellings(t *testing.T) {
	realRoot := t.TempDir()
	link := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	sub := filepath.Join(realRoot, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// Workspace spelled via the symlink; target resolves through the real
	// path (and vice versa). Both directions must stay inside scope.
	for _, tc := range []struct{ workspace, dir string }{
		{link, sub},
		{realRoot, filepath.Join(link, "sub")},
	} {
		got, err := resolveWithinWorkspace(tc.workspace, tc.dir)
		if errors.Is(err, ErrWorkspaceEscape) || errors.Is(err, ErrInvalidDir) {
			t.Errorf("workspace=%q dir=%q rejected: %v (mixed spellings must not look like escapes)", tc.workspace, tc.dir, err)
			continue
		}
		if err != nil {
			t.Errorf("workspace=%q dir=%q unexpected error: %v", tc.workspace, tc.dir, err)
			continue
		}
		if !withinAny(realRoot, link, got) {
			t.Errorf("resolved to %q, outside both spellings of the root", got)
		}
	}

	// And a genuine escape through a differently-spelled parent is still
	// caught: sibling-of-real-root reached by climbing out of the link.
	outside := t.TempDir()
	if _, err := resolveWithinWorkspace(link, filepath.Join("..", filepath.Base(outside))); !errors.Is(err, ErrWorkspaceEscape) {
		t.Errorf("climb-out via linked spelling error = %v, want ErrWorkspaceEscape", err)
	}
}

func TestResolveWithinRejectsTraversal(t *testing.T) {
	ws := t.TempDir()

	escapes := []string{
		filepath.Join("..", ".."),
		filepath.Join(ws, "..", ".."),
		filepath.Join(string(filepath.Separator), "usr"),
		ws + string(filepath.Separator) + "..",
	}
	for _, dir := range escapes {
		if _, err := resolveWithinWorkspace(ws, dir); !errors.Is(err, ErrWorkspaceEscape) {
			t.Errorf("resolveWithinWorkspace(%q) error = %v, want ErrWorkspaceEscape", dir, err)
		}
	}

	if _, err := resolveWithinWorkspace(ws, filepath.Join(ws, "no-such-dir")); !errors.Is(err, ErrInvalidDir) {
		t.Errorf("nonexistent dir error = %v, want ErrInvalidDir", err)
	}
}

func TestResolveWithinRejectsLinuxPathsOnWindows(t *testing.T) {
	if !runtimeCaseInsensitive() {
		t.Skip("windows-only semantics")
	}
	ws := t.TempDir()
	_, err := resolveWithinWorkspace(ws, "/home/user")
	if !errors.Is(err, ErrWorkspaceEscape) {
		t.Errorf("/home/user error = %v, want ErrWorkspaceEscape (Linux paths never live in a Windows workspace)", err)
	}
	_, err = resolveWithinWorkspace(ws, "C:relative")
	if !errors.Is(err, ErrInvalidDir) {
		t.Errorf("drive-relative error = %v, want ErrInvalidDir", err)
	}
}

func TestResolveWithinCatchesSymlinkEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()

	link := filepath.Join(ws, "link")
	if err := os.Symlink(outside, link); err != nil {
		// Creating symlinks needs developer mode or admin on Windows.
		t.Skipf("cannot create symlink: %v", err)
	}

	if _, err := resolveWithinWorkspace(ws, link); !errors.Is(err, ErrWorkspaceEscape) {
		t.Errorf("symlink-to-outside error = %v, want ErrWorkspaceEscape", err)
	}

	// A link pointing inside stays fine.
	inside := filepath.Join(ws, "inside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	linkIn := filepath.Join(ws, "in-link")
	if err := os.Symlink(inside, linkIn); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	if _, err := resolveWithinWorkspace(ws, linkIn); err != nil {
		t.Errorf("symlink-inside error = %v, want nil", err)
	}
}

func TestSummaryLinesHonestNative(t *testing.T) {
	lines := strings.Join(Enforcement{Mode: ModeNative}.SummaryLines(), "\n")
	if !strings.Contains(lines, "Isolation") || !strings.Contains(lines, "none") {
		t.Errorf("native isolation line must say none:\n%s", lines)
	}
	if !strings.Contains(lines, "full host environment") {
		t.Errorf("native must admit full env forwarding:\n%s", lines)
	}
	if strings.Contains(strings.ToLower(lines), "sandboxed boundary") {
		t.Errorf("native must not be described as a sandbox:\n%s", lines)
	}
}

func TestSummaryLinesHonestWSL(t *testing.T) {
	enforced := Enforcement{Mode: ModeWSL, Network: NetworkDisabled, CwdPinned: true, NetworkEnforced: true}
	lines := strings.Join(enforced.SummaryLines(), "\n")
	for _, want := range []string{"WSL", "enforced", "NOT blocked"} {
		if !strings.Contains(lines, want) {
			t.Errorf("WSL enforcement lines missing %q:\n%s", want, lines)
		}
	}

	unenforced := Enforcement{Mode: ModeWSL, Network: NetworkDisabled, CwdPinned: true, NetworkEnforced: false,
		Notes: []string{"network isolation cannot be established here"}}
	lines = strings.Join(unenforced.SummaryLines(), "\n")
	if !strings.Contains(lines, "NOT enforced") {
		t.Errorf("unforced network must be labeled honestly:\n%s", lines)
	}
	if !strings.Contains(lines, "network isolation cannot be established") {
		t.Errorf("limitation note missing:\n%s", lines)
	}
}
