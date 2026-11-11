//go:build !windows

package config

import "os"

func replaceAtomic(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
