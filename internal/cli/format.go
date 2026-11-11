package cli

import (
	"fmt"

	"github.com/FiveMesh/fivemesh-backup/internal/version"
)

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	divisor, exponent := int64(unit), 0
	for value := bytes / unit; value >= unit && exponent < 5; value /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(divisor), "KMGTPE"[exponent])
}

func versionString() string {
	return fmt.Sprintf("%s (commit %s, built %s)", version.Version, version.Commit, version.Date)
}
