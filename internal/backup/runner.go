package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/archive"
	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/diskfree"
	"github.com/FiveMesh/fivemesh-backup/internal/history"
	"github.com/FiveMesh/fivemesh-backup/internal/mysql"
	"github.com/FiveMesh/fivemesh-backup/internal/retention"
	"github.com/FiveMesh/fivemesh-backup/internal/runlock"
)

const (
	freeSpaceWarningPercent = 10
	defaultReadinessTimeout = 120 * time.Second
	defaultReadinessRetry   = 2 * time.Second
	defaultProbeTimeout     = 10 * time.Second
)

var ErrAlreadyRunning = errors.New("a backup is already running for this profile")
var ErrReadinessTimeout = errors.New("database readiness timeout")

type Progress struct {
	UncompressedBytes int64
	Elapsed           time.Duration
}

type Options struct {
	Timeout               time.Duration
	ReadinessTimeout      time.Duration
	ReadinessRetryDelay   time.Duration
	ReadinessProbeTimeout time.Duration
	SkipRetention         bool
	OnPhase               func(phase string)
	OnProgress            func(Progress)
}

type Outcome struct {
	Record   history.Record
	LogPath  string
	Warnings []string
}

type Runner struct {
	ProfileName string
	Profile     config.Profile
	Password    string
	StateDir    string

	Discover        func(context.Context, config.Database) (mysql.Tools, error)
	Dump            func(context.Context, mysql.ToolInfo, config.Database, string, io.Writer) (mysql.DumpResult, error)
	CheckConnection func(context.Context, mysql.ToolInfo, config.Database, string) error
}

func New(profileName string, profile config.Profile, password, stateDir string) *Runner {
	return &Runner{
		ProfileName:     profileName,
		Profile:         profile,
		Password:        password,
		StateDir:        stateDir,
		Discover:        mysql.Discover,
		Dump:            mysql.Dump,
		CheckConnection: mysql.TestConnection,
	}
}

