package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/archive"
	"github.com/FiveMesh/fivemesh-backup/internal/history"
	"github.com/FiveMesh/fivemesh-backup/internal/runlock"
)

func runVerify(args []string, stdout, stderr io.Writer) error {
	defaults, err := defaultPaths()
	if err != nil {
		return err
	}
	flags := newFlagSet("verify", stderr)
	paths := addPathFlags(flags, defaults)
	profileName := flags.String("profile", "", "profile whose archive directory should be scanned")
	all := flags.Bool("all", false, "verify every archive for the selected profile")
	if err := parseFlags(flags, args); err != nil {
		return err
	}

	var pathsToVerify []string
	if *all {
		if len(flags.Args()) != 0 {
			return withCode(exitUsage, errors.New("--all cannot be combined with archive paths"))
		}
		cfg, err := loadConfig(paths)
		if err != nil {
			return err
		}
		resolvedName, profile, err := cfg.ResolveProfile(*profileName)
		if err != nil {
			return err
		}
		scanned, warnings, err := archive.Scan(profile.Backup.Directory)
		if err != nil {
			return err
		}
		for _, warning := range warnings {
			fmt.Fprintf(stderr, "warning: skip %s: %v\n", warning.Path, warning.Err)
		}
		for _, item := range scanned {
			if item.Metadata.Profile == resolvedName {
				pathsToVerify = append(pathsToVerify, item.Path)
			}
		}
		if len(pathsToVerify) == 0 {
			return fmt.Errorf("no archives found for profile %q", resolvedName)
		}
	} else {
		if len(flags.Args()) != 1 {
			return withCode(exitUsage, errors.New("verify requires exactly one archive path, or --all"))
		}
		pathsToVerify = flags.Args()
	}

	failed := false
	for _, path := range pathsToVerify {
		verification, err := archive.Verify(path)
		if err != nil {
			failed = true
			fmt.Fprintf(stdout, "FAILED  %s: %v\n", filepath.Base(path), err)
			continue
		}
		fmt.Fprintf(stdout, "OK      %s  %s  %s\n",
			filepath.Base(path),
			verification.Metadata.CreatedAt.Local().Format(time.RFC3339),
			shortHash(verification.Metadata.SHA256),
		)
	}
	if failed {
		return withCode(exitVerification, errors.New("one or more archives failed verification"))
	}
	return nil
}

func runList(args []string, stdout, stderr io.Writer) error {
	defaults, err := defaultPaths()
	if err != nil {
		return err
	}
	flags := newFlagSet("list", stderr)
	paths := addPathFlags(flags, defaults)
	profileName := flags.String("profile", "", "profile to list")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return withCode(exitUsage, errors.New("list does not accept positional arguments"))
	}
	cfg, err := loadConfig(paths)
	if err != nil {
		return err
	}
	name, profile, err := cfg.ResolveProfile(*profileName)
	if err != nil {
		return err
	}
	archives, warnings, err := archive.Scan(profile.Backup.Directory)
	if err != nil {
		return err
	}
	for _, warning := range warnings {
		fmt.Fprintf(stderr, "warning: skip %s: %v\n", warning.Path, warning.Err)
	}
	if len(archives) == 0 {
		fmt.Fprintf(stdout, "No archives found for profile %q.\n", name)
		return nil
	}
	fmt.Fprintln(stdout, "CREATED                  SIZE        STATE     SHA-256   ARCHIVE")
	found := false
	for _, item := range archives {
		if item.Metadata.Profile != name {
			continue
		}
		found = true
		state := "incomplete"
		if item.Metadata.Complete {
			state = "complete"
		}
		fmt.Fprintf(stdout, "%-24s %-11s %-9s %-9s %s\n",
			item.Metadata.CreatedAt.Local().Format("2006-01-02 15:04:05"),
			formatBytes(item.Metadata.SizeBytes),
			state,
			shortHash(item.Metadata.SHA256),
			filepath.Base(item.Path),
		)
	}
	if !found {
		fmt.Fprintf(stdout, "No archives found for profile %q.\n", name)
	}
	return nil
}

