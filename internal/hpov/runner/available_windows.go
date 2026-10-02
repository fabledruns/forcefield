//go:build windows

package runner

// havePTY reports ConPTY availability: Windows 10 1809+. hpov
// targets supported Windows releases, so this is true; the pty suite
// still smoke-checks creation and reports unsupported on failure.
func havePTY() bool { return true }

// haveMemory reports OS memory-measurement availability: psapi is part
// of every supported Windows release.
func haveMemory() bool { return true }