func (r *Runner) Run(ctx context.Context, options Options) (Outcome, error) {
	if r.Discover == nil {
		r.Discover = mysql.Discover
	}
	if r.Dump == nil {
		r.Dump = mysql.Dump
	}
	if r.CheckConnection == nil {
		r.CheckConnection = mysql.TestConnection
	}
	started := time.Now().UTC()
	runID, err := history.NewRunID(started)
	if err != nil {
		return Outcome{}, err
	}
	locksDir, logsDir, err := config.EnsureStateDirs(r.StateDir)
	if err != nil {
		return Outcome{}, err
	}
	store := history.New(r.StateDir, logsDir)
	logFile, err := store.StartLog(runID)
	if err != nil {
		return Outcome{}, err
	}
	defer logFile.Close()

	record := history.Record{
		ID:        runID,
		Profile:   r.ProfileName,
		Status:    history.StatusRunning,
		Phase:     "starting",
		StartedAt: started,
	}
	var warnings []string
	outcome := Outcome{Record: record, LogPath: store.LogPath(runID)}
	logEvent(logFile, "started profile=%s run=%s", r.ProfileName, runID)
	setPhase := func(phase string) {
		record.Phase = phase
		logEvent(logFile, "phase=%s", phase)
		if options.OnPhase != nil {
			options.OnPhase(phase)
		}
	}
	fail := func(phase string, runErr error, exitCode int) (Outcome, error) {
		record.Status = history.StatusFailed
		if errors.Is(runErr, context.Canceled) {
			record.Status = history.StatusInterrupted
			phase = "cancelled"
		} else if errors.Is(runErr, context.DeadlineExceeded) {
			phase = "timeout"
		}
		record.Phase = phase
		record.FinishedAt = time.Now().UTC()
		record.DurationMS = record.FinishedAt.Sub(started).Milliseconds()
		record.Error = sanitizeHistoryError(runErr, r.Password)
		record.DumpExitCode = exitCode
		logEvent(logFile, "failed phase=%s error=%s", phase, record.Error)
		if appendErr := store.Append(record); appendErr != nil {
			warnings = append(warnings, "execution history could not be recorded: "+appendErr.Error())
		}
		outcome.Record = record
		outcome.Warnings = warnings
		return outcome, runErr
	}

	lock, acquired, err := runlock.Acquire(locksDir, r.ProfileName)
	if err != nil {
		return fail("acquire-lock", err, -1)
	}
	if !acquired {
		return fail("acquire-lock", ErrAlreadyRunning, -1)
	}
	defer func() {
		if err := lock.Release(); err != nil {
			logEvent(logFile, "warning lock-release error=%s", sanitizeHistoryError(err, r.Password))
		}
	}()

	recovered, err := store.RecoverInterrupted(
		r.ProfileName,
		func() (bool, error) { return false, nil },
		time.Now().UTC(),
	)
	if err != nil {
		return fail("recovering-history", err, -1)
	}
	if len(recovered) > 0 {
		logEvent(logFile, "recovered_interrupted_runs=%d", len(recovered))
	}
	if err := store.Append(record); err != nil {
		return outcome, fmt.Errorf("record starting execution: %w", err)
	}

	setPhase("discovering-client")
	tools, err := r.Discover(ctx, r.Profile.Database)
	if err != nil {
		return fail("discovering-client", err, -1)
	}
	record.DumpTool = tools.Dump.Path
	record.DumpToolVersion = tools.Dump.Version
	logEvent(logFile, "dump_tool=%s version=%s", tools.Dump.Path, tools.Dump.Version)

	executionContext := ctx
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = r.Profile.BackupTimeout(0)
	}
	var cancel context.CancelFunc
	if timeout > 0 {
		executionContext, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
		logEvent(logFile, "timeout=%s", timeout)
	}

	setPhase("preparing-storage")
	backupDirectory := filepath.Clean(r.Profile.Backup.Directory)
	if err := os.MkdirAll(backupDirectory, 0o750); err != nil {
		return fail("preparing-storage", fmt.Errorf("create backup directory: %w", err), -1)
	}
	removedPartials, err := archive.CleanupPartials(backupDirectory, r.ProfileName)
	if err != nil {
		return fail("preparing-storage", err, -1)
	}
	for _, path := range removedPartials {
		logEvent(logFile, "removed_interrupted_partial=%s", filepath.Base(path))
	}
	availableBytes, err := diskfree.AvailableBytes(backupDirectory)
	if err != nil {
		return fail("preparing-storage", err, -1)
	}
	if r.Profile.Backup.MinimumFreeSpaceBytes > 0 && availableBytes < uint64(r.Profile.Backup.MinimumFreeSpaceBytes) {
		return fail("preparing-storage", fmt.Errorf(
			"backup directory has %s free; configured minimum is %s",
			formatBytes(int64(availableBytes)),
			formatBytes(r.Profile.Backup.MinimumFreeSpaceBytes),
		), -1)
	}
	minimumBytes := uint64(r.Profile.Backup.MinimumFreeSpaceBytes)
	if minimumBytes > 0 && availableBytes-minimumBytes < minimumBytes/uint64(freeSpaceWarningPercent) {
		warnings = append(warnings, "free space is close to the configured minimum; compressed archive size cannot be predicted exactly")
	}

	setPhase("waiting-for-database")
	readinessAttempts, readinessWait, readinessErr := r.waitForDatabase(
		executionContext,
		tools.Client,
		r.Profile.Database,
		r.Password,
		options,
		logFile,
	)
	record.ReadinessAttempts = readinessAttempts
	record.ReadinessWaitMS = readinessWait.Milliseconds()
	if readinessErr != nil {
		phase := "database-readiness"
		if errors.Is(readinessErr, ErrReadinessTimeout) {
			phase = "database-readiness-timeout"
		}
		return fail(phase, readinessErr, -1)
	}

	archivePath, err := archive.NewPath(backupDirectory, r.ProfileName, started)
	if err != nil {
		return fail("preparing-storage", err, -1)
	}
	builder, err := archive.NewBuilder(archivePath, r.Profile.Backup.CompressionLevel)
	if err != nil {
		return fail("preparing-storage", err, -1)
	}
	committed := false
	defer func() {
		if !committed {
			builder.Abort()
		}
	}()

	setPhase("dumping")

	destination := &progressWriter{
		destination: builder,
		started:     started,
		callback:    options.OnProgress,
	}
	dumpResult, dumpErr := r.Dump(executionContext, tools.Dump, r.Profile.Database, r.Password, destination)
	record.DumpExitCode = dumpResult.ExitCode
	if dumpErr != nil {
		phase := "dumping"
		if executionContext.Err() != nil {
			phase = "cancelled"
		}
		return fail(phase, dumpErr, dumpResult.ExitCode)
	}

	setPhase("finalizing")
	consistency := archive.ConsistencyMetadata{SingleTransaction: r.Profile.Database.SingleTransaction}
	if r.Profile.Database.SingleTransaction {
		consistency.Warnings = append(consistency.Warnings,
			"single-transaction does not provide a consistent snapshot for MyISAM or other nontransactional tables")
	} else {
		consistency.Warnings = append(consistency.Warnings,
			"single-transaction is disabled; tables may change while the dump is running")
	}
	consistency.Warnings = append(consistency.Warnings,
		"concurrent schema changes can still produce an inconsistent dump")
	// Consistency caveats are inherent to logical dumps and documented; they
	// are recorded in archive metadata rather than printed after every run.
	result, err := builder.Commit(archive.Metadata{
		RunID:            runID,
		Profile:          r.ProfileName,
		Database:         r.Profile.Database.Database,
		Engine:           r.Profile.Database.Engine,
		CreatedAt:        started,
		CompressionLevel: r.Profile.Backup.CompressionLevel,
		DumpTool:         filepath.Base(tools.Dump.Path),
		DumpToolVersion:  tools.Dump.Version,
		Consistency:      consistency,
	})
	if err != nil {
		return fail("finalizing", err, 0)
	}
	committed = true

	setPhase("verifying")
	if _, err := archive.Verify(result.Path); err != nil {
		removeErr := archive.Remove(result.Path)
		if removeErr != nil {
			logEvent(logFile, "warning failed to remove invalid archive=%s", sanitizeHistoryError(removeErr, r.Password))
		}
		return fail("verifying", fmt.Errorf("archive verification failed: %w", err), 0)
	}
	record.Verified = true
	record.Archive = result.Path
	record.ArchiveSHA256 = result.Metadata.SHA256
	record.ArchiveSize = result.Metadata.SizeBytes
	record.UncompressedSize = result.Metadata.UncompressedSize

	if !options.SkipRetention && retentionEnabled(r.Profile.Retention) {
		setPhase("retention")
		plan, planErr := retention.BuildPlan(backupDirectory, r.ProfileName, r.Profile.Retention, time.Now().UTC())
		if planErr != nil {
			record.RetentionError = sanitizeHistoryError(planErr, r.Password)
			warnings = append(warnings, "backup succeeded but retention planning failed: "+record.RetentionError)
		} else {
			deleted, deleteErr := retention.Execute(backupDirectory, r.ProfileName, plan)
			record.RetentionDeleted = len(deleted)
			for _, action := range deleted {
				record.RetentionFreedBytes += action.SizeBytes
			}
			if deleteErr != nil {
				record.RetentionError = sanitizeHistoryError(deleteErr, r.Password)
				warnings = append(warnings, "backup succeeded but retention failed: "+record.RetentionError)
			}
			warnings = append(warnings, plan.Warnings...)
		}
	}

	record.Status = history.StatusSuccess
	record.Phase = "complete"
	record.FinishedAt = time.Now().UTC()
	record.DurationMS = record.FinishedAt.Sub(started).Milliseconds()
	if record.RetentionDeleted > 0 {
		logEvent(logFile, "retention_deleted=%d freed_bytes=%d",
			record.RetentionDeleted,
			record.RetentionFreedBytes,
		)
	}
	logEvent(logFile, "success archive=%s bytes=%d sha256=%s duration_ms=%d",
		record.Archive, record.ArchiveSize, record.ArchiveSHA256, record.DurationMS)
	if err := store.Append(record); err != nil {
		warnings = append(warnings, "backup succeeded but execution history could not be recorded: "+err.Error())
	}
	outcome.Record = record
	outcome.Warnings = warnings
	return outcome, nil
}

