//go:build linux

package sandbox

import (
	"os"
	"testing"
)

// TestMain dispatches the isolated-execution helper when the test
// binary is re-executed with the helper sentinel, mirroring the
// production main.go dispatch. This lets backend tests execute the
// real helper (same binary, real Landlock) instead of a mock.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == HelperArg {
		os.Exit(HelperMain())
	}
	os.Exit(m.Run())
}
