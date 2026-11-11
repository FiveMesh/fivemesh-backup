//go:build windows

package platform

func IsRoot() bool {
	return false
}
