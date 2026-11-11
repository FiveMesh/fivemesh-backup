package history

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAppendAndReadExecutionHistory(t *testing.T) {
	root := t.TempDir()
	store := New(root, filepath.Join(root, "logs"))
	record := Record{
		ID:        "run-1",
		Profile:   "production",
		Status:    StatusSuccess,
		Phase:     "complete",
		StartedAt: time.Now().UTC(),
	}
	if err := store.Append(record); err != nil {
		t.Fatal(err)
	}
	records, warnings, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(records) != 1 || records[0].ID != record.ID {
		t.Fatalf("unexpected records: %#v", records)
	}
	if runtime.GOOS != "windows" && runtimeMode(t, store.path) != 0o600 {
		t.Fatalf("history file is not mode 0600")
	}
}

func TestRecoverInterruptedAppendsTerminalRecordWhenInactive(t *testing.T) {
	root := t.TempDir()
	store := New(root, filepath.Join(root, "logs"))
	started := time.Now().UTC().Add(-time.Hour)
	running := Record{
		ID:        "crashed-run",
		Profile:   "production",
		Status:    StatusRunning,
		Phase:     "dumping",
		StartedAt: started,
	}
	if err := store.Append(running); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.RecoverInterrupted(
		"production",
		func() (bool, error) { return false, nil },
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].Status != StatusInterrupted || !recovered[0].Recovered {
		t.Fatalf("unexpected recovery result: %#v", recovered)
	}
	records, _, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Status != StatusInterrupted {
		t.Fatalf("unexpected effective history: %#v", records)
	}
	data, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "\n") != 2 {
		t.Fatalf("original record was not preserved: %s", data)
	}
}

func TestRecoverInterruptedDoesNotTouchActiveRun(t *testing.T) {
	root := t.TempDir()
	store := New(root, filepath.Join(root, "logs"))
	running := Record{
		ID:        "long-running",
		Profile:   "production",
		Status:    StatusRunning,
		Phase:     "dumping",
		StartedAt: time.Now().UTC().Add(-72 * time.Hour),
	}
	if err := store.Append(running); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.RecoverInterrupted(
		"production",
		func() (bool, error) { return true, nil },
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 0 {
		t.Fatalf("active long-running job was recovered: %#v", recovered)
	}
}

func TestConcurrentHistoryAppendsAreSerialized(t *testing.T) {
	root := t.TempDir()
	store := New(root, filepath.Join(root, "logs"))
	var wait sync.WaitGroup
	errs := make(chan error, 20)
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			errs <- store.Append(Record{
				ID:        "run-" + strconv.Itoa(index),
				Profile:   "profile-" + strconv.Itoa(index%2),
				Status:    StatusFailed,
				Phase:     "test",
				StartedAt: time.Now().UTC(),
			})
		}(index)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	records, _, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 20 {
		t.Fatalf("read %d records, want 20", len(records))
	}
}

func runtimeMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
