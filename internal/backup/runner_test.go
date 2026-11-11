package backup

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/history"
	"github.com/FiveMesh/fivemesh-backup/internal/mysql"
	"github.com/FiveMesh/fivemesh-backup/internal/runlock"
)

func TestSuccessfulBackupPublishesVerifiedArchive(t *testing.T) {
	root := t.TempDir()
	runner := testRunner(t, root)
	runner.Dump = func(_ context.Context, _ mysql.ToolInfo, _ config.Database, _ string, destination io.Writer) (mysql.DumpResult, error) {
		data := []byte("CREATE TABLE players (id BIGINT);\n")
		if _, err := destination.Write(data); err != nil {
			return mysql.DumpResult{}, err
		}
		return mysql.DumpResult{Bytes: int64(len(data))}, nil
	}

	outcome, err := runner.Run(context.Background(), Options{})
	if err != nil {
		t.Fatalf("run backup: %v", err)
	}
	if outcome.Record.Status != history.StatusSuccess || !outcome.Record.Verified {
		t.Fatalf("unexpected outcome: %#v", outcome.Record)
	}
	if _, err := os.Stat(outcome.Record.Archive); err != nil {
		t.Fatalf("archive was not published: %v", err)
	}
	store := history.New(filepath.Join(root, "state"), filepath.Join(root, "state", "logs"))
	records, _, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Status != history.StatusSuccess {
		t.Fatalf("unexpected history: %#v", records)
	}
}

func TestFailedDumpLeavesNoArchive(t *testing.T) {
	root := t.TempDir()
	runner := testRunner(t, root)
	runner.Dump = func(_ context.Context, _ mysql.ToolInfo, _ config.Database, _ string, destination io.Writer) (mysql.DumpResult, error) {
		_, _ = destination.Write([]byte("partial dump"))
		return mysql.DumpResult{ExitCode: 2}, errors.New("authentication failed")
	}

	_, err := runner.Run(context.Background(), Options{})
	if err == nil {
		t.Fatal("expected failed backup")
	}
	entries, err := os.ReadDir(runner.Profile.Backup.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed dump left archive artifacts: %v", entries)
	}
}

func TestCancelledDumpLeavesNoArchive(t *testing.T) {
	root := t.TempDir()
	runner := testRunner(t, root)
	runner.Dump = func(ctx context.Context, _ mysql.ToolInfo, _ config.Database, _ string, destination io.Writer) (mysql.DumpResult, error) {
		_, _ = destination.Write([]byte("partial dump"))
		<-ctx.Done()
		return mysql.DumpResult{ExitCode: -1}, ctx.Err()
	}

	_, err := runner.Run(context.Background(), Options{Timeout: 30 * time.Millisecond})
	if err == nil {
		t.Fatal("expected timeout failure")
	}
	entries, err := os.ReadDir(runner.Profile.Backup.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("cancelled dump left archive artifacts: %v", entries)
	}
}

