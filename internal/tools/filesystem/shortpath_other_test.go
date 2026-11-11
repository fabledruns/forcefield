//go:build !windows

package filesystem

import "errors"

func windowsShortPath(p string) (string, error) {
	return "", errors.New("not windows")
}
