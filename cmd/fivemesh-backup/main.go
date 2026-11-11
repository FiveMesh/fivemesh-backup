package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/FiveMesh/fivemesh-backup/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	// When the executable is double-clicked in Explorer, Windows closes the
	// console as soon as the process exits. Keep it open so the result can
	// be read. Only bare launches qualify; scheduled and helper runs never
	// wait for input.
	if len(os.Args) == 1 && ownsConsoleAlone() {
		waitForEnter()
	}
	os.Exit(code)
}