func (r *Runner) waitForDatabase(
	ctx context.Context,
	tool mysql.ToolInfo,
	database config.Database,
	password string,
	options Options,
	logWriter io.Writer,
) (int, time.Duration, error) {
	readinessTimeout := options.ReadinessTimeout
	if readinessTimeout <= 0 {
		readinessTimeout = defaultReadinessTimeout
	}
	retryDelay := options.ReadinessRetryDelay
	if retryDelay <= 0 {
		retryDelay = defaultReadinessRetry
	}
	probeTimeout := options.ReadinessProbeTimeout
	if probeTimeout <= 0 {
		probeTimeout = defaultProbeTimeout
	}

	started := time.Now()
	deadline := started.Add(readinessTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	attempts := 0
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return attempts, time.Since(started), ctxErr
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return attempts, time.Since(started), fmt.Errorf(
				"%w after %s",
				ErrReadinessTimeout,
				readinessTimeout.Round(time.Second),
			)
		}
		attemptTimeout := probeTimeout
		if remaining < attemptTimeout {
			attemptTimeout = remaining
		}
		attemptContext, cancel := context.WithTimeout(ctx, attemptTimeout)
		attempts++
		err := r.CheckConnection(attemptContext, tool, database, password)
		cancel()
		if err == nil {
			logEvent(logWriter, "database_ready attempts=%d wait_ms=%d", attempts, time.Since(started).Milliseconds())
			return attempts, time.Since(started), nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return attempts, time.Since(started), ctxErr
		}
		if !time.Now().Before(deadline) {
			return attempts, time.Since(started), fmt.Errorf(
				"%w after %s: %s",
				ErrReadinessTimeout,
				readinessTimeout.Round(time.Second),
				sanitizeHistoryError(err, password),
			)
		}
		if !mysql.IsTransientConnectionError(err) {
			return attempts, time.Since(started), fmt.Errorf("database readiness check failed: %w", err)
		}
		logEvent(
			logWriter,
			"database_not_ready attempt=%d retry_in=%s error=%s",
			attempts,
			retryDelay,
			sanitizeHistoryError(err, password),
		)
		wait := retryDelay
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			// Go 1.23+ timers use synchronous channels. Do not drain after Stop:
			// receiving from a stopped timer channel could block indefinitely.
			timer.Stop()
			return attempts, time.Since(started), ctx.Err()
		case <-timer.C:
		}
	}
}

