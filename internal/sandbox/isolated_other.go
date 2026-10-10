//go:build !linux

package sandbox

import (
	"fmt"
)

// newIsolatedExecutor cannot exist off Linux: isolated execution is a
// Linux (Landlock) capability. The mode exists in configuration but
// cannot be constructed here, and the failure says exactly that
// instead of falling back to native execution.
func newIsolatedExecutor(p Policy) (Executor, error) {
	return nil, fmt.Errorf("%w: sandbox mode \"isolated\" requires Linux; set sandbox.mode to \"native\" or run Forcefield on Linux",
		ErrUnsupported)
}
