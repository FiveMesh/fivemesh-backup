package archive

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveRoundTripAndVerification(t *testing.T) {
	path, err := NewPath(t.TempDir(), "production", time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewBuilder(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("CREATE TABLE example (id BIGINT);\n"), 100_000)
	if _, err := builder.Write(content); err != nil {
		t.Fatal(err)
	}
	result, err := builder.Commit(Metadata{
		RunID:            "run-1",
		Profile:          "production",
		Database:         "fxserver",
		Engine:           "mysql",
		CompressionLevel: 3,
		DumpTool:         "mariadb-dump",
	})
	if err != nil {
		t.Fatalf("commit archive: %v", err)
	}
	if result.Metadata.SHA256 == "" || result.Metadata.SizeBytes <= 0 {
		t.Fatal("archive result is missing integrity metadata")
	}
	if result.Metadata.UncompressedSize != int64(len(content)) {
		t.Fatalf("uncompressed size = %d, want %d", result.Metadata.UncompressedSize, len(content))
	}

	verification, err := Verify(result.Path)
	if err != nil {
		t.Fatalf("verify archive: %v", err)
	}
	if verification.Metadata.RunID != "run-1" {
		t.Fatalf("unexpected run ID %q", verification.Metadata.RunID)
	}
}

func TestVerificationDetectsCorruption(t *testing.T) {
	directory := t.TempDir()
	path, err := NewPath(directory, "production", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewBuilder(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builder.Write(bytes.Repeat([]byte("backup data\n"), 10_000)); err != nil {
		t.Fatal(err)
	}
	result, err := builder.Commit(Metadata{
		RunID:            "run-corrupt",
		Profile:          "production",
		Database:         "fxserver",
		Engine:           "mysql",
		CompressionLevel: 3,
	})
	if err != nil {
		t.Fatal(err)
	}

	file, err := os.OpenFile(result.Path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{0}, result.Metadata.SizeBytes/2); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(result.Path); err == nil {
		t.Fatal("corrupted archive passed verification")
	}
}

func TestAbortDoesNotPublishArchive(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "production-test.sql.zst")
	builder, err := NewBuilder(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builder.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	builder.Abort()

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("aborted archive was published: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary files remain after abort: %v", entries)
	}
}

func TestCleanupPartialsIsProfileScoped(t *testing.T) {
	directory := t.TempDir()
	productionPartial := filepath.Join(directory, ".production-20261020T060000Z-abc.sql.zst.partial-123")
	stagingPartial := filepath.Join(directory, ".staging-20261020T060000Z-def.sql.zst.partial-456")
	for _, path := range []string{productionPartial, stagingPartial} {
		if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := CleanupPartials(directory, "production")
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != productionPartial {
		t.Fatalf("unexpected cleanup result: %v", removed)
	}
	if _, err := os.Stat(stagingPartial); err != nil {
		t.Fatalf("other profile partial was removed: %v", err)
	}
}
