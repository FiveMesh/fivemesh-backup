package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/wizard"
	"golang.org/x/term"
)

const (
	exitSuccess      = 0
	exitFailure      = 1
	exitUsage        = 2
	exitVerification = 3
	exitBusy         = 4
	exitInterrupted  = 130
)

type codedError struct {
	code int
	err  error
}

func (e *codedError) Error() string {
	return e.err.Error()
}

func (e *codedError) Unwrap() error {
	return e.err
}

func withCode(code int, err error) error {
	if err == nil {
		return nil
	}
	return &codedError{code: code, err: err}
}

func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		if !isTerminal(stdin) || !isTerminal(stdout) {
			printUsage(stderr)
			return exitUsage
		}
		// Running the bare command (or double-clicking it on Windows) starts
		// guided setup the first time and shows backup status afterwards.
		args = []string{"setup"}
		if paths, err := defaultPaths(); err == nil {
			if _, statErr := os.Stat(paths.Config); statErr == nil {
				code := Run(ctx, []string{"status"}, stdin, stdout, stderr)
				fmt.Fprintln(stdout)
				fmt.Fprintln(stdout, `Run "fivemesh-backup help" for all commands, or "fivemesh-backup configure" to change settings.`)
				return code
			}
		}
	}

	command := args[0]
	commandArgs := args[1:]
	var err error
	switch command {
	case "help", "-h", "--help":
		printUsage(stdout)
		return exitSuccess
	case "version", "--version":
		err = runVersion(stdout)
	case "init":
		if len(commandArgs) == 0 {
			err = runConfigure(ctx, commandArgs, stdin, stdout, stderr, false)
		} else {
			err = runInit(commandArgs, stdin, stdout, stderr)
		}
	case "configure":
		err = runConfigure(ctx, commandArgs, stdin, stdout, stderr, true)
	case "setup":
		err = runSetup(ctx, commandArgs, stdin, stdout, stderr)
	case "schedule":
		err = runSchedule(ctx, commandArgs, stdin, stdout, stderr)
	case "notify":
		err = runNotify(ctx, commandArgs, stdin, stdout, stderr)
	case "uninstall":
		err = runUninstall(ctx, commandArgs, stdin, stdout, stderr)
	case "password-broker":
		err = runPasswordBroker(ctx, commandArgs, stdin, stderr)
	case "elevated-register":
		err = runElevatedRegister(ctx, commandArgs, stderr)
	case "backup":
		err = runBackup(ctx, commandArgs, stdin, stdout, stderr)
	case "check":
		err = runCheck(ctx, commandArgs, stdout, stderr)
	case "status":
		err = runStatus(commandArgs, stdout, stderr)
	case "list":
		err = runList(commandArgs, stdout, stderr)
	case "verify":
		err = runVerify(commandArgs, stdout, stderr)
	case "logs":
		err = runLogs(commandArgs, stdout, stderr)
	case "prune":
		err = runPrune(commandArgs, stdout, stderr)
	case "restore":
		err = runRestore(ctx, commandArgs, stdin, stdout, stderr)
	default:
		err = withCode(exitUsage, fmt.Errorf("unknown command %q", command))
	}
	if err == nil {
		return exitSuccess
	}
	if errors.Is(err, flag.ErrHelp) {
		return exitSuccess
	}

	code := exitFailure
	var coded *codedError
	if errors.As(err, &coded) {
		code = coded.code
	}
	if errors.Is(err, context.Canceled) {
		code = exitInterrupted
	}
	if errors.Is(err, wizard.ErrCancelled) {
		code = exitInterrupted
	}
	fmt.Fprintf(stderr, "error: %v\n", err)
	return code
}

func isTerminal(stream any) bool {
	file, ok := stream.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func runVersion(stdout io.Writer) error {
	fmt.Fprintf(stdout, "fivemesh-backup %s\n", versionString())
	return nil
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags
}

type pathFlags struct {
	config      *string
	credentials *string
	stateDir    *string
}

func addPathFlags(flags *flag.FlagSet, defaults config.Paths) pathFlags {
	return pathFlags{
		config:      flags.String("config", defaults.Config, "path to config.toml"),
		credentials: flags.String("credentials", defaults.Credentials, "path to credentials.toml"),
		stateDir:    flags.String("state-dir", defaults.StateDir, "state directory for locks, history, and logs"),
	}
}

func (p pathFlags) configPath() string {
	return *p.config
}

func loadConfig(p pathFlags) (config.Config, error) {
	cfg, err := config.Load(p.configPath())
	if err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func loadSelectedProfile(p pathFlags, profileName string) (config.Config, string, config.Profile, string, error) {
	cfg, err := loadConfig(p)
	if err != nil {
		return config.Config{}, "", config.Profile{}, "", err
	}
	name, profile, err := cfg.ResolveProfile(profileName)
	if err != nil {
		return config.Config{}, "", config.Profile{}, "", err
	}
	password, err := config.LoadCredential(*p.credentials, name)
	if err != nil {
		return config.Config{}, "", config.Profile{}, "", err
	}
	return cfg, name, profile, password, nil
}

func defaultPaths() (config.Paths, error) {
	paths, err := config.DefaultPaths()
	if err != nil {
		return config.Paths{}, err
	}
	return paths, nil
}

func parseFlags(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return flag.ErrHelp
		}
		return withCode(exitUsage, err)
	}
	return nil
}

func printUsage(writer io.Writer) {
	fmt.Fprint(writer, `FiveMesh Backup - local MariaDB and MySQL backups

Usage:
  fivemesh-backup [command] [options]

Run without a command to start guided setup (or see status once configured).

Get started:
  setup      Guided setup: connect, choose storage and schedule, test backup
  configure  Change an existing profile with the same guided setup
  init       Create a profile non-interactively (for scripts and provisioning)

Back up and restore:
  backup     Run a backup now and verify the archive
  restore    Restore an archive into a database you name explicitly
  list       List archives for a profile
  verify     Check archive checksums and compression integrity
  prune      Preview or apply the retention policy

Monitor:
  status     Latest result for every profile
  logs       Read the sanitized output of a past run
  check      Test the database connection

Automatic backups:
  schedule   status | configure | enable | update | disable | remove | next | run | logs
             (Task Scheduler on Windows, systemd user timers on Linux)

Notifications:
  notify     discord | test | status | enable | disable | remove
             (post backup results and changes to a Discord channel)

Other:
  uninstall  Remove schedules and optionally the executable; keeps your data
  version    Print build information

Use "fivemesh-backup <command> -h" for command-specific options.
`)
}
