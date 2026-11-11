package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"golang.org/x/term"
)

func runInit(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	defaults, err := defaultPaths()
	if err != nil {
		return err
	}
	flags := newFlagSet("init", stderr)
	paths := addPathFlags(flags, defaults)
	profileName := flags.String("profile", "", "profile name (required)")
	description := flags.String("description", "", "profile description")
	host := flags.String("host", "127.0.0.1", "database host")
	socket := flags.String("socket", "", "database socket (instead of host)")
	port := flags.Int("port", 3306, "database TCP port")
	username := flags.String("user", "", "database username (required)")
	databaseName := flags.String("database", "", "database name (required)")
	backupDirectory := flags.String("backup-dir", "", "archive destination directory (required)")
	compressionLevel := flags.Int("compression-level", 3, "Zstandard level from 1 to 19")
	keepLast := flags.Int("keep-last", 7, "keep the newest N successful archives")
	keepDays := flags.Int("keep-days", 30, "keep successful archives for N days")
	maxBytes := flags.Int64("max-bytes", 0, "maximum profile archive storage in bytes")
	minimumFree := flags.Int64("minimum-free-bytes", 512*1024*1024, "required free space before backup")
	singleTransaction := flags.Bool("single-transaction", true, "request a transactional InnoDB snapshot")
	routines := flags.Bool("routines", true, "include stored procedures and functions")
	events := flags.Bool("events", true, "include scheduled events")
	dumpTool := flags.String("dump-tool", "auto", "mariadb-dump/mysqldump executable")
	clientTool := flags.String("client-tool", "auto", "mariadb/mysql executable")
	passwordStdin := flags.Bool("password-stdin", false, "read the database password from standard input")
	timeout := flags.String("timeout", "", "optional dump timeout, such as 12h")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return withCode(exitUsage, errors.New("init does not accept positional arguments"))
	}
	if strings.TrimSpace(*profileName) == "" {
		return withCode(exitUsage, errors.New("--profile is required"))
	}
	if strings.TrimSpace(*username) == "" {
		return withCode(exitUsage, errors.New("--user is required"))
	}
	if strings.TrimSpace(*databaseName) == "" {
		return withCode(exitUsage, errors.New("--database is required"))
	}
	if strings.TrimSpace(*backupDirectory) == "" {
		return withCode(exitUsage, errors.New("--backup-dir is required"))
	}
	if *socket != "" {
		*host = ""
		*port = 0
	}

	password, err := readInitialPassword(stdin, stdout, *passwordStdin)
	if err != nil {
		return withCode(exitUsage, err)
	}
	cfg := config.Config{
		Version:        config.CurrentVersion,
		DefaultProfile: *profileName,
		Profiles: map[string]config.Profile{
			*profileName: {
				Description: *description,
				Database: config.Database{
					Engine:            "mysql",
					DumpTool:          *dumpTool,
					ClientTool:        *clientTool,
					Host:              *host,
					Port:              *port,
					Socket:            *socket,
					Username:          *username,
					Database:          *databaseName,
					SingleTransaction: *singleTransaction,
					IncludeRoutines:   *routines,
					IncludeEvents:     *events,
				},
				Backup: config.Backup{
					Directory:             *backupDirectory,
					CompressionLevel:      *compressionLevel,
					Timeout:               *timeout,
					MinimumFreeSpaceBytes: *minimumFree,
				},
				Retention: config.Retention{
					KeepLast:     *keepLast,
					KeepDays:     *keepDays,
					MaximumBytes: *maxBytes,
				},
			},
		},
	}
	credentials := config.CredentialFile{
		Profiles: map[string]config.Credential{
			*profileName: {Password: password},
		},
	}
	resolved := config.Paths{
		Config:      *paths.config,
		Credentials: *paths.credentials,
		StateDir:    *paths.stateDir,
	}
	if err := config.SaveInitial(cfg, credentials, resolved); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "Created profile %q.\n", *profileName)
	fmt.Fprintf(stdout, "Configuration: %s\n", resolved.Config)
	fmt.Fprintf(stdout, "Credentials:   %s\n", resolved.Credentials)
	fmt.Fprintln(stdout, "Run \"fivemesh-backup configure\" to review this profile or choose a schedule.")
	fmt.Fprintf(stdout, "Next: fivemesh-backup backup --profile %s\n", *profileName)
	return nil
}

func readInitialPassword(stdin io.Reader, stdout io.Writer, fromStdin bool) (string, error) {
	if fromStdin {
		line, err := readLine(bufio.NewReader(stdin))
		if err != nil {
			return "", fmt.Errorf("read password from standard input: %w", err)
		}
		if strings.ContainsAny(line, "\r\n\x00") {
			return "", errors.New("password contains unsupported control characters")
		}
		return line, nil
	}

	file, ok := stdin.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return "", errors.New("standard input is not an interactive terminal; use --password-stdin")
	}
	fmt.Fprint(stdout, "Database password: ")
	first, err := term.ReadPassword(int(file.Fd()))
	fmt.Fprintln(stdout)
	if err != nil {
		return "", fmt.Errorf("read database password: %w", err)
	}
	fmt.Fprint(stdout, "Confirm password: ")
	second, err := term.ReadPassword(int(file.Fd()))
	fmt.Fprintln(stdout)
	if err != nil {
		return "", fmt.Errorf("confirm database password: %w", err)
	}
	if string(first) != string(second) {
		return "", errors.New("passwords do not match")
	}
	if strings.ContainsAny(string(first), "\r\n\x00") {
		return "", errors.New("password contains unsupported control characters")
	}
	return string(first), nil
}
