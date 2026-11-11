package mysql

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
)

func TestWindowsDiscoversInstalledMariaDBWithoutPATH(t *testing.T) {
	t.Setenv("FIVEMESH_TEST_DISCOVERY_CLIENT", "1")
	bin := fixtureClientDirectory(t, true, true)
	t.Setenv("ProgramFiles", filepath.Dir(filepath.Dir(bin)))
	t.Setenv("ProgramW6432", "")
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("PATH", t.TempDir())

	tools, err := Discover(context.Background(), config.Database{
		Flavor: "mariadb", DumpTool: "auto", ClientTool: "auto",
	})
	if err != nil {
		t.Fatalf("installed MariaDB not discovered outside PATH: %v", err)
	}
	if !strings.HasPrefix(strings.ToLower(tools.Dump.Path), strings.ToLower(bin)) ||
		!strings.HasPrefix(strings.ToLower(tools.Client.Path), strings.ToLower(bin)) {
		t.Fatalf("unexpected installed tool locations: %+v", tools)
	}
}
