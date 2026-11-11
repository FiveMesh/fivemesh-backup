//go:build linux

package cli

import (
	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/schedule"
	"github.com/FiveMesh/fivemesh-backup/internal/systemd"
)

func buildScheduler(paths config.Paths) (schedule.Manager, error) {
	return systemd.New(paths)
}
