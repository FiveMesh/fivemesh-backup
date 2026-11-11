//go:build !windows

package wizard

import "os"

func enableVirtualTerminal(*os.File) {}