func runStatus(args []string, stdout, stderr io.Writer) error {
	defaults, err := defaultPaths()
	if err != nil {
		return err
	}
	flags := newFlagSet("status", stderr)
	paths := addPathFlags(flags, defaults)
	profileName := flags.String("profile", "", "show only this profile")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return withCode(exitUsage, errors.New("status does not accept positional arguments"))
	}
	store := history.New(*paths.stateDir, filepath.Join(*paths.stateDir, "logs"))
	records, warnings, err := store.Read()
	if err != nil {
		return err
	}
	profilesToCheck := make(map[string]struct{})
	for _, record := range records {
		if record.Status == history.StatusRunning {
			profilesToCheck[record.Profile] = struct{}{}
		}
	}
	for profile := range profilesToCheck {
		_, recoveryErr := store.RecoverInterrupted(
			profile,
			func() (bool, error) {
				return runlock.IsActive(filepath.Join(*paths.stateDir, "locks"), profile)
			},
			time.Now().UTC(),
		)
		if recoveryErr != nil {
			fmt.Fprintf(stderr, "warning: could not reconcile profile %q: %v\n", profile, recoveryErr)
		}
	}
	records, warnings, err = store.Read()
	if err != nil {
		return err
	}
	for _, warning := range warnings {
		fmt.Fprintf(stderr, "warning: history line %d is invalid: %v\n", warning.Line, warning.Err)
	}
	if len(records) == 0 {
		fmt.Fprintln(stdout, "No backup executions have been recorded.")
		return nil
	}
	latest := latestByProfile(records)
	names := make([]string, 0, len(latest))
	for name := range latest {
		if *profileName == "" || name == *profileName {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return fmt.Errorf("no execution history found for profile %q", *profileName)
	}
	for _, name := range names {
		record := latest[name]
		fmt.Fprintf(stdout, "%s: %s at %s (%s)\n",
			name,
			record.Status,
			record.StartedAt.Local().Format(time.RFC3339),
			record.Phase,
		)
		if record.Archive != "" {
			fmt.Fprintf(stdout, "  archive: %s\n", record.Archive)
		}
		if record.Error != "" {
			fmt.Fprintf(stdout, "  error: %s\n", record.Error)
		}
		if record.RetentionError != "" {
			fmt.Fprintf(stdout, "  retention warning: %s\n", record.RetentionError)
		}
		fmt.Fprintf(stdout, "  log: fivemesh-backup logs %s\n", record.ID)
	}
	return nil
}

func runLogs(args []string, stdout, stderr io.Writer) error {
	defaults, err := defaultPaths()
	if err != nil {
		return err
	}
	flags := newFlagSet("logs", stderr)
	paths := addPathFlags(flags, defaults)
	profileName := flags.String("profile", "", "profile whose latest run should be shown")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if len(flags.Args()) > 1 {
		return withCode(exitUsage, errors.New("logs accepts at most one run ID"))
	}
	store := history.New(*paths.stateDir, filepath.Join(*paths.stateDir, "logs"))
	runID := ""
	if len(flags.Args()) == 1 {
		runID = flags.Args()[0]
	} else {
		records, _, err := store.Read()
		if err != nil {
			return err
		}
		var latest history.Record
		for _, record := range records {
			if *profileName != "" && record.Profile != *profileName {
				continue
			}
			if latest.ID == "" || record.StartedAt.After(latest.StartedAt) {
				latest = record
			}
		}
		if latest.ID == "" {
			return errors.New("no matching execution log found")
		}
		runID = latest.ID
	}
	if strings.ContainsAny(runID, `/\`) || runID == "." || runID == ".." {
		return withCode(exitUsage, errors.New("invalid run ID"))
	}
	data, err := store.ReadLog(runID)
	if err != nil {
		return err
	}
	_, err = stdout.Write(data)
	return err
}

func shortHash(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
