//go:build windows

package mysql

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/winsec"
)

func TestWindowsClientOptionFileHasVerifiedACL(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "temporary client Ω")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := createOptionFileInTempDir(directory, config.Database{
		Host:     "127.0.0.1",
		Port:     3306,
		Username: "backup",
		Database: "fxserver",
	}, "very-secret-password")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if err := winsec.VerifyFile(path); err != nil {
		t.Fatalf("temporary option file ACL: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `password="very-secret-password"`) {
		t.Fatal("test option file did not contain the expected password content")
	}
}

func TestWindowsOptionFileCleanedAfterDumpStartFailure(t *testing.T) {
	pattern := filepath.Join(os.TempDir(), "fivemesh-backup-client-*.cnf")
	before, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Dump(
		context.Background(),
		ToolInfo{Path: filepath.Join(t.TempDir(), "missing-client.exe")},
		config.Database{
			Host:     "127.0.0.1",
			Port:     3306,
			Username: "backup",
			Database: "fxserver",
		},
		"very-secret-password",
		io.Discard,
	)
	if err == nil {
		t.Fatal("dump unexpectedly succeeded with a missing client")
	}
	after, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("temporary option files leaked: before=%v after=%v", before, after)
	}
}
