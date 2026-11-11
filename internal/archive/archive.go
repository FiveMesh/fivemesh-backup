package archive

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

const SchemaVersion = 1

type Metadata struct {
	SchemaVersion    int                 `json:"schema_version"`
	Complete         bool                `json:"complete"`
	RunID            string              `json:"run_id"`
	Profile          string              `json:"profile"`
	Database         string              `json:"database"`
	Engine           string              `json:"engine"`
	CreatedAt        time.Time           `json:"created_at"`
	Compression      string              `json:"compression"`
	CompressionLevel int                 `json:"compression_level"`
	SHA256           string              `json:"sha256"`
	SizeBytes        int64               `json:"size_bytes"`
	UncompressedSize int64               `json:"uncompressed_size_bytes"`
	DumpTool         string              `json:"dump_tool"`
	DumpToolVersion  string              `json:"dump_tool_version"`
	Consistency      ConsistencyMetadata `json:"consistency"`
}

type ConsistencyMetadata struct {
	SingleTransaction bool     `json:"single_transaction"`
	Warnings          []string `json:"warnings,omitempty"`
}

type Result struct {
	Path            string
	MetadataPath    string
	ChecksumPath    string
	Metadata        Metadata
	UncompressedLen int64
}

type Builder struct {
	finalPath    string
	tempPath     string
	file         *os.File
	encoder      *zstd.Encoder
	hasher       hash.Hash
	compressed   *countingWriter
	uncompressed int64
	closed       bool
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (w *countingWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.count += int64(n)
	return n, err
}

type Verification struct {
	Path     string
	Metadata Metadata
}

type ScanWarning struct {
	Path string
	Err  error
}

type ScannedArchive struct {
	Path     string
	Metadata Metadata
}

func NewPath(directory, profile string, now time.Time) (string, error) {
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generate archive suffix: %w", err)
	}
	name := fmt.Sprintf(
		"%s-%s-%s.sql.zst",
		filenamePart(profile),
		now.UTC().Format("20060102T150405Z"),
		hex.EncodeToString(suffix),
	)
	return filepath.Join(directory, name), nil
}

func NewBuilder(finalPath string, compressionLevel int) (*Builder, error) {
	directory := filepath.Dir(finalPath)
	temp, err := os.CreateTemp(directory, "."+filepath.Base(finalPath)+".partial-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary archive: %w", err)
	}
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		_ = os.Remove(temp.Name())
		return nil, fmt.Errorf("restrict temporary archive: %w", err)
	}

	hasher := sha256.New()
	output := &countingWriter{writer: io.MultiWriter(temp, hasher)}
	encoder, err := zstd.NewWriter(
		output,
		zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(compressionLevel)),
		zstd.WithEncoderConcurrency(1),
	)
	if err != nil {
		_ = temp.Close()
		_ = os.Remove(temp.Name())
		return nil, fmt.Errorf("create Zstandard encoder: %w", err)
	}
	return &Builder{
		finalPath:  finalPath,
		tempPath:   temp.Name(),
		file:       temp,
		encoder:    encoder,
		hasher:     hasher,
		compressed: output,
	}, nil
}

func (b *Builder) Write(data []byte) (int, error) {
	n, err := b.encoder.Write(data)
	b.uncompressed += int64(n)
	return n, err
}

