//go:build windows

package cli

import (
	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/schedule"
	"github.com/FiveMesh/fivemesh-backup/internal/taskscheduler"
)

func buildScheduler(paths config.Paths) (schedule.Manager, error) {
	return taskscheduler.New(paths)
}
