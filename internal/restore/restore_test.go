package restore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/archive"
	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/mysql"
)

func TestRestoreVerifiesAndStreamsArchive(t *testing.T) {
	path := createTestArchive(t)
	runner := testRunner()
	var restored string
	runner.Restore = func(_ context.Context, _ mysql.ToolInfo, _ config.Database, _ string, target string, source io.Reader) error {
		data, err := io.ReadAll(source)
		if err != nil {
			return err
		}
		restored = target + ":" + string(data)
		return nil
	}

	metadata, err := runner.Run(context.Background(), path, "restore_target", Options{})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if metadata.Database != "fxserver" {
		t.Fatalf("unexpected source database %q", metadata.Database)
	}
	if restored != "restore_target:CREATE TABLE players (id BIGINT);\n" {
		t.Fatalf("unexpected restored stream %q", restored)
	}
}

func TestRestoreRejectsWrongProfile(t *testing.T) {
	path := createTestArchive(t)
	runner := testRunner()
	runner.ProfileName = "staging"
	runner.Profile.Backup.Directory = filepath.Join(t.TempDir(), "backups")
	_, err := runner.Run(context.Background(), path, "restore_target", Options{})
	if err == nil {
		t.Fatal("expected profile mismatch")
	}
}

func TestRestoreDoesNotRunAfterVerificationFailure(t *testing.T) {
	path := createTestArchive(t)
	checksumPath := path + ".sha256"
	if err := writeFile(checksumPath, []byte("0000  wrong\n")); err != nil {
		t.Fatal(err)
	}
	runner := testRunner()
	called := false
	runner.Restore = func(context.Context, mysql.ToolInfo, config.Database, string, string, io.Reader) error {
		called = true
		return errors.New("must not run")
	}
	_, err := runner.Run(context.Background(), path, "restore_target", Options{})
	if err == nil {
		t.Fatal("expected verification failure")
	}
	if called {
		t.Fatal("restore client ran after archive verification failed")
	}
}

func testRunner() *Runner {
	profile := config.Profile{
		Database: config.Database{
			Engine:     "mysql",
			ClientTool: "auto",
			Host:       "127.0.0.1",
			Port:       3306,
			Username:   "backup",
			Database:   "fxserver",
		},
	}
	runner := New("production", profile, "secret")
	runner.DiscoverClient = func(context.Context, config.Database) (mysql.ToolInfo, error) {
		return mysql.ToolInfo{Path: "mariadb", Version: "test"}, nil
	}
	return runner
}

func createTestArchive(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "production-test.sql.zst")
	builder, err := archive.NewBuilder(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builder.Write([]byte("CREATE TABLE players (id BIGINT);\n")); err != nil {
		t.Fatal(err)
	}
	result, err := builder.Commit(archive.Metadata{
		RunID:            "restore-test",
		Profile:          "production",
		Database:         "fxserver",
		Engine:           "mysql",
		CreatedAt:        time.Now().UTC(),
		CompressionLevel: 1,
		DumpTool:         "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Path
}

func writeFile(path string, data []byte) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(data)
	return err
}
