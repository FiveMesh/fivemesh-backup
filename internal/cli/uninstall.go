package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/notify"
)

func runUninstall(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	defaults, err := defaultPaths()
	if err != nil {
		return err
	}
	flags := newFlagSet("uninstall", stderr)
	paths := addPathFlags(flags, defaults)
	confirmed := flags.Bool("yes", false, "confirm removal without interactive input")
	removeBinary := flags.Bool("remove-binary", false, "also remove the running executable")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return withCode(exitUsage, errors.New("uninstall does not accept positional arguments"))
	}
	resolved := pathValues(paths)
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("identify installed executable: %w", err)
	}
	executable, _ = filepath.Abs(executable)

	cfg := config.Config{Profiles: map[string]config.Profile{}}
	if _, statErr := os.Stat(resolved.Config); statErr == nil {
		cfg, err = config.Load(resolved.Config)
		if err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect configuration: %w", statErr)
	}
	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Fprintln(stdout, "FiveMesh Backup uninstall")
	fmt.Fprintf(stdout, "Executable: %s\n", executable)
	fmt.Fprintf(stdout, "Configuration: %s\n", resolved.Config)
	fmt.Fprintf(stdout, "Credentials: %s\n", resolved.Credentials)
	fmt.Fprintf(stdout, "History and logs: %s\n", resolved.StateDir)
	for _, name := range names {
		profile := cfg.Profiles[name]
		fmt.Fprintf(stdout, "Profile %s backups: %s\n", name, profile.Backup.Directory)
		if profile.Schedule.IsAutomatic() {
			fmt.Fprintf(stdout, "Profile %s schedule backend: %s\n", name, profile.Schedule.Mode)
		}
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "This removes native schedules. Backup archives, configuration, credentials, and history are preserved.")
	if *removeBinary {
		fmt.Fprintln(stdout, "The installed executable will also be removed.")
	} else {
		fmt.Fprintln(stdout, "The installed executable will be preserved; use --remove-binary to remove it.")
	}
	if !*confirmed {
		if err := confirmUninstall(stdin, stdout); err != nil {
			return err
		}
	}

	if len(names) > 0 {
		scheduler, schedulerErr := buildScheduler(resolved)
		if schedulerErr != nil && hasAutomaticSchedule(cfg) {
			return schedulerErr
		}
		if schedulerErr == nil {
			for _, name := range names {
				profile := cfg.Profiles[name]
				if profile.Schedule.IsAutomatic() && !scheduler.Manages(profile.Schedule.Mode) {
					return fmt.Errorf(
						"profile %q is configured for %s scheduling but the current backend is %s",
						name,
						profile.Schedule.Mode,
						scheduler.Mode(),
					)
				}
				if !profile.Schedule.IsAutomatic() {
					stale, _, inspectErr := scheduler.Inspect(ctx, name, profile)
					if inspectErr != nil {
						return fmt.Errorf("inspect residual schedule for profile %q: %w", name, inspectErr)
					}
					if !stale {
						continue
					}
				}
				if !scheduler.Manages(profile.Schedule.Mode) && profile.Schedule.IsAutomatic() {
					continue
				}
				if err := scheduler.Remove(ctx, name); err != nil {
					return fmt.Errorf("remove schedule for profile %q: %w", name, err)
				}
				fmt.Fprintf(stdout, "Removed schedule for %s.\n", name)
			}
		}
	}
	if *removeBinary {
		if err := removeInstalledBinary(executable); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Removed executable %s.\n", executable)
	}
	sendNotification(paths, notify.ConfigEvent("", "FiveMesh Backup uninstalled",
		"Automatic backups on this server were removed. Existing backups, configuration, and credentials were kept."), stderr)
	fmt.Fprintln(stdout, "Uninstall complete. User data was preserved.")
	return nil
}

func hasAutomaticSchedule(cfg config.Config) bool {
	for _, profile := range cfg.Profiles {
		if profile.Schedule.IsAutomatic() {
			return true
		}
	}
	return false
}

func confirmUninstall(stdin io.Reader, output io.Writer) error {
	fmt.Fprint(output, "Type UNINSTALL to continue: ")
	line, err := readLine(bufio.NewReader(stdin))
	if err != nil {
		return fmt.Errorf("read uninstall confirmation: %w", err)
	}
	if strings.TrimSpace(line) != "UNINSTALL" {
		return withCode(exitUsage, errors.New("uninstall cancelled"))
	}
	return nil
}

func removeInstalledBinary(path string) error {
	if runtime.GOOS == "windows" {
		return errors.New("cannot remove a running executable on Windows; close the program and remove it manually")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect executable: %w", err)
	}
	if !info.Mode().IsRegular() || !strings.HasPrefix(filepath.Base(path), "fivemesh-backup") {
		return fmt.Errorf("refusing to remove unexpected executable path %q", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove executable: %w", err)
	}
	return nil
}
