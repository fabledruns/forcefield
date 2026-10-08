package shell

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// isWSLMode reports whether the shell's executor is in WSL mode.
func (s *Shell) isWSLMode(ctx context.Context) bool {
	enc := s.executorFor().Describe(ctx)
	return enc.Mode == sandbox.ModeWSL
}

// isWSLForbiddenPattern reports whether command contains obvious host
// filesystem escapes that are blocked in WSL mode. This is a conservative
// lexical mitigation, not a filesystem sandbox and not a network
// boundary: Windows .exe interop executes inside the network namespace
// with host networking (verified Phase 0), so the whole .exe class is
// refused here while ordinary approval (ask) still gates everything
// else. It blocks /mnt/*, Windows drive patterns like C:\ or C:/,
// /proc and /sys, traversal via ../, and WSL interop paths.
//
// Deliberately NOT matched: bare `cmd` without .exe (a substring rule
// would false-positive on words and flags containing "cmd"; real
// interop invocations spell cmd.exe, which is matched, and the .exe
// catch-all below covers the rest).
func isWSLForbiddenPattern(command string) bool {
	if strings.Contains(command, "/mnt/") {
		return true
	}
	lower := strings.ToLower(command)
	for i := 0; i < len(lower)-2; i++ {
		c := lower[i]
		if c >= 'a' && c <= 'z' && lower[i+1] == ':' && (lower[i+2] == '/' || lower[i+2] == '\\') {
			return true
		}
	}
	if strings.Contains(command, `\\wsl`) || strings.Contains(lower, "wsl.localhost") || strings.Contains(lower, `\\wsl$`) {
		return true
	}
	// Indirect WSL/host escapes via proc, sys, or interop helpers.
	// /run/wsl is matched lowercase, which also covers the real /run/WSL
	// socket directory (verified Phase 0: the sockets live there).
	if strings.Contains(lower, "/proc/") || strings.Contains(lower, "/sys/") {
		return true
	}
	if strings.Contains(lower, "/run/wsl") || strings.Contains(lower, "/usr/lib/wsl") {
		return true
	}
	if strings.Contains(lower, "wslpath") || strings.Contains(lower, "wslinfo") || strings.Contains(lower, "powershell") || strings.Contains(lower, "cmd.exe") || strings.Contains(lower, "wsl.exe") {
		return true
	}
	// Named interop launchers observed reachable from inside the
	// namespace (verified Phase 0). Checked before the generic .exe
	// rule so refusal messages name the culprit.
	if strings.Contains(lower, "curl.exe") || strings.Contains(lower, "explorer.exe") || strings.Contains(lower, "notepad.exe") {
		return true
	}
	// Catch-all for the interop class: any .exe reference from inside
	// the distribution runs on the Windows host, outside the Linux
	// network namespace. Conservative by design (a mere filename
	// mentioning .exe is also refused); bypassable by the same
	// indirection as every rule here ($var, quoting), so this stays a
	// mitigation and never a claimed boundary.
	if strings.Contains(lower, ".exe") {
		return true
	}
	// Traversal outside workspace via ../
	if strings.Contains(command, "../") || strings.Contains(command, `..\\`) {
		return true
	}
	// Bare .. as a path component (e.g. "cat ../secret")
	if command == ".." || strings.HasPrefix(command, "../") || strings.HasSuffix(command, "/..") || strings.Contains(command, "/../") {
		return true
	}
	return false
}

