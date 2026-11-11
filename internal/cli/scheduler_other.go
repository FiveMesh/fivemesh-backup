//go:build !linux && !windows

package cli

import (
	"errors"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/schedule"
)

func buildScheduler(config.Paths) (schedule.Manager, error) {
	return nil, errors.New("native scheduling is not implemented on this platform")
}
