//go:build !windows

package terminal

func findLogoPath() string {
	return ""
}

// EnsureWindowsTerminalFragment is a no-op on non-Windows platforms.
func EnsureWindowsTerminalFragment(logoPath string) error {
	return nil
}
