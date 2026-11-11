//go:build windows

package diskfree

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func AvailableBytes(path string) (uint64, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("encode filesystem path: %w", err)
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(pathPointer, &available, nil, nil); err != nil {
		return 0, fmt.Errorf("read filesystem statistics for %q: %w", path, err)
	}
	return available, nil
}
