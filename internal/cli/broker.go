package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/securepipe"
)

func runPasswordBroker(ctx context.Context, args []string, stdin io.Reader, stderr io.Writer) error {
	flags := newFlagSet("password-broker", stderr)
	pipeName := flags.String("pipe", "", "one-shot secure named pipe")
	timeout := flags.Duration("timeout", 30*time.Minute, "maximum time to wait for the CLI client")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return withCode(exitUsage, errors.New("password-broker does not accept positional arguments"))
	}
	if !securepipe.ValidName(*pipeName) {
		return withCode(exitUsage, errors.New("--pipe must be a valid one-shot pipe name"))
	}
	if *timeout <= 0 {
		return withCode(exitUsage, errors.New("--timeout must be greater than zero"))
	}
	data, err := io.ReadAll(io.LimitReader(stdin, securepipe.MaxSecretBytes+1))
	if err != nil {
		return fmt.Errorf("read ephemeral Windows password: %w", err)
	}
	defer clearSecretBytes(data)
	if len(data) == 0 {
		return errors.New("ephemeral Windows password was empty")
	}
	if len(data) > securepipe.MaxSecretBytes {
		return errors.New("ephemeral Windows password exceeded size limit")
	}
	brokerContext, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if err := securepipe.ServePasswordPipe(brokerContext, *pipeName, data); err != nil {
		return fmt.Errorf("serve secure Windows password pipe: %w", err)
	}
	return nil
}

func readWindowsPasswordPipe(ctx context.Context, pipeName string) (string, error) {
	if !securepipe.ValidName(pipeName) {
		return "", errors.New("invalid Windows password pipe name")
	}
	return securepipe.ReadPassword(ctx, pipeName)
}

func clearSecretBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
