//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"runtime"
	"syscall"

	"forcefield/internal/sandbox/landlock"
)

// HelperMain is the isolated-execution helper entry point, dispatched
// from main.go before cobra when argv[1] is HelperArg. It runs on the
// calling OS thread: Landlock and no_new_privs attach to this thread
// and are inherited across the final exec, so the shell it spawns is
// born confined. It returns a process exit code; main.go exits with
// it. On any error it prints "FFSBX:<kind>:<detail>" to stderr and
// returns 126, reusing the spike protocol so parents (and tests)
// distinguish setup failure from command failure.
func HelperMain() int {
	runtime.LockOSThread()
	fail := func(kind string, err error) int {
		fmt.Fprintf(os.Stderr, "%s%s:%v\n", landlock.SentinelPrefix, kind, err)
		return landlock.ExitSetupFailed
	}
	pipe := os.NewFile(3, "helper-policy")
	req, err := readHelperRequest(pipe)
	_ = pipe.Close()
	if err != nil {
		return fail("POLICY", err)
	}
	if err := landlock.ApplyPolicy(landlock.Policy{
		Version:    landlock.PolicyVersion,
		NoNewPrivs: req.NoNewPrivs,
		Allowed:    req.Rules,
	}); err != nil {
		return fail("SETUP", err)
	}
	if err := os.Chdir(req.Dir); err != nil {
		return fail("SETUP", fmt.Errorf("enter working directory: %w", err))
	}
	// Replace this process with the shell: from here on the kernel
	// enforces the ruleset on everything the command (and its
	// descendants) can open.
	if err := syscall.Exec(req.Bash, []string{"bash", "-lc", req.Command}, req.Env); err != nil {
		return fail("SETUP", fmt.Errorf("exec shell: %w", err))
	}
	return 0 // unreachable
}