func wslForbiddenSnippet(command string) string {
	if strings.Contains(command, "/mnt/") {
		return "/mnt/"
	}
	lower := strings.ToLower(command)
	for i := 0; i < len(lower)-2; i++ {
		c := lower[i]
		if c >= 'a' && c <= 'z' && lower[i+1] == ':' && (lower[i+2] == '/' || lower[i+2] == '\\') {
			return string(command[i : i+3])
		}
	}
	if strings.Contains(command, `\\wsl`) {
		return `\\wsl`
	}
	if strings.Contains(lower, "wsl.localhost") {
		return "wsl.localhost"
	}
	if strings.Contains(lower, "/proc/") {
		return "/proc/"
	}
	if strings.Contains(lower, "/sys/") {
		return "/sys/"
	}
	if strings.Contains(command, "../") {
		return "../"
	}
	if strings.Contains(lower, "wslpath") {
		return "wslpath"
	}
	if strings.Contains(lower, "wslinfo") {
		return "wslinfo"
	}
	if strings.Contains(lower, "powershell") {
		return "powershell"
	}
	if strings.Contains(lower, "curl.exe") {
		return "curl.exe"
	}
	if strings.Contains(lower, "explorer.exe") {
		return "explorer.exe"
	}
	if strings.Contains(lower, "notepad.exe") {
		return "notepad.exe"
	}
	if strings.Contains(lower, ".exe") {
		return ".exe"
	}
	return "host filesystem"
}

// interactiveCommands are programs that always need a real controlling
// terminal to be useful, regardless of what arguments they're given.
var interactiveCommands = map[string]bool{
	"vim": true, "vi": true, "nvim": true, "nano": true, "pico": true,
	"emacs": true, "joe": true,
	"top": true, "htop": true, "btop": true, "watch": true,
	"less": true, "more": true, "man": true,
	"ssh": true, "mosh": true, "telnet": true, "ftp": true, "sftp": true,
	"tmux": true, "screen": true,
	"mysql": true, "psql": true, "sqlite3": true, "redis-cli": true,
}

// bareReplCommands are programs that are only interactive when invoked
// with no arguments (they drop into a REPL); given a script or expression
// to run, they're ordinary non-interactive commands, so these are only
// refused when they're the entire segment.
var bareReplCommands = map[string]bool{
	"python": true, "python3": true, "node": true, "irb": true,
	"pry": true, "ipython": true, "gdb": true, "lldb": true,
	"julia": true, "R": true,
}

// commandSeparators splits a shell command line on the operators that
// chain multiple commands together, so "true && vim" is still caught.
var commandSeparators = regexp.MustCompile(`&&|\|\||[;|]`)

// wrapperCommands are prefix commands that run the following word as the
// real command ("sudo vim", "env python", "nohup top", "command ssh").
// They're skipped when looking for the interactive program in a segment.
var wrapperCommands = map[string]bool{
	"sudo": true, "env": true, "nohup": true, "command": true,
	"xargs": true, "nice": true, "stdbuf": true, "time": true,
}

// shellCommands are shells whose -c flag takes a command string. When the
// first real token is a shell and it is invoked with -c, the string after
// -c is shell code that must be inspected for interactive programs. This
// closes the ` + "`bash -c \"vim\"`" + ` bypass where the heuristic previously
// saw only "bash" and missed the interactive program inside.
var shellCommands = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "dash": true,
	"ksh": true, "ash": true, "fish": true,
}

// detectInteractiveCommand reports whether command invokes a program that
// requires a TTY. It's a deliberately simple heuristic - split on shell
// control operators, skip leading VAR=value assignments and wrapper
// commands, strip quotes from the candidate word, look at the first
// remaining word of each segment - not a full shell parser. That's enough
// to catch the common cases (bare "vim", "cmd1 && ssh host", "FOO=bar
// top", "sudo vim") without taking on the complexity of actually parsing
// shell syntax.
func detectInteractiveCommand(command string) (string, bool) {
	for _, segment := range commandSeparators.Split(command, -1) {
		fields := splitFields(segment)
		for i, field := range fields {
			if isAssignment(field) {
				continue
			}
			name := strings.TrimSuffix(commandBaseName(unquote(field)), ".exe")
			if wrapperCommands[name] {
				continue
			}
			if interactiveCommands[name] {
				return name, true
			}
			if bareReplCommands[name] && i == len(fields)-1 {
				return name, true
			}
			// If the first real token is a shell invoked with -c, inspect
			// the command string after -c. This closes the `bash -c "vim"`
			// and `sh -c 'ssh ...'` bypasses.
			if shellCommands[name] {
				for j := i + 1; j < len(fields); j++ {
					f := fields[j]
					if strings.HasPrefix(f, "-") && strings.Contains(f, "c") {
						if j+1 < len(fields) {
							cmdStr := unquote(fields[j+1])
							if prog, ok := detectInteractiveCommand(cmdStr); ok {
								return prog, true
							}
						}
						break
					}
				}
			}
			break // first real (non-assignment, non-wrapper) token
		}
	}
	return "", false
}

