//go:build linux

package providers

import (
	"errors"
	"strings"
	"testing"
)

func TestIsWSL2Host_LinuxEnvVarTriggersWSL2Hint(t *testing.T) {
	t.Setenv("WSL_DISTRO_NAME", "Ubuntu-26.04")
	if !IsWSL2Host() {
		t.Fatal("IsWSL2Host() = false on Linux with WSL_DISTRO_NAME set, want true")
	}
	hint := WSL2LoopbackConnectionHint("http://127.0.0.1:11557", errors.New("dial tcp 127.0.0.1:11557: connect: connection refused"))
	if !strings.Contains(hint, "networkingMode=mirrored") {
		t.Fatalf("expected WSL2 mirrored hint on Linux with WSL_DISTRO_NAME set, got %q", hint)
	}
}
