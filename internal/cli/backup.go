package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/backup"
	"github.com/FiveMesh/fivemesh-backup/internal/history"
	"github.com/FiveMesh/fivemesh-backup/internal/notify"
	"github.com/FiveMesh/fivemesh-backup/internal/platform"
	"golang.org/x/term"
)

func runBackup(ctx context.Context, args []string, _ io.Reader, stdout, stderr io.Writer) error {
	defaults, err := defaultPaths()
	if err != nil {
		return err
	}
	flags := newFlagSet("backup", stderr)
	paths := addPathFlags(flags, defaults)
	profileName := flags.String("profile", "", "profile to back up")
	timeout := flags.Duration("timeout", 0, "override the configured execution timeout")
	skipRetention := flags.Bool("no-retention", false, "skip retention after this successful backup")
	allowRoot := flags.Bool("allow-root", false, "allow the backup process to run as root")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return withCode(exitUsage, errors.New("backup does not accept positional arguments"))
	}
	if platform.IsRoot() && !*allowRoot {
		return withCode(exitUsage, errors.New("refusing to run backup as root; use a dedicated account or pass --allow-root deliberately"))
	}
	_, name, profile, password, err := loadSelectedProfile(paths, *profileName)
	if err != nil {
		return err
	}

	progress := &progressDisplay{writer: stderr, interactive: isTerminalWriter(stderr)}
	runner := backup.New(name, profile, password, *paths.stateDir)
	outcome, err := runner.Run(ctx, backup.Options{
		Timeout:       *timeout,
		SkipRetention: *skipRetention,
		OnPhase: func(phase string) {
			progress.phase(phase)
		},
		OnProgress: func(current backup.Progress) {
			progress.backup(current)
		},
	})
	progress.finish()
	if err != nil && errors.Is(err, backup.ErrAlreadyRunning) {
		return withCode(exitBusy, err)
	}
	if outcome.Record.ID != "" {
		sendNotification(paths, notify.BackupEvent(name, profile, outcome.Record), stderr)
	}
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "Backup complete: %s\n", outcome.Record.Archive)
	fmt.Fprintf(stdout, "SHA-256: %s\n", outcome.Record.ArchiveSHA256)
	fmt.Fprintf(stdout, "Archive: %s; SQL stream: %s; duration: %s\n",
		formatBytes(outcome.Record.ArchiveSize),
		formatBytes(outcome.Record.UncompressedSize),
		formatDuration(time.Duration(outcome.Record.DurationMS)*time.Millisecond),
	)
	if outcome.Record.RetentionDeleted > 0 {
		fmt.Fprintf(stdout, "Retention removed %d archive(s), freeing %s.\n",
			outcome.Record.RetentionDeleted,
			formatBytes(outcome.Record.RetentionFreedBytes),
		)
	}
	for _, warning := range outcome.Warnings {
		fmt.Fprintf(stderr, "warning: %s\n", warning)
	}
	return nil
}

type progressDisplay struct {
	writer      io.Writer
	interactive bool
	lastLine    bool
}

func (p *progressDisplay) phase(phase string) {
	if p.interactive {
		fmt.Fprintf(p.writer, "\r%-24s", strings.ReplaceAll(phase, "-", " "))
		p.lastLine = true
		return
	}
	fmt.Fprintf(p.writer, "phase: %s\n", phase)
}

func (p *progressDisplay) backup(progress backup.Progress) {
	if !p.interactive {
		return
	}
	fmt.Fprintf(p.writer, "\rdumping %s in %s   ",
		formatBytes(progress.UncompressedBytes),
		formatDuration(progress.Elapsed),
	)
	p.lastLine = true
}

func (p *progressDisplay) finish() {
	if p.interactive && p.lastLine {
		// Clear the transient progress line instead of leaving the last
		// phase name behind.
		fmt.Fprintf(p.writer, "\r%-48s\r", "")
		p.lastLine = false
	}
}

func isTerminalWriter(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func formatDuration(duration time.Duration) string {
	switch {
	case duration < time.Second:
		return duration.Round(time.Millisecond).String()
	case duration < time.Minute:
		return duration.Round(100 * time.Millisecond).String()
	default:
		return duration.Round(time.Second).String()
	}
}

func latestByProfile(records []history.Record) map[string]history.Record {
	latest := make(map[string]history.Record)
	for _, record := range records {
		current, exists := latest[record.Profile]
		if !exists || record.StartedAt.After(current.StartedAt) {
			latest[record.Profile] = record
		}
	}
	return latest
}
