package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/archive"
	"github.com/FiveMesh/fivemesh-backup/internal/notify"
	"github.com/FiveMesh/fivemesh-backup/internal/platform"
	"github.com/FiveMesh/fivemesh-backup/internal/restore"
	"github.com/FiveMesh/fivemesh-backup/internal/retention"
)

func runPrune(args []string, stdout, stderr io.Writer) error {
	defaults, err := defaultPaths()
	if err != nil {
		return err
	}
	flags := newFlagSet("prune", stderr)
	paths := addPathFlags(flags, defaults)
	profileName := flags.String("profile", "", "profile to prune")
	dryRun := flags.Bool("dry-run", false, "show deletions without changing files")
	confirmed := flags.Bool("yes", false, "confirm permanent archive deletion")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return withCode(exitUsage, errors.New("prune does not accept positional arguments"))
	}
	cfg, err := loadConfig(paths)
	if err != nil {
		return err
	}
	name, profile, err := cfg.ResolveProfile(*profileName)
	if err != nil {
		return err
	}
	plan, err := retention.BuildPlan(profile.Backup.Directory, name, profile.Retention, time.Now().UTC())
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Profile: %s\nArchives considered: %d\nArchives currently kept: %d\n",
		name,
		plan.ArchivesConsidered,
		plan.ArchivesKept,
	)
	if len(plan.ToDelete) == 0 {
		fmt.Fprintln(stdout, "Nothing to delete.")
	} else {
		fmt.Fprintln(stdout, "Selected for deletion:")
		for _, action := range plan.ToDelete {
			fmt.Fprintf(stdout, "  %s  %s  %s\n",
				action.CreatedAt.Local().Format(time.RFC3339),
				formatBytes(action.SizeBytes),
				filepath.Base(action.Path),
			)
		}
	}
	for _, warning := range plan.Warnings {
		fmt.Fprintf(stderr, "warning: %s\n", warning)
	}
	if *dryRun {
		fmt.Fprintln(stdout, "Dry run: no files changed.")
		return nil
	}
	if !*confirmed {
		return withCode(exitUsage, errors.New("refusing permanent deletion without --yes; preview with --dry-run first"))
	}
	deleted, err := retention.Execute(profile.Backup.Directory, name, plan)
	if err != nil {
		return err
	}
	var freed int64
	for _, action := range deleted {
		freed += action.SizeBytes
	}
	fmt.Fprintf(stdout, "Deleted %d archive(s), freeing %s.\n", len(deleted), formatBytes(freed))
	return nil
}

func runRestore(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	defaults, err := defaultPaths()
	if err != nil {
		return err
	}
	flags := newFlagSet("restore", stderr)
	paths := addPathFlags(flags, defaults)
	profileName := flags.String("profile", "", "profile that created the archive")
	archivePath := flags.String("archive", "", "verified archive path (required)")
	targetDatabase := flags.String("database", "", "explicit target database (required)")
	confirmed := flags.Bool("yes", false, "skip interactive confirmation")
	allowRoot := flags.Bool("allow-root", false, "allow the restore process to run as root")
	timeout := flags.Duration("timeout", 0, "optional restore timeout")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return withCode(exitUsage, errors.New("restore does not accept positional arguments"))
	}
	if strings.TrimSpace(*archivePath) == "" {
		return withCode(exitUsage, errors.New("--archive is required"))
	}
	if strings.TrimSpace(*targetDatabase) == "" || strings.HasPrefix(*targetDatabase, "-") {
		return withCode(exitUsage, errors.New("--database is required and must not start with '-'"))
	}
	if platform.IsRoot() && !*allowRoot {
		return withCode(exitUsage, errors.New("refusing to run restore as root; use a dedicated account or pass --allow-root deliberately"))
	}
	_, name, profile, password, err := loadSelectedProfile(paths, *profileName)
	if err != nil {
		return err
	}

	verification, err := archive.Verify(*archivePath)
	if err != nil {
		return withCode(exitVerification, fmt.Errorf("archive failed verification: %w", err))
	}
	if verification.Metadata.Profile != name {
		return fmt.Errorf("archive belongs to profile %q, not %q", verification.Metadata.Profile, name)
	}
	fmt.Fprintf(stdout, "Archive verified: %s\nSource database: %s\nCreated: %s\nTarget database: %s\n",
		filepath.Base(*archivePath),
		verification.Metadata.Database,
		verification.Metadata.CreatedAt.Local().Format(time.RFC3339),
		*targetDatabase,
	)
	fmt.Fprintln(stderr, "Restore can drop and replace objects in the target database.")
	if !*confirmed {
		if err := confirmRestore(stdin, stderr, *targetDatabase); err != nil {
			return err
		}
	}

	progress := &restoreProgress{writer: stderr, interactive: isTerminalWriter(stderr)}
	restoreStarted := time.Now()
	runner := restore.New(name, profile, password)
	metadata, err := runner.Run(ctx, *archivePath, *targetDatabase, restore.Options{
		Timeout:      *timeout,
		Verification: &verification,
		OnPhase:      progress.phase,
		OnProgress: func(current restore.Progress) {
			progress.update(current)
		},
	})
	progress.finish()
	sendNotification(paths, notify.RestoreEvent(name, *archivePath, *targetDatabase, time.Since(restoreStarted), err), stderr)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Restore client completed for database %s.\n", *targetDatabase)
	fmt.Fprintf(stdout, "Source archive database: %s; archive SHA-256: %s\n",
		metadata.Database,
		shortHash(metadata.SHA256),
	)
	fmt.Fprintln(stdout, "A successful restore client exit does not by itself validate application-level data.")
	return nil
}

func confirmRestore(stdin io.Reader, writer io.Writer, targetDatabase string) error {
	fmt.Fprintf(writer, "Type %q to continue: ", targetDatabase)
	line, err := readLine(bufio.NewReader(stdin))
	if err != nil {
		return fmt.Errorf("read restore confirmation: %w", err)
	}
	if strings.TrimSpace(line) != targetDatabase {
		return withCode(exitUsage, errors.New("restore cancelled; target database confirmation did not match"))
	}
	return nil
}

type restoreProgress struct {
	writer      io.Writer
	interactive bool
	lastLine    bool
}

func (p *restoreProgress) phase(phase string) {
	if p.interactive {
		fmt.Fprintf(p.writer, "\r%-24s", strings.ReplaceAll(phase, "-", " "))
		p.lastLine = true
		return
	}
	fmt.Fprintf(p.writer, "phase: %s\n", phase)
}

func (p *restoreProgress) update(current restore.Progress) {
	if !p.interactive {
		return
	}
	fmt.Fprintf(p.writer, "\rrestoring %s in %s   ",
		formatBytes(current.UncompressedBytes),
		formatDuration(current.Elapsed),
	)
	p.lastLine = true
}

func (p *restoreProgress) finish() {
	if p.interactive && p.lastLine {
		fmt.Fprintln(p.writer)
	}
}
