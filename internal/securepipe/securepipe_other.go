//go:build !windows

package securepipe

import (
	"context"
	"errors"
)

func ServePasswordPipe(context.Context, string, []byte) error {
	return errors.New("secure password pipes are available on Windows only")
}

func ReadPassword(context.Context, string) (string, error) {
	return "", errors.New("secure password pipes are available on Windows only")
}
