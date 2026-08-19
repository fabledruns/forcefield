//go:build windows

package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

// The Bash execution layer for Windows. The host process never executes
// /bin/bash (or any other shell) directly: every command is driven through
// wsl.exe, which relays it into the WSL distribution. The invocation is
// assembled as an argv, never as a command string, so the agent's Bash
// command and environment values cannot be re-parsed or injected on the way
// through:
//
//	wsl.exe [--distribution <name>] --cd <dir> --exec
//	        [/usr/bin/env K=V ...] /bin/bash -lc <command>
//
// --exec runs the following argv inside the distribution without a shell;
// each K=V pair and the whole command are single argv elements, so spaces,
// quotes, newlines, pipes, and heredocs reach Bash verbatim.

// wslPreflightTimeout bounds both the one-time backend probe and the
// working-directory check inside the distribution. A cold distribution can
// take a few seconds to boot, so this is deliberately generous.
const wslPreflightTimeout = 30 * time.Second

// wslArgBudget is the payload size above which the command is spilled to a
// staged script file instead of being passed on wsl.exe's command line.
// Windows CreateProcess command lines max out at 32767 UTF-16 units; stay
// well under.
const wslArgBudget = 30000

// wslExePath resolves the wsl.exe launcher. It is a seam over the real
// resolution so tests can simulate WSL being absent.
var wslExePath = defaultWSLExePath

func defaultWSLExePath() (string, error) {
	// Prefer the well-known System32 location so a tampered PATH cannot
	// substitute a different executable.
	if root := os.Getenv("SystemRoot"); root != "" {
		p := filepath.Join(root, "System32", "wsl.exe")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return exec.LookPath("wsl.exe")
}

func wslMissingError() error {
	return errors.New("Bash on Windows runs through WSL, but wsl.exe was not found. " +
		"Install WSL and a Linux distribution (e.g. `wsl --install -d Ubuntu`) and try again")
}

// distroArgs returns the arguments selecting the WSL distribution. By
// default WSL's configured default distribution is used; FORCEFIELD_WSL_DISTRO
// overrides that for machines whose default is broken or is a utility
// distribution (e.g. docker-desktop).
func distroArgs() []string {
	if d := strings.TrimSpace(os.Getenv("FORCEFIELD_WSL_DISTRO")); d != "" {
		return []string{"--distribution", d}
	}
	return nil
}

// ensureBackend probes WSL once per Shell and caches the outcome, so a
// machine without a usable WSL installation produces one clear, structured
// error per command instead of a confusing per-command failure. A probe
// aborted by caller cancellation is reported but not cached.
func (s *Shell) ensureBackend(ctx context.Context) error {
	s.backendMu.Lock()
	defer s.backendMu.Unlock()
	if s.backendProbed {
		return s.backendErr
	}
	err := probeWSL(ctx)
	if ctx.Err() == nil {
		s.backendProbed, s.backendErr = true, err
	}
	return err
}

// probeWSL verifies that wsl.exe can actually run /bin/bash in the selected
// distribution: WSL may be installed yet have no distribution, or a
// distribution whose disk fails to mount. Probing with `bash -c true` also
// confirms Bash itself exists inside the distribution.
func probeWSL(ctx context.Context) error {
	exe, err := wslExePath()
	if err != nil {
		return wslMissingError()
	}
	pctx, cancel := context.WithTimeout(ctx, wslPreflightTimeout)
	defer cancel()
	probe := exec.CommandContext(pctx, exe, append(distroArgs(), "--exec", "/bin/bash", "-c", "true")...)
	out, err := probe.CombinedOutput()
	if pctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("Bash on Windows runs through WSL, but WSL did not respond within %s; try `wsl --shutdown` and retry", wslPreflightTimeout)
	}
	if err == nil {
		return nil
	}
	exitCode := -1
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		exitCode = ee.ExitCode()
	}
	detail := strings.TrimSpace(decodeWSLText(out))
	msg := fmt.Sprintf("Bash on Windows runs through WSL, but WSL failed to run /bin/bash in the distribution (exit code %d)", exitCode)
	if detail != "" {
		msg += ": " + detail
	}
	if _, hint := backendFailure(string(out), ""); hint != "" {
		msg += "\n" + hint
	}
	return errors.New(msg)
}

