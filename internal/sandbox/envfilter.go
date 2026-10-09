// Credential-variable stripping for native shell children.
//
// Forcefield reads provider API keys from the process environment when
// they are env-sourced (see config.ResolveEnvValue). Those values then
// sit in os.Environ and would reach every native shell/job child
// through hostEnv, where printenv or exfiltration could read them.
// Output redaction cannot help a live child, so the names Forcefield
// itself treats as credentials are removed from the inherited
// environment before the child spawns.
//
// Explicit per-command ExtraEnv is appended AFTER stripping, so a value
// the caller deliberately set for that command still wins. This is
// hygiene against accidental and prompt-injection-driven leakage, NOT
// an OS-level boundary: in native mode a determined same-user child can
// read the parent's environment through the OS (Linux
// /proc/<ppid>/environ). Enforcement.Describe and doctor report it as
// such via LimEnvCredsStripped.
package sandbox

import (
	"strings"
)

// stripCredentialEnv returns env minus entries whose variable name is
// listed in names. Comparison is exact on Unix and case-insensitive on
// Windows, matching each platform's environment semantics (see
// runtimeCaseInsensitive). Entries without '=' are treated as bare
// names. The input slice is never mutated; extra entries are the
// caller's to append afterwards so explicit values win.
func stripCredentialEnv(env []string, names []string) []string {
	if len(names) == 0 {
		return env
	}
	fold := runtimeCaseInsensitive()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if credentialNameMatch(name, names, fold) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func credentialNameMatch(name string, names []string, fold bool) bool {
	for _, n := range names {
		if n == "" {
			continue
		}
		if fold {
			if strings.EqualFold(name, n) {
				return true
			}
			continue
		}
		if name == n {
			return true
		}
	}
	return false
}
