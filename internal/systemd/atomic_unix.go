//go:build !windows

package systemd

import "os"

func replaceAtomic(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
