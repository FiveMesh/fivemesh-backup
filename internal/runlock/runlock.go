package runlock

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

type Lock struct {
	file *flock.Flock
}

func Acquire(locksDir, profile string) (*Lock, bool, error) {
	if err := os.MkdirAll(locksDir, 0o700); err != nil {
		return nil, false, fmt.Errorf("create lock directory: %w", err)
	}
	path := filepath.Join(locksDir, profile+".lock")
	file := flock.New(path)
	locked, err := file.TryLock()
	if err != nil {
		return nil, false, fmt.Errorf("acquire profile lock: %w", err)
	}
	if !locked {
		return nil, false, nil
	}
	return &Lock{file: file}, true, nil
}

func IsActive(locksDir, profile string) (bool, error) {
	if err := os.MkdirAll(locksDir, 0o700); err != nil {
		return false, fmt.Errorf("create lock directory: %w", err)
	}
	file := flock.New(filepath.Join(locksDir, profile+".lock"))
	acquired, err := file.TryLock()
	if err != nil {
		return false, fmt.Errorf("probe profile lock: %w", err)
	}
	if acquired {
		if err := file.Unlock(); err != nil {
			return false, fmt.Errorf("release profile lock probe: %w", err)
		}
		return false, nil
	}
	return true, nil
}

func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	if err := l.file.Unlock(); err != nil {
		return fmt.Errorf("release profile lock: %w", err)
	}
	return nil
}
