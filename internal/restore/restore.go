package restore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/archive"
	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/mysql"
	"github.com/klauspost/compress/zstd"
)

type Progress struct {
	UncompressedBytes int64
	Elapsed           time.Duration
}

type Options struct {
	Timeout      time.Duration
	Verification *archive.Verification
	OnPhase      func(string)
	OnProgress   func(Progress)
}

type Runner struct {
	ProfileName string
	Profile     config.Profile
	Password    string

	DiscoverClient func(context.Context, config.Database) (mysql.ToolInfo, error)
	Restore        func(context.Context, mysql.ToolInfo, config.Database, string, string, io.Reader) error
}

func New(profileName string, profile config.Profile, password string) *Runner {
	return &Runner{
		ProfileName:    profileName,
		Profile:        profile,
		Password:       password,
		DiscoverClient: mysql.DiscoverClient,
		Restore:        mysql.Restore,
	}
}

func (r *Runner) Run(ctx context.Context, archivePath, targetDatabase string, options Options) (archive.Metadata, error) {
	if r.DiscoverClient == nil {
		r.DiscoverClient = mysql.DiscoverClient
	}
	if r.Restore == nil {
		r.Restore = mysql.Restore
	}
	setPhase := func(phase string) {
		if options.OnPhase != nil {
			options.OnPhase(phase)
		}
	}

	verification := archive.Verification{}
	if options.Verification != nil {
		verification = *options.Verification
		if verification.Path != archivePath {
			return archive.Metadata{}, errors.New("pre-verified archive path does not match requested archive")
		}
	} else {
		setPhase("verifying-archive")
		var err error
		verification, err = archive.Verify(archivePath)
		if err != nil {
			return archive.Metadata{}, fmt.Errorf("refusing to restore an archive that failed verification: %w", err)
		}
	}
	if verification.Metadata.Profile != r.ProfileName {
		return archive.Metadata{}, fmt.Errorf(
			"archive belongs to profile %q, not %q",
			verification.Metadata.Profile,
			r.ProfileName,
		)
	}

	setPhase("discovering-client")
	client, err := r.DiscoverClient(ctx, r.Profile.Database)
	if err != nil {
		return archive.Metadata{}, err
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return archive.Metadata{}, fmt.Errorf("open verified archive: %w", err)
	}
	defer file.Close()
	decoder, err := zstd.NewReader(file, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return archive.Metadata{}, fmt.Errorf("open archive Zstandard stream: %w", err)
	}
	defer decoder.Close()

	executionContext := ctx
	if options.Timeout > 0 {
		var cancel context.CancelFunc
		executionContext, cancel = context.WithTimeout(ctx, options.Timeout)
		defer cancel()
	}

	setPhase("restoring")
	started := time.Now()
	source := &progressReader{
		source:   decoder,
		started:  started,
		callback: options.OnProgress,
	}
	if err := r.Restore(
		executionContext,
		client,
		r.Profile.Database,
		r.Password,
		targetDatabase,
		source,
	); err != nil {
		return verification.Metadata, err
	}
	setPhase("complete")
	return verification.Metadata, nil
}

type progressReader struct {
	source     io.Reader
	started    time.Time
	bytes      int64
	lastUpdate time.Time
	callback   func(Progress)
}

func (r *progressReader) Read(data []byte) (int, error) {
	n, err := r.source.Read(data)
	r.bytes += int64(n)
	now := time.Now()
	if r.callback != nil && (r.lastUpdate.IsZero() || now.Sub(r.lastUpdate) >= time.Second) {
		r.lastUpdate = now
		r.callback(Progress{
			UncompressedBytes: r.bytes,
			Elapsed:           now.Sub(r.started),
		})
	}
	return n, err
}
