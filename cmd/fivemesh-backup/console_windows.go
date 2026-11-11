//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"

	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetConsoleProcessList = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")

// ownsConsoleAlone reports whether this process is the only one attached to
// its console, which is the case when it was started from Explorer.
func ownsConsoleAlone() bool {
	processes := make([]uint32, 4)
	count, _, _ := procGetConsoleProcessList.Call(
		uintptr(unsafe.Pointer(&processes[0])),
		uintptr(len(processes)),
	)
	return count == 1
}

func waitForEnter() {
	fmt.Fprint(os.Stdout, "\nPress Enter to close this window.")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
