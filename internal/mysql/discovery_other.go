//go:build !windows

package mysql

func installedClientDirectories() []string {
	return nil
}