// buildCommand runs command through GNU Bash inside the WSL distribution by
// invoking wsl.exe (see the comment at the top of this file for the exact
// invocation). It never executes /bin/bash from Windows and never falls back
// to PowerShell or cmd. The returned cleanup, when non-nil, removes a staged
// script file and must be called once the command has run.
func buildCommand(ctx context.Context, command, cwd string, extraEnv []string) (*exec.Cmd, func(), error) {
	exe, err := wslExePath()
	if err != nil {
		return nil, nil, wslMissingError()
	}

	args := append([]string{}, distroArgs()...)
	if cd := wslCdPath(cwd); cd != "" {
		args = append(args, "--cd", cd)
	}
	args = append(args, "--exec")

	var cleanup func()
	if commandBudget(command, extraEnv) <= wslArgBudget {
		if len(extraEnv) > 0 {
			args = append(args, "/usr/bin/env")
			args = append(args, extraEnv...)
		}
		args = append(args, "/bin/bash", "-lc", command)
	} else {
		script, remove, err := writeWslScript(extraEnv, command)
		if err != nil {
			return nil, nil, fmt.Errorf("shell: staging large command for WSL: %w", err)
		}
		cleanup = remove
		args = append(args, "/bin/bash", "-l", script)
	}

	cmd := exec.CommandContext(ctx, exe, args...)
	// wsl.exe needs its normal Windows environment to run; the Linux-side
	// environment comes through the argv above, since WSL does not forward
	// arbitrary Windows variables into the distribution. The host-side
	// working directory is left alone (--cd handles chdir inside WSL).
	cmd.Env = os.Environ()
	return cmd, cleanup, nil
}

// commandBudget estimates the payload wsl.exe would carry on its command
// line. Byte length is a safe over-estimate for non-ASCII text, which
// shrinks when encoded as UTF-16.
func commandBudget(command string, extraEnv []string) int {
	n := len(command)
	for _, kv := range extraEnv {
		n += len(kv) + 2
	}
	return n
}

// writeWslScript stages the environment exports and the command in a
// temporary Bash script for payloads that exceed wsl.exe's command-line
// limit. The script lives on the Windows filesystem, so it is visible inside
// the distribution through the /mnt drvfs automount. The returned path is
// the script's WSL path; the returned func removes the file.
func writeWslScript(extraEnv []string, command string) (string, func(), error) {
	f, err := os.CreateTemp("", "forcefield-cmd-*.sh")
	if err != nil {
		return "", nil, err
	}
	remove := func() { _ = os.Remove(f.Name()) }

	var b strings.Builder
	for _, kv := range extraEnv {
		k, v, _ := strings.Cut(kv, "=")
		if !envNameRe.MatchString(k) {
			_ = f.Close()
			remove()
			return "", nil, fmt.Errorf("env key %q is not a valid shell variable name", k)
		}
		b.WriteString("export " + k + "=" + shellQuote(v) + "\n")
	}
	b.WriteString(command)
	b.WriteString("\n")

	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		remove()
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		remove()
		return "", nil, err
	}
	return wslPathFromWindows(f.Name()), remove, nil
}

