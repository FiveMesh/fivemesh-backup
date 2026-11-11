package history

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

type Status string

const (
	StatusRunning     Status = "running"
	StatusSuccess     Status = "success"
	StatusFailed      Status = "failed"
	StatusInterrupted Status = "interrupted"
)

type Record struct {
	ID                  string    `json:"id"`
	Profile             string    `json:"profile"`
	Status              Status    `json:"status"`
	Phase               string    `json:"phase"`
	StartedAt           time.Time `json:"started_at"`
	FinishedAt          time.Time `json:"finished_at,omitempty"`
	DurationMS          int64     `json:"duration_ms,omitempty"`
	Archive             string    `json:"archive,omitempty"`
	ArchiveSHA256       string    `json:"archive_sha256,omitempty"`
	ArchiveSize         int64     `json:"archive_size_bytes,omitempty"`
	UncompressedSize    int64     `json:"uncompressed_size_bytes,omitempty"`
	Verified            bool      `json:"verified"`
	ReadinessAttempts   int       `json:"readiness_attempts,omitempty"`
	ReadinessWaitMS     int64     `json:"readiness_wait_ms,omitempty"`
	DumpTool            string    `json:"dump_tool,omitempty"`
	DumpToolVersion     string    `json:"dump_tool_version,omitempty"`
	DumpExitCode        int       `json:"dump_exit_code,omitempty"`
	Error               string    `json:"error,omitempty"`
	RetentionDeleted    int       `json:"retention_deleted,omitempty"`
	RetentionFreedBytes int64     `json:"retention_freed_bytes,omitempty"`
	RetentionError      string    `json:"retention_error,omitempty"`
	Recovered           bool      `json:"recovered,omitempty"`
}

type Store struct {
	stateDir string
	logsDir  string
	path     string
}

type ReadWarning struct {
	Line int
	Err  error
}

func New(stateDir, logsDir string) *Store {
	return &Store{
		stateDir: stateDir,
		logsDir:  logsDir,
		path:     filepath.Join(stateDir, "history.jsonl"),
	}
}

func NewRunID(now time.Time) (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate run ID: %w", err)
	}
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(random), nil
}

func (s *Store) StartLog(runID string) (*os.File, error) {
	if err := os.MkdirAll(s.logsDir, 0o700); err != nil {
		return nil, fmt.Errorf("create logs directory: %w", err)
	}
	path := s.LogPath(runID)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create run log: %w", err)
	}
	return file, nil
}

func (s *Store) Append(record Record) error {
	if err := os.MkdirAll(s.stateDir, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	lock := flock.New(s.path + ".lock")
	lockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockContext, 10*time.Millisecond)
	if err != nil {
		return fmt.Errorf("lock execution history: %w", err)
	}
	if !locked {
		return errors.New("execution history is busy")
	}
	defer lock.Unlock()

	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open execution history: %w", err)
	}
	defer file.Close()
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode execution history: %w", err)
	}
	data = append(data, '\n')
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("append execution history: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("synchronize execution history: %w", err)
	}
	return nil
}

func (s *Store) Read() ([]Record, []ReadWarning, error) {
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("open execution history: %w", err)
	}
	defer file.Close()

	var records []Record
	var warnings []ReadWarning
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			warnings = append(warnings, ReadWarning{Line: lineNumber, Err: err})
			continue
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, warnings, fmt.Errorf("read execution history: %w", err)
	}
	byID := make(map[string]Record, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	latest := make([]Record, 0, len(byID))
	for _, record := range byID {
		latest = append(latest, record)
	}
	sort.Slice(latest, func(i, j int) bool {
		return latest[i].StartedAt.Before(latest[j].StartedAt)
	})
	return latest, warnings, nil
}

func (s *Store) ReadLog(runID string) ([]byte, error) {
	data, err := os.ReadFile(s.LogPath(runID))
	if err != nil {
		return nil, fmt.Errorf("read run log %q: %w", runID, err)
	}
	return data, nil
}

func (s *Store) LogPath(runID string) string {
	return filepath.Join(s.logsDir, runID+".log")
}

// RecoverInterrupted appends terminal records for runs whose profile is no
// longer locked. The original records remain in the append-only JSONL file.
// Callers must not infer abandonment from age alone.
func (s *Store) RecoverInterrupted(
	profile string,
	isActive func() (bool, error),
	now time.Time,
) ([]Record, error) {
	records, _, err := s.Read()
	if err != nil {
		return nil, err
	}
	var abandoned []Record
	for _, record := range records {
		if record.Profile == profile && record.Status == StatusRunning {
			abandoned = append(abandoned, record)
		}
	}
	if len(abandoned) == 0 {
		return nil, nil
	}
	active, err := isActive()
	if err != nil {
		return nil, fmt.Errorf("check profile liveness: %w", err)
	}
	if active {
		return nil, nil
	}

	recovered := make([]Record, 0, len(abandoned))
	for _, previous := range abandoned {
		record := previous
		record.Status = StatusInterrupted
		record.Phase = "interrupted"
		record.FinishedAt = now.UTC()
		record.DurationMS = record.FinishedAt.Sub(record.StartedAt).Milliseconds()
		record.Error = "execution ended unexpectedly before a terminal state was recorded; profile lock is no longer held"
		record.Recovered = true
		if err := s.Append(record); err != nil {
			return recovered, err
		}
		recovered = append(recovered, record)
	}
	return recovered, nil
}
