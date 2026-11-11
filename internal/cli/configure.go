package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/schedule"
	"github.com/FiveMesh/fivemesh-backup/internal/wizard"
)

func runConfigure(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, allowExisting bool) error {
	defaults, err := defaultPaths()
	if err != nil {
		return err
	}
	flags := newFlagSet("configure", stderr)
	paths := addPathFlags(flags, defaults)
	windowsPasswordPipe := flags.String("windows-password-pipe", "", "one-shot secure pipe for an unattended Windows task password")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return withCode(exitUsage, errors.New("configure does not accept positional arguments"))
	}
	resolved := pathValues(paths)
	var scheduler schedule.Lifecycle
	nativeScheduler, err := buildScheduler(resolved)
	if err != nil {
		return err
	}
	scheduler = nativeScheduler
	prompt := wizard.NewPrompter(stdin, stdout)
	var windowsPasswordInput func(context.Context) (string, error)
	if *windowsPasswordPipe != "" {
		pipeName := *windowsPasswordPipe
		windowsPasswordInput = func(runContext context.Context) (string, error) {
			return readWindowsPasswordPipe(runContext, pipeName)
		}
	}
	result, err := wizard.Run(ctx, wizard.Options{
		Paths:                resolved,
		StateDir:             resolved.StateDir,
		Prompter:             prompt,
		AllowExisting:        allowExisting,
		Scheduler:            scheduler,
		WindowsPasswordInput: windowsPasswordInput,
		Notifications:        true,
	})
	if err != nil {
		return err
	}
	if result.ConfigCreated && !prompt.Rich() {
		fmt.Fprintf(stdout, "Profile %q is ready.\n", result.ProfileName)
	}
	return nil
}

func runSetup(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return runConfigure(ctx, args, stdin, stdout, stderr, true)
}

func pathValues(paths pathFlags) config.Paths {
	return config.Paths{
		Config:      *paths.config,
		Credentials: *paths.credentials,
		StateDir:    *paths.stateDir,
	}
}
