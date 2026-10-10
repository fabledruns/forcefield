//go:build !linux

package sandbox

import (
	"fmt"
	"os"

	"forcefield/internal/sandbox/landlock"
)

// HelperMain never runs successfully off Linux: the isolated executor
// cannot be constructed there, so reaching the helper means a bug or
// manual invocation. It reports through the same sentinel protocol
// and exit code as the Linux helper.
func HelperMain() int {
	fmt.Fprintf(os.Stderr, "%s%s:%s\n", landlock.SentinelPrefix, "SETUP", "isolated execution requires Linux")
	return landlock.ExitSetupFailed
}
