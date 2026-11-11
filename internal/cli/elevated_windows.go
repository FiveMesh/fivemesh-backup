//go:build windows

package cli

import (
	"context"
	"errors"
	"io"

	"github.com/FiveMesh/fivemesh-backup/internal/taskscheduler"
)

// runElevatedRegister is started by Windows after UAC approval. It is not a
// user-facing command and performs exactly one validated task registration.
func runElevatedRegister(ctx context.Context, args []string, stderr io.Writer) error {
	flags := newFlagSet(taskscheduler.ElevatedRegisterCommand, stderr)
	request := flags.String("request", "", "approval request path")
	result := flags.String("result", "", "approval result path")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if *request == "" || *result == "" {
		return withCode(exitUsage, errors.New("this command is used internally by background scheduling"))
	}
	return taskscheduler.ServeElevatedRegistration(ctx, *request, *result)
}
