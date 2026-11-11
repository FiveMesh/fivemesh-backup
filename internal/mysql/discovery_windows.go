package mysql

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// installedClientDirectories searches a small, deterministic set of known
// Windows installer layouts. It never recursively traverses user files.
func installedClientDirectories() []string {
	var patterns []string
	for _, root := range []string{
		os.Getenv("ProgramFiles"),
		os.Getenv("ProgramW6432"),
		os.Getenv("ProgramFiles(x86)"),
	} {
		if root == "" {
			continue
		}
		patterns = append(patterns,
			filepath.Join(root, "MariaDB *", "bin"),
			filepath.Join(root, "MySQL", "MySQL Server *", "bin"),
		)
	}
	drive := os.Getenv("SystemDrive")
	if drive == "" {
		drive = "C:"
	}
	patterns = append(patterns,
		filepath.Join(drive+"\\", "xampp", "mysql", "bin"),
		filepath.Join(drive+"\\", "laragon", "bin", "mariadb", "*", "bin"),
		filepath.Join(drive+"\\", "laragon", "bin", "mysql", "*", "bin"),
		filepath.Join(drive+"\\", "wamp64", "bin", "mariadb", "*", "bin"),
		filepath.Join(drive+"\\", "wamp64", "bin", "mysql", "*", "bin"),
	)
	seen := make(map[string]bool)
	var directories []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		// Prefer newer installs for the same installer family.
		sort.Sort(sort.Reverse(sort.StringSlice(matches)))
		for _, match := range matches {
			key := strings.ToLower(filepath.Clean(match))
			if !seen[key] {
				seen[key] = true
				directories = append(directories, match)
			}
		}
	}
	return directories
}
