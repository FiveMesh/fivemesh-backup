package retention

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/archive"
	"github.com/FiveMesh/fivemesh-backup/internal/config"
)

type Action struct {
	Path      string
	CreatedAt time.Time
	SizeBytes int64
	Reason    string
}

type Plan struct {
	Profile            string
	ArchivesConsidered int
	ArchivesKept       int
	TotalBytes         int64
	ProjectedBytes     int64
	ToDelete           []Action
	Warnings           []string
}

func BuildPlan(directory, profile string, policy config.Retention, now time.Time) (Plan, error) {
	plan := Plan{Profile: profile}
	archives, _, err := archive.Scan(directory)
	if err != nil {
		return Plan{}, err
	}

	var relevant []archive.ScannedArchive
	for _, item := range archives {
		if item.Metadata.Profile != profile || !item.Metadata.Complete {
			continue
		}
		relevant = append(relevant, item)
		plan.TotalBytes += item.Metadata.SizeBytes
	}
	plan.ArchivesConsidered = len(relevant)
	plan.ProjectedBytes = plan.TotalBytes
	if len(relevant) == 0 {
		return plan, nil
	}

	protected := make([]bool, len(relevant))
	protected[0] = true // Never remove the newest known successful archive.
	for index := 0; index < len(relevant) && index < policy.KeepLast; index++ {
		protected[index] = true
	}
	if policy.KeepDays > 0 {
		cutoff := now.Add(-time.Duration(policy.KeepDays) * 24 * time.Hour)
		for index, item := range relevant {
			if !item.Metadata.CreatedAt.Before(cutoff) {
				protected[index] = true
			}
		}
	}

	selected := make([]bool, len(relevant))
	if policy.KeepLast > 0 || policy.KeepDays > 0 {
		for index, item := range relevant {
			if protected[index] {
				continue
			}
			selected[index] = true
			plan.ToDelete = append(plan.ToDelete, Action{
				Path:      item.Path,
				CreatedAt: item.Metadata.CreatedAt,
				SizeBytes: item.Metadata.SizeBytes,
				Reason:    "outside keep policy",
			})
			plan.ProjectedBytes -= item.Metadata.SizeBytes
		}
	}

	if policy.MaximumBytes > 0 && plan.ProjectedBytes > policy.MaximumBytes {
		for index := len(relevant) - 1; index >= 1 && plan.ProjectedBytes > policy.MaximumBytes; index-- {
			if selected[index] {
				continue
			}
			item := relevant[index]
			selected[index] = true
			plan.ToDelete = append(plan.ToDelete, Action{
				Path:      item.Path,
				CreatedAt: item.Metadata.CreatedAt,
				SizeBytes: item.Metadata.SizeBytes,
				Reason:    "maximum storage allocation",
			})
			plan.ProjectedBytes -= item.Metadata.SizeBytes
		}
		if plan.ProjectedBytes > policy.MaximumBytes {
			plan.Warnings = append(plan.Warnings,
				"retained archives still exceed maximum_bytes; the newest successful archive is protected")
		}
	}

	plan.ArchivesKept = len(relevant) - len(plan.ToDelete)
	return plan, nil
}

func Execute(directory, profile string, plan Plan) ([]Action, error) {
	cleanDirectory, err := filepath.Abs(filepath.Clean(directory))
	if err != nil {
		return nil, fmt.Errorf("resolve backup directory: %w", err)
	}
	var deleted []Action
	for _, action := range plan.ToDelete {
		cleanPath, err := filepath.Abs(filepath.Clean(action.Path))
		if err != nil {
			return deleted, fmt.Errorf("resolve archive path: %w", err)
		}
		if filepath.Dir(cleanPath) != cleanDirectory {
			return deleted, fmt.Errorf("refusing to delete archive outside backup directory: %q", action.Path)
		}
		metadata, err := archive.ReadMetadata(cleanPath)
		if err != nil {
			return deleted, fmt.Errorf("re-read archive metadata: %w", err)
		}
		if metadata.Profile != profile || !metadata.Complete {
			return deleted, fmt.Errorf("refusing to delete archive that does not belong to profile %q", profile)
		}
		if err := removeArchive(cleanPath); err != nil {
			return deleted, err
		}
		deleted = append(deleted, action)
	}
	return deleted, nil
}

func removeArchive(path string) error {
	// Remove metadata first so an interrupted cleanup cannot leave a file that
	// still appears to be a complete archive.
	paths := []string{
		path + ".json",
		path + ".sha256",
		path,
	}
	for _, candidate := range paths {
		if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %q: %w", filepath.Base(candidate), err)
		}
	}
	return nil
}

func FormatActions(actions []Action) string {
	if len(actions) == 0 {
		return "no archives"
	}
	var builder strings.Builder
	for index, action := range actions {
		if index > 0 {
			builder.WriteString(", ")
		}
		builder.WriteString(filepath.Base(action.Path))
	}
	return builder.String()
}
