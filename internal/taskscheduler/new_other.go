//go:build !windows

package taskscheduler

import (
	"errors"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
)

func New(config.Paths) (*Scheduler, error) {
	return nil, errors.New("Windows Task Scheduler is available on Windows only")
}

func isRemoteDrive(string) bool {
	return false
}
