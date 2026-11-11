//go:build !windows

package cli

import (
	"context"
	"errors"
	"io"
)

func runElevatedRegister(context.Context, []string, io.Writer) error {
	return withCode(exitUsage, errors.New("elevated registration is available on Windows only"))
}