func TestGracefulCancellationRecordsInterruptedStatus(t *testing.T) {
	root := t.TempDir()
	runner := testRunner(t, root)
	started := make(chan struct{})
	runner.Dump = func(ctx context.Context, _ mysql.ToolInfo, _ config.Database, _ string, destination io.Writer) (mysql.DumpResult, error) {
		_, _ = destination.Write([]byte("partial dump"))
		close(started)
		<-ctx.Done()
		return mysql.DumpResult{ExitCode: -1}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()

	outcome, err := runner.Run(ctx, Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if outcome.Record.Status != history.StatusInterrupted {
		t.Fatalf("status = %q, want interrupted", outcome.Record.Status)
	}
}

func TestNextRunRecoversAbandonedPreviousExecution(t *testing.T) {
	root := t.TempDir()
	runner := testRunner(t, root)
	store := history.New(runner.StateDir, filepath.Join(runner.StateDir, "logs"))
	if err := store.Append(history.Record{
		ID:        "abandoned",
		Profile:   runner.ProfileName,
		Status:    history.StatusRunning,
		Phase:     "dumping",
		StartedAt: time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	runner.Dump = func(_ context.Context, _ mysql.ToolInfo, _ config.Database, _ string, destination io.Writer) (mysql.DumpResult, error) {
		_, err := destination.Write([]byte("CREATE TABLE recovered (id BIGINT);\n"))
		return mysql.DumpResult{Bytes: 36}, err
	}
	if _, err := runner.Run(context.Background(), Options{}); err != nil {
		t.Fatalf("run backup after abandoned history: %v", err)
	}
	records, _, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	var recovered, successful bool
	for _, record := range records {
		if record.ID == "abandoned" && record.Status == history.StatusInterrupted && record.Recovered {
			recovered = true
		}
		if record.Status == history.StatusSuccess {
			successful = true
		}
	}
	if !recovered || !successful {
		t.Fatalf("unexpected history after recovery: %#v", records)
	}
}

func TestAbruptProcessTerminationLeavesRecoverableHistory(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	cmd := exec.Command(os.Args[0], "-test.run=TestCrashHelperProcess", "--")
	cmd.Env = append(os.Environ(),
		"FIVEMESH_CRASH_HELPER=1",
		"FIVEMESH_CRASH_STATE_DIR="+stateDir,
	)
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 {
		t.Fatalf("helper process did not terminate abruptly as expected: %v", err)
	}

	store := history.New(stateDir, filepath.Join(stateDir, "logs"))
	locksDir := filepath.Join(stateDir, "locks")
	active, err := runlock.IsActive(locksDir, "production")
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("profile lock remained active after helper process death")
	}
	recovered, err := store.RecoverInterrupted(
		"production",
		func() (bool, error) { return runlock.IsActive(locksDir, "production") },
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].Status != history.StatusInterrupted {
		t.Fatalf("unexpected recovery after process death: %#v", recovered)
	}
}

func TestIndependentProfilesCanBackupConcurrently(t *testing.T) {
	root := t.TempDir()
	production := testRunner(t, root)
	staging := testRunner(t, root)
	staging.ProfileName = "staging"
	staging.Profile.Backup.Directory = filepath.Join(root, "backups-staging")
	production.CheckConnection = func(context.Context, mysql.ToolInfo, config.Database, string) error {
		time.Sleep(25 * time.Millisecond)
		return nil
	}
	production.Dump = func(_ context.Context, _ mysql.ToolInfo, _ config.Database, _ string, destination io.Writer) (mysql.DumpResult, error) {
		time.Sleep(30 * time.Millisecond)
		_, err := destination.Write([]byte("CREATE TABLE production_data (id BIGINT);\n"))
		return mysql.DumpResult{Bytes: 40}, err
	}
	staging.Dump = func(_ context.Context, _ mysql.ToolInfo, _ config.Database, _ string, destination io.Writer) (mysql.DumpResult, error) {
		time.Sleep(30 * time.Millisecond)
		_, err := destination.Write([]byte("CREATE TABLE staging_data (id BIGINT);\n"))
		return mysql.DumpResult{Bytes: 37}, err
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, runner := range []*Runner{production, staging} {
		wait.Add(1)
		go func(runner *Runner) {
			defer wait.Done()
			<-start
			_, err := runner.Run(context.Background(), Options{})
			results <- err
		}(runner)
	}
	close(start)
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent backup failed: %v", err)
		}
	}
	store := history.New(production.StateDir, filepath.Join(production.StateDir, "logs"))
	records, _, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	successes := 0
	for _, record := range records {
		if record.Status == history.StatusSuccess {
			successes++
		}
	}
	if successes != 2 {
		t.Fatalf("successful records = %d, want 2: %#v", successes, records)
	}
}

func TestCrashHelperProcess(t *testing.T) {
	if os.Getenv("FIVEMESH_CRASH_HELPER") != "1" {
		return
	}
	stateDir := os.Getenv("FIVEMESH_CRASH_STATE_DIR")
	locksDir, logsDir, err := config.EnsureStateDirs(stateDir)
	if err != nil {
		os.Exit(2)
	}
	lock, acquired, err := runlock.Acquire(locksDir, "production")
	if err != nil || !acquired {
		os.Exit(3)
	}
	_ = lock
	store := history.New(stateDir, logsDir)
	if err := store.Append(history.Record{
		ID:        "abrupt-run",
		Profile:   "production",
		Status:    history.StatusRunning,
		Phase:     "dumping",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		os.Exit(4)
	}
	os.Exit(17)
}

func TestProfileOverlapIsRejected(t *testing.T) {
	root := t.TempDir()
	runner := testRunner(t, root)
	locksDir, _, err := config.EnsureStateDirs(runner.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	lock, acquired, err := runlock.Acquire(locksDir, runner.ProfileName)
	if err != nil || !acquired {
		t.Fatalf("acquire external lock: acquired=%v err=%v", acquired, err)
	}
	defer lock.Release()

	_, err = runner.Run(context.Background(), Options{})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("expected ErrAlreadyRunning, got %v", err)
	}
}

func TestLowDiskSpaceFailsBeforeDump(t *testing.T) {
	root := t.TempDir()
	runner := testRunner(t, root)
	runner.Profile.Backup.MinimumFreeSpaceBytes = math.MaxInt64
	dumpCalled := false
	runner.Dump = func(context.Context, mysql.ToolInfo, config.Database, string, io.Writer) (mysql.DumpResult, error) {
		dumpCalled = true
		return mysql.DumpResult{}, nil
	}

	_, err := runner.Run(context.Background(), Options{})
	if err == nil || !strings.Contains(err.Error(), "configured minimum") {
		t.Fatalf("expected disk-space failure, got %v", err)
	}
	if dumpCalled {
		t.Fatal("dump started despite insufficient disk space")
	}
}

func TestReadinessRetriesTransientUnixSocketFailure(t *testing.T) {
	root := t.TempDir()
	runner := testRunner(t, root)
	attempts := 0
	runner.CheckConnection = func(context.Context, mysql.ToolInfo, config.Database, string) error {
		attempts++
		if attempts < 3 {
			return errors.New("Got error: 2002: Can't connect to local server through socket")
		}
		return nil
	}
	runner.Dump = func(_ context.Context, _ mysql.ToolInfo, _ config.Database, _ string, destination io.Writer) (mysql.DumpResult, error) {
		_, err := destination.Write([]byte("CREATE TABLE ready (id BIGINT);\n"))
		return mysql.DumpResult{Bytes: 32}, err
	}
	outcome, err := runner.Run(context.Background(), Options{
		ReadinessTimeout:      500 * time.Millisecond,
		ReadinessRetryDelay:   time.Millisecond,
		ReadinessProbeTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("readiness retry failed: %v", err)
	}
	if attempts != 3 || outcome.Record.ReadinessAttempts != 3 {
		t.Fatalf("attempts = %d, record = %d", attempts, outcome.Record.ReadinessAttempts)
	}
}

func TestReadinessRetriesTransientTCPFailure(t *testing.T) {
	root := t.TempDir()
	runner := testRunner(t, root)
	attempts := 0
	runner.CheckConnection = func(context.Context, mysql.ToolInfo, config.Database, string) error {
		attempts++
		if attempts < 2 {
			return errors.New("Got error: 2003: Can't connect to MySQL server (111)")
		}
		return nil
	}
	runner.Dump = func(_ context.Context, _ mysql.ToolInfo, _ config.Database, _ string, destination io.Writer) (mysql.DumpResult, error) {
		_, err := destination.Write([]byte("CREATE TABLE ready (id BIGINT);\n"))
		return mysql.DumpResult{Bytes: 32}, err
	}
	if _, err := runner.Run(context.Background(), Options{
		ReadinessTimeout:      500 * time.Millisecond,
		ReadinessRetryDelay:   time.Millisecond,
		ReadinessProbeTimeout: 50 * time.Millisecond,
	}); err != nil {
		t.Fatalf("TCP readiness retry failed: %v", err)
	}
}

func TestReadinessFailsFastForPermanentErrors(t *testing.T) {
	for _, message := range []string{
		"ERROR 1045 (28000): Access denied for user",
		"ERROR 1044 (42000): Access denied for user to database",
		"ERROR 1049 (42000): Unknown database 'missing'",
		"ERROR 1142 (42000): SELECT command denied",
	} {
		t.Run(message, func(t *testing.T) {
			root := t.TempDir()
			runner := testRunner(t, root)
			attempts := 0
			runner.CheckConnection = func(context.Context, mysql.ToolInfo, config.Database, string) error {
				attempts++
				return errors.New(message)
			}
			dumpCalled := false
			runner.Dump = func(context.Context, mysql.ToolInfo, config.Database, string, io.Writer) (mysql.DumpResult, error) {
				dumpCalled = true
				return mysql.DumpResult{}, nil
			}
			_, err := runner.Run(context.Background(), Options{
				ReadinessTimeout:      500 * time.Millisecond,
				ReadinessRetryDelay:   time.Millisecond,
				ReadinessProbeTimeout: 50 * time.Millisecond,
			})
			if err == nil || attempts != 1 || dumpCalled {
				t.Fatalf("permanent error was retried or dumped: attempts=%d dump=%v err=%v", attempts, dumpCalled, err)
			}
		})
	}
}

func TestReadinessTimeoutPublishesNoArchive(t *testing.T) {
	root := t.TempDir()
	runner := testRunner(t, root)
	runner.CheckConnection = func(context.Context, mysql.ToolInfo, config.Database, string) error {
		return errors.New("Got error: 2002: Can't connect to local server through socket")
	}
	runner.Dump = func(context.Context, mysql.ToolInfo, config.Database, string, io.Writer) (mysql.DumpResult, error) {
		t.Fatal("dump started before readiness timeout")
		return mysql.DumpResult{}, nil
	}
	_, err := runner.Run(context.Background(), Options{
		ReadinessTimeout:      40 * time.Millisecond,
		ReadinessRetryDelay:   5 * time.Millisecond,
		ReadinessProbeTimeout: 5 * time.Millisecond,
	})
	if !errors.Is(err, ErrReadinessTimeout) {
		t.Fatalf("expected readiness timeout, got %v", err)
	}
	entries, readErr := os.ReadDir(runner.Profile.Backup.Directory)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("readiness timeout left archive artifacts: %v", entries)
	}
}

func TestReadinessCancellationIsInterrupted(t *testing.T) {
	root := t.TempDir()
	runner := testRunner(t, root)
	started := make(chan struct{})
	runner.CheckConnection = func(ctx context.Context, _ mysql.ToolInfo, _ config.Database, _ string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	outcome, err := runner.Run(ctx, Options{
		ReadinessTimeout:      500 * time.Millisecond,
		ReadinessRetryDelay:   time.Millisecond,
		ReadinessProbeTimeout: 100 * time.Millisecond,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if outcome.Record.Status != history.StatusInterrupted {
		t.Fatalf("status = %s, want interrupted", outcome.Record.Status)
	}
}

func TestReadinessCancellationDuringRetryWait(t *testing.T) {
	runner := testRunner(t, t.TempDir())
	attempted := make(chan struct{})
	runner.CheckConnection = func(context.Context, mysql.ToolInfo, config.Database, string) error {
		close(attempted)
		return errors.New("Got error: 2002: Can't connect to local server through socket")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, Options{
			ReadinessTimeout:      3 * time.Second,
			ReadinessRetryDelay:   time.Second,
			ReadinessProbeTimeout: 100 * time.Millisecond,
		})
		finished <- err
	}()
	select {
	case <-attempted:
	case <-time.After(2 * time.Second):
		t.Fatal("readiness probe did not start")
	}
	// Cancel while the retry timer is pending, not while the probe is running.
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation during retry backoff, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readiness cancellation hung while stopping retry timer")
	}
}

func testRunner(t *testing.T, root string) *Runner {
	t.Helper()
	profile := config.Profile{
		Database: config.Database{
			Engine:            "mysql",
			DumpTool:          "auto",
			ClientTool:        "auto",
			Host:              "127.0.0.1",
			Port:              3306,
			Username:          "backup",
			Database:          "fxserver",
			SingleTransaction: true,
		},
		Backup: config.Backup{
			Directory:        filepath.Join(root, "backups"),
			CompressionLevel: 1,
		},
	}
	runner := New("production", profile, "secret", filepath.Join(root, "state"))
	runner.Discover = func(context.Context, config.Database) (mysql.Tools, error) {
		tool := mysql.ToolInfo{Path: "mariadb-dump", Version: "test-version"}
		return mysql.Tools{Dump: tool, Client: mysql.ToolInfo{Path: "mariadb", Version: "test-version"}}, nil
	}
	runner.CheckConnection = func(context.Context, mysql.ToolInfo, config.Database, string) error {
		return nil
	}
	return runner
}