// envNameRe matches the identifiers Bash accepts as variable names.
var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// shellQuote wraps s in single quotes - the one form of Bash quoting with
// no nested interpretation - so a staged value can never break out of the
// string.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// wslPathFromWindows maps an absolute Windows filesystem path onto its WSL
// drvfs mount, e.g. C:\Users\a\f.sh -> /mnt/c/Users/a/f.sh. Paths without a
// drive letter (UNC, POSIX) are returned unchanged.
func wslPathFromWindows(p string) string {
	vol := filepath.VolumeName(p)
	if len(vol) != 2 || vol[1] != ':' {
		return p
	}
	rest := strings.ReplaceAll(strings.TrimPrefix(p, vol), `\`, `/`)
	return "/mnt/" + strings.ToLower(vol[:1]) + rest
}

// wslCdPath maps a working directory onto a form `wsl.exe --cd` understands.
// --cd accepts absolute Windows paths (translated to the distribution's
// /mnt automount), absolute Linux paths, and \\wsl$ paths as-is. Relative
// paths are made absolute against the host process's working directory,
// because --cd requires an absolute path.
func wslCdPath(dir string) string {
	switch {
	case dir == "":
		return ""
	case isLinuxAbs(dir), isUNC(dir):
		return dir
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

func isLinuxAbs(p string) bool { return strings.HasPrefix(p, "/") }

func isUNC(p string) bool { return strings.HasPrefix(p, `\\`) }

// validWorkingDir reports whether dir is usable as the command's working
// directory. Windows paths are checked on the host as usual. Absolute Linux
// paths belong to the WSL filesystem, which the host cannot stat, so they
// are verified inside the distribution: wsl.exe only warns when --cd fails
// and still runs the command (in the wrong directory), so the check must
// happen before the command is allowed to run.
func validWorkingDir(dir string) bool {
	switch {
	case isLinuxAbs(dir):
		return wslDirExists(dir)
	case isUNC(dir):
		// \\wsl$ paths address the distribution's own filesystem; the
		// subsequent --cd reports if they don't exist.
		return true
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// wslDirExists reports whether dir exists inside the WSL distribution. If
// WSL itself is broken the check reports true so the failure surfaces
// through the backend probe with its proper diagnosis instead of a
// misleading "working directory does not exist".
func wslDirExists(dir string) bool {
	exe, err := wslExePath()
	if err != nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), wslPreflightTimeout)
	defer cancel()
	check := exec.CommandContext(ctx, exe, append(distroArgs(), "--exec", "/usr/bin/test", "-d", dir)...)
	out, err := check.CombinedOutput()
	if err == nil {
		return true
	}
	_, hint := backendFailure(string(out), "")
	return hint != ""
}

// wslFailureSignatures are substrings (lowercased) of wsl.exe's own error
// messages indicating that a command never (properly) reached Bash, each
// with a hint for fixing the underlying problem. "also", when set, must
// appear as well to reduce false positives on command output.
var wslFailureSignatures = []struct {
	match, also, hint string
}{
	{
		match: "no installed distributions",
		hint:  "No WSL distribution is installed. Install one, e.g. `wsl --install -d Ubuntu`.",
	},
	{
		match: "mounting the distribution disk",
		hint: "The WSL distribution's virtual disk failed to mount (see https://aka.ms/wsldiskmountrecovery). " +
			"`wsl --shutdown` then retry often helps. If the default distribution is a utility distribution " +
			"such as docker-desktop, set a real one with `wsl --set-default <name>` or set FORCEFIELD_WSL_DISTRO.",
	},
	{
		match: "no distribution with the supplied name",
		hint:  "The selected WSL distribution does not exist. Check FORCEFIELD_WSL_DISTRO or `wsl -l -v`.",
	},
	{
		match: "wslregisterdistribution",
		hint:  "The WSL distribution failed to start. Try `wsl --shutdown` and retry.",
	},
	{
		match: "chdir(",
		also:  "wsl",
		hint:  "WSL could not enter the requested working directory, so the command did not run where intended.",
	},
}

// backendFailure inspects captured output for signatures of WSL
// infrastructure failures (as opposed to the command's own errors). It
// returns the decoded diagnostic and a remediation hint when the command
// never produced output, i.e. never actually ran.
func backendFailure(stderr, stdout string) (detail, hint string) {
	if stdout != "" {
		return "", "" // the command produced output, so it did run
	}
	detail = strings.TrimSpace(decodeWSLText([]byte(stderr)))
	if detail == "" {
		return "", ""
	}
	lower := strings.ToLower(detail)
	for _, sig := range wslFailureSignatures {
		if strings.Contains(lower, sig.match) && (sig.also == "" || strings.Contains(lower, sig.also)) {
			return detail, sig.hint
		}
	}
	return "", ""
}

// decodeWSLText decodes b from UTF-16LE when it carries wsl.exe's own
// message encoding. wsl.exe writes its diagnostics ("wsl: ...") as UTF-16LE
// when its output is piped rather than attached to a console, while output
// relayed from Linux processes is plain UTF-8. Anything that doesn't look
// like UTF-16LE text is returned as-is.
func decodeWSLText(b []byte) string {
	if !looksUTF16LE(b) {
		return string(b)
	}
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, uint16(b[i])|uint16(b[i+1])<<8)
	}
	runes := utf16.Decode(units)
	printable := 0
	for _, r := range runes {
		if r == '\n' || r == '\r' || r == '\t' || (r >= 0x20 && r != 0x7f) {
			printable++
		}
	}
	if printable*4 < len(runes)*3 {
		return string(b) // decoded to mostly-unprintable data; keep raw bytes
	}
	return string(runes)
}

// looksUTF16LE reports whether b plausibly is UTF-16LE-encoded ASCII-range
// text: byte pairs whose high byte is zero (ASCII code units) dominate.
func looksUTF16LE(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	pairs, ascii := 0, 0
	for i := 0; i+1 < len(b) && pairs < 256; i += 2 {
		if b[i+1] == 0 && b[i] != 0 {
			ascii++
		}
		pairs++
	}
	return pairs >= 4 && ascii*4 > pairs*3
}
