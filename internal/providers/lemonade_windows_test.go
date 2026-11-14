//go:build windows

package providers

import (
	"errors"
	"testing"
)

func TestIsWSL2Host_FalseOnNativeWindowsEvenWithEnvVar(t *testing.T) {
	t.Setenv("WSL_DISTRO_NAME", "Ubuntu-26.04")
	if IsWSL2Host() {
		t.Fatal("IsWSL2Host() = true on native Windows, want false")
	}
	err := errors.New("dial tcp 127.0.0.1:11557: connectex: No connection could be made because the target machine actively refused it.")
	if hint := WSL2LoopbackConnectionHint("http://127.0.0.1:11557", err); hint != "" {
		t.Fatalf("expected empty WSL2 hint on native Windows, got %q", hint)
	}
}
