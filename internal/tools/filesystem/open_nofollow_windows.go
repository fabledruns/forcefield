//go:build windows

package filesystem

import "os"

func openNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}

func writeFileNoFollow(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}