func (b *Builder) Commit(metadata Metadata) (Result, error) {
	if b.closed {
		return Result{}, errors.New("archive builder is already closed")
	}
	if err := b.encoder.Close(); err != nil {
		b.Abort()
		return Result{}, fmt.Errorf("finish Zstandard stream: %w", err)
	}
	b.closed = true
	if err := b.file.Sync(); err != nil {
		b.removeTemporary()
		return Result{}, fmt.Errorf("synchronize archive: %w", err)
	}
	if err := b.file.Close(); err != nil {
		b.removeTemporary()
		return Result{}, fmt.Errorf("close archive: %w", err)
	}

	metadata.SchemaVersion = SchemaVersion
	metadata.Complete = true
	metadata.Compression = "zstd"
	metadata.SHA256 = hex.EncodeToString(b.hasher.Sum(nil))
	metadata.SizeBytes = b.compressed.count
	metadata.UncompressedSize = b.uncompressed
	if metadata.CreatedAt.IsZero() {
		metadata.CreatedAt = time.Now().UTC()
	}
	if err := validateMetadata(metadata); err != nil {
		b.removeTemporary()
		return Result{}, err
	}

	checksumPath := b.finalPath + ".sha256"
	metadataPath := b.finalPath + ".json"
	checksum := []byte(fmt.Sprintf("%s  %s\n", metadata.SHA256, filepath.Base(b.finalPath)))
	checksumTemp, err := writeTemporarySidecar(checksumPath, checksum)
	if err != nil {
		b.removeTemporary()
		return Result{}, err
	}
	metadataJSON, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		b.removeTemporary()
		_ = os.Remove(checksumTemp)
		return Result{}, fmt.Errorf("encode archive metadata: %w", err)
	}
	metadataJSON = append(metadataJSON, '\n')
	metadataTemp, err := writeTemporarySidecar(metadataPath, metadataJSON)
	if err != nil {
		_ = os.Remove(checksumTemp)
		b.removeTemporary()
		return Result{}, err
	}

	if err := renameNew(b.tempPath, b.finalPath); err != nil {
		_ = os.Remove(checksumTemp)
		_ = os.Remove(metadataTemp)
		b.removeTemporary()
		return Result{}, fmt.Errorf("publish archive: %w", err)
	}
	if err := renameNew(checksumTemp, checksumPath); err != nil {
		_ = os.Remove(b.finalPath)
		_ = os.Remove(metadataTemp)
		return Result{}, fmt.Errorf("publish checksum: %w", err)
	}
	if err := renameNew(metadataTemp, metadataPath); err != nil {
		_ = os.Remove(b.finalPath)
		_ = os.Remove(checksumPath)
		return Result{}, fmt.Errorf("publish archive metadata: %w", err)
	}
	if err := syncDirectory(filepath.Dir(b.finalPath)); err != nil {
		_ = os.Remove(b.finalPath)
		_ = os.Remove(checksumPath)
		_ = os.Remove(metadataPath)
		return Result{}, fmt.Errorf("synchronize archive directory: %w", err)
	}

	return Result{
		Path:            b.finalPath,
		MetadataPath:    metadataPath,
		ChecksumPath:    checksumPath,
		Metadata:        metadata,
		UncompressedLen: b.uncompressed,
	}, nil
}

func (b *Builder) Abort() {
	if b.closed {
		return
	}
	b.closed = true
	if b.encoder != nil {
		b.encoder.Close()
	}
	if b.file != nil {
		_ = b.file.Close()
	}
	b.removeTemporary()
}

func (b *Builder) removeTemporary() {
	_ = os.Remove(b.tempPath)
}

func Verify(path string) (Verification, error) {
	metadata, err := ReadMetadata(path)
	if err != nil {
		return Verification{}, err
	}
	if !metadata.Complete {
		return Verification{}, errors.New("archive metadata is not marked complete")
	}
	if metadata.SchemaVersion != SchemaVersion {
		return Verification{}, fmt.Errorf("unsupported archive metadata schema %d", metadata.SchemaVersion)
	}

	checksumData, err := os.ReadFile(path + ".sha256")
	if err != nil {
		return Verification{}, fmt.Errorf("read checksum file: %w", err)
	}
	checksumFields := strings.Fields(string(checksumData))
	if len(checksumFields) < 1 {
		return Verification{}, errors.New("checksum file is empty or malformed")
	}
	if !strings.EqualFold(checksumFields[0], metadata.SHA256) {
		return Verification{}, errors.New("checksum file does not match archive metadata")
	}

	file, err := os.Open(path)
	if err != nil {
		return Verification{}, fmt.Errorf("open archive: %w", err)
	}
	defer file.Close()

	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return Verification{}, fmt.Errorf("hash archive: %w", err)
	}
	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actualHash, metadata.SHA256) {
		return Verification{}, errors.New("archive SHA-256 does not match metadata")
	}
	if size != metadata.SizeBytes {
		return Verification{}, fmt.Errorf("archive size is %d bytes; metadata records %d", size, metadata.SizeBytes)
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Verification{}, fmt.Errorf("rewind archive: %w", err)
	}
	decoder, err := zstd.NewReader(file, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return Verification{}, fmt.Errorf("open Zstandard stream: %w", err)
	}
	decoded, decodeErr := io.Copy(io.Discard, decoder)
	decoder.Close()
	if decodeErr != nil {
		return Verification{}, fmt.Errorf("decode Zstandard stream: %w", decodeErr)
	}
	if decoded != metadata.UncompressedSize {
		return Verification{}, fmt.Errorf("decoded %d bytes; metadata records %d", decoded, metadata.UncompressedSize)
	}

	return Verification{Path: path, Metadata: metadata}, nil
}