type progressWriter struct {
	destination io.Writer
	started     time.Time
	bytes       int64
	lastUpdate  time.Time
	callback    func(Progress)
}

func (w *progressWriter) Write(data []byte) (int, error) {
	n, err := w.destination.Write(data)
	w.bytes += int64(n)
	now := time.Now()
	if w.callback != nil && (w.lastUpdate.IsZero() || now.Sub(w.lastUpdate) >= time.Second) {
		w.lastUpdate = now
		w.callback(Progress{
			UncompressedBytes: w.bytes,
			Elapsed:           now.Sub(w.started),
		})
	}
	return n, err
}

func retentionEnabled(policy config.Retention) bool {
	return policy.KeepLast > 0 || policy.KeepDays > 0 || policy.MaximumBytes > 0
}

func sanitizeHistoryError(err error, password string) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if password != "" {
		message = strings.ReplaceAll(message, password, "[REDACTED]")
	}
	if len(message) > 8192 {
		message = message[len(message)-8192:]
	}
	return message
}

func logEvent(writer io.Writer, format string, arguments ...any) {
	if writer == nil {
		return
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	_, _ = fmt.Fprintf(writer, "%s %s\n", timestamp, fmt.Sprintf(format, arguments...))
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	divisor, exponent := int64(unit), 0
	for value := bytes / unit; value >= unit && exponent < 5; value /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(divisor), "KMGTPE"[exponent])
}
