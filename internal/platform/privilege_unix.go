//go:build !windows

package platform

import "os"

func IsRoot() bool {
	return os.Geteuid() == 0
}
