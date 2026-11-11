//go:build !windows

package config

import "errors"

func windowsShortPath(p string) (string, error) {
	return "", errors.New("not windows")
}
