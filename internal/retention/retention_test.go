package retention

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/archive"
	"github.com/FiveMesh/fivemesh-backup/internal/config"
)

func TestKeepLastDeletesOnlyOlderProfileArchives(t *testing.T) {
	directory := t.TempDir()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	createArchive(t, directory, "production", "production-old", now.Add(-72*time.Hour), 4096)
	createArchive(t, directory, "production", "production-middle", now.Add(-48*time.Hour), 4096)
	createArchive(t, directory, "production", "production-new", now.Add(-24*time.Hour), 4096)
	createArchive(t, directory, "staging", "staging-old", now.Add(-96*time.Hour), 4096)
	unrelated := filepath.Join(directory, "do-not-delete.txt")
	if err := os.WriteFile(unrelated, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := BuildPlan(directory, "production", config.Retention{KeepLast: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ToDelete) != 1 || filepath.Base(plan.ToDelete[0].Path) != "production-old.sql.zst" {
		t.Fatalf("unexpected retention plan: %#v", plan)
	}
	deleted, err := Execute(directory, "production", plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 {
		t.Fatalf("deleted %d archives, want 1", len(deleted))
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated file was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "staging-old.sql.zst")); err != nil {
		t.Fatalf("other profile archive was removed: %v", err)
	}
}

func TestMaximumBytesKeepsNewestArchive(t *testing.T) {
	directory := t.TempDir()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	result := createArchive(t, directory, "production", "only", now, 1024)
	plan, err := BuildPlan(directory, "production", config.Retention{MaximumBytes: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ToDelete) != 0 {
		t.Fatalf("only known successful archive was selected: %#v", plan)
	}
	if len(plan.Warnings) == 0 {
		t.Fatal("expected warning that the protected archive exceeds the cap")
	}
	if _, err := os.Stat(result.Path); err != nil {
		t.Fatalf("newest archive missing: %v", err)
	}
}

func TestMaximumBytesDeletesOldestUntilUnderCap(t *testing.T) {
	directory := t.TempDir()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	createArchive(t, directory, "production", "old", now.Add(-72*time.Hour), 8192)
	middle := createArchive(t, directory, "production", "middle", now.Add(-48*time.Hour), 8192)
	newest := createArchive(t, directory, "production", "newest", now.Add(-24*time.Hour), 8192)
	capBytes := middle.Metadata.SizeBytes + newest.Metadata.SizeBytes

	plan, err := BuildPlan(directory, "production", config.Retention{MaximumBytes: capBytes}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ToDelete) != 1 || filepath.Base(plan.ToDelete[0].Path) != "old.sql.zst" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if plan.ProjectedBytes > capBytes {
		t.Fatalf("projected bytes %d exceed cap %d", plan.ProjectedBytes, capBytes)
	}
	if _, err := os.Stat(middle.Path); err != nil {
		t.Fatalf("middle archive missing: %v", err)
	}
}

func createArchive(
	t *testing.T,
	directory, profile, name string,
	created time.Time,
	contentSize int,
) archive.Result {
	t.Helper()
	path := filepath.Join(directory, name+".sql.zst")
	builder, err := archive.NewBuilder(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builder.Write(make([]byte, contentSize)); err != nil {
		t.Fatal(err)
	}
	result, err := builder.Commit(archive.Metadata{
		RunID:            name,
		Profile:          profile,
		Database:         "fxserver",
		Engine:           "mysql",
		CreatedAt:        created,
		CompressionLevel: 1,
		DumpTool:         "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