func ReadMetadata(path string) (Metadata, error) {
	data, err := os.ReadFile(path + ".json")
	if err != nil {
		return Metadata{}, fmt.Errorf("read archive metadata: %w", err)
	}
	var metadata Metadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return Metadata{}, fmt.Errorf("decode archive metadata: %w", err)
	}
	return metadata, nil
}

func Scan(directory string) ([]ScannedArchive, []ScanWarning, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, nil, fmt.Errorf("read archive directory: %w", err)
	}
	var archives []ScannedArchive
	var warnings []ScanWarning
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql.zst.json") {
			continue
		}
		archivePath := filepath.Join(directory, strings.TrimSuffix(entry.Name(), ".json"))
		metadata, err := ReadMetadata(archivePath)
		if err != nil {
			warnings = append(warnings, ScanWarning{Path: archivePath, Err: err})
			continue
		}
		archives = append(archives, ScannedArchive{Path: archivePath, Metadata: metadata})
	}
	sort.Slice(archives, func(i, j int) bool {
		return archives[i].Metadata.CreatedAt.After(archives[j].Metadata.CreatedAt)
	})
	return archives, warnings, nil
}

func MetadataPath(archivePath string) string {
	return archivePath + ".json"
}

func CleanupPartials(directory, profile string) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read backup directory for partial cleanup: %w", err)
	}
	prefix := "." + filenamePart(profile) + "-"
	var removed []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) || !strings.Contains(entry.Name(), ".partial-") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, fmt.Errorf("remove interrupted archive partial %q: %w", entry.Name(), err)
		}
		removed = append(removed, path)
	}
	return removed, nil
}

func Remove(path string) error {
	for _, candidate := range []string{path + ".json", path + ".sha256", path} {
		if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %q: %w", filepath.Base(candidate), err)
		}
	}
	return nil
}

func writeTemporarySidecar(finalPath string, data []byte) (string, error) {
	temp, err := os.CreateTemp(filepath.Dir(finalPath), "."+filepath.Base(finalPath)+".partial-*")
	if err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Base(finalPath), err)
	}
	tempPath := temp.Name()
	complete := false
	defer func() {
		_ = temp.Close()
		if !complete {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := temp.Write(data); err != nil {
		return "", err
	}
	if err := temp.Sync(); err != nil {
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	complete = true
	return tempPath, nil
}

func renameNew(oldPath, newPath string) error {
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("%q already exists", newPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(oldPath, newPath)
}

func validateMetadata(metadata Metadata) error {
	if strings.TrimSpace(metadata.RunID) == "" {
		return errors.New("archive run ID is required")
	}
	if strings.TrimSpace(metadata.Profile) == "" {
		return errors.New("archive profile is required")
	}
	if strings.TrimSpace(metadata.Database) == "" {
		return errors.New("archive database is required")
	}
	if metadata.SizeBytes <= 0 {
		return errors.New("archive size must be positive")
	}
	return nil
}

func filenamePart(value string) string {
	var builder strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z':
			builder.WriteRune(char)
		case char >= 'A' && char <= 'Z':
			builder.WriteRune(char)
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
		case char == '-', char == '_', char == '.':
			builder.WriteRune(char)
		default:
			builder.WriteByte('-')
		}
	}
	result := strings.Trim(builder.String(), "-.")
	if result == "" {
		return "backup"
	}
	return result
}
