//go:build windows

package sandbox

import "context"

// probeNativeHost verifies the Bash relay (wsl.exe plus a runnable
// distribution) is healthy, matching what native execution needs.
func probeNativeHost(ctx context.Context) error {
	exe, err := wslExePath()
	if err != nil {
		return wslMissingError()
	}
	return probeWSLDistro(ctx, exe, resolveDistro(""))
}