// splitFields splits a shell segment into words, treating single- and
// double-quoted spans (including spaces inside them) as one field, so a
// quoted program path like `"C:\Program Files\vim.exe"` stays a single
// word for interactive-command detection.
func splitFields(segment string) []string {
	var fields []string
	var current strings.Builder
	inSingle, inDouble := false, false
	flush := func() {
		if current.Len() > 0 {
			fields = append(fields, current.String())
			current.Reset()
		}
	}
	for _, r := range segment {
		switch {
		case r == '\'' && !inDouble:
			inSingle = !inSingle
		case r == '"' && !inSingle:
			inDouble = !inDouble
		case r == ' ' || r == '\t':
			if inSingle || inDouble {
				current.WriteRune(r)
			} else {
				flush()
			}
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return fields
}

// commandBaseName returns the final path component of word, splitting on
// both "/" and "\". Bash commands are agent-authored text, not host
// filesystem paths, so the split can't use filepath.Base: its separator is
// platform-dependent (only ":" and "/" are volume/separator characters on
// Unix builds), which would leave a Windows-style path like
// `C:\Program Files\vim.exe` un-split - and therefore invisible to
// detectInteractiveCommand - when Forcefield itself runs on Linux/macOS.
func commandBaseName(word string) string {
	if i := strings.LastIndexAny(word, `/\`); i >= 0 {
		return word[i+1:]
	}
	return word
}

// unquote strips one level of matching single or double quotes from word,
// so a quoted program name (`"C:\Program Files\vim.exe"`) is still
// recognized.
func unquote(word string) string {
	if len(word) >= 2 {
		if (word[0] == '"' && word[len(word)-1] == '"') || (word[0] == '\'' && word[len(word)-1] == '\'') {
			return word[1 : len(word)-1]
		}
	}
	return word
}

// isAssignment reports whether field looks like a leading shell
// environment assignment, e.g. "FOO=bar", rather than the command itself.
func isAssignment(field string) bool {
	eq := strings.IndexByte(field, '=')
	if eq <= 0 {
		return false
	}
	name := field[:eq]
	for i, r := range name {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// extraEnvArgs returns the environment variables supplied under the "env"
// argument as K=V pairs, sorted by key for deterministic ordering. Only the
// extras are returned: how they merge into the child's environment is a
// backend decision (appended to os.Environ natively; passed to `env` inside
// WSL, since WSL does not forward arbitrary Windows variables into the
// distribution). Keys must be valid shell variable names (see
// sandbox.IsValidEnvName, the same check the WSL large-payload path
// applies): a flag-like "-u", "A=B", empty, or whitespace-containing key
// would otherwise alter process setup - notably the WSL small path, which
// passes pairs to /usr/bin/env. A malformed env argument is an argument
// error rather than being silently dropped: the caller asked for variables
// the command would then run without.
func extraEnvArgs(args map[string]any) ([]string, error) {
	raw, ok := args["env"]
	if !ok {
		return nil, nil
	}
	extra, ok := raw.(map[string]any)
	if !ok {
		return nil, &tools.ArgumentError{Field: "env", Reason: "must be an object of string key/value pairs"}
	}

	pairs := make([]string, 0, len(extra))
	for k, v := range extra {
		if !sandbox.IsValidEnvName(k) {
			return nil, &tools.ArgumentError{Field: "env", Reason: fmt.Sprintf("key %q is not a valid environment variable name (must match [A-Za-z_][A-Za-z0-9_]*)", k)}
		}
		s, ok := v.(string)
		if !ok {
			return nil, &tools.ArgumentError{Field: "env", Reason: fmt.Sprintf("value for %q must be a string", k)}
		}
		pairs = append(pairs, k+"="+s)
	}
	sort.Strings(pairs)
	return pairs, nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
