//go:build !windows

package winsec

func ProtectDirectory(string) error { return nil }
func ProtectFile(string) error      { return nil }
func VerifyDirectory(string) error  { return nil }
func VerifyFile(string) error       { return nil }
