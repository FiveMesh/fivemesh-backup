//go:build !windows

package diskfree

import (
	"fmt"
	"syscall"
)

func AvailableBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("read filesystem statistics for %q: %w", path, err)
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
