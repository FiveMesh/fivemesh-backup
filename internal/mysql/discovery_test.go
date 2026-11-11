package mysql

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
)

// The copied Go test executable acts as a versionable native client in both
// Linux and Windows CI; it does not require a real MariaDB installation.
func TestMain(m *testing.M) {
	if os.Getenv("FIVEMESH_TEST_DISCOVERY_CLIENT") == "1" {
		fmt.Fprintln(os.Stdout, "mariadb test client version 12.2")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fixtureClientDirectory(t *testing.T, withDump, withClient bool) string {
	t.Helper()
	binDir := filepath.Join(t.TempDir(), "MariaDB 12.2", "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	copyExecutable := func(name string) {
		t.Helper()
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		src, err := os.Open(exe)
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		dst, err := os.OpenFile(filepath.Join(binDir, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700)
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := io.Copy(dst, src)
		closeErr := dst.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatalf("copy native test executable: %v, close: %v", copyErr, closeErr)
		}
	}
	if withDump {
		copyExecutable("mariadb-dump")
	}
	if withClient {
		copyExecutable("mariadb")
	}
	return binDir
}

func TestDiscoverInDirectoryAcceptsRootOrBinWithSpaces(t *testing.T) {
	t.Setenv("FIVEMESH_TEST_DISCOVERY_CLIENT", "1")
	bin := fixtureClientDirectory(t, true, true)
	database := config.Database{Flavor: "mariadb", DumpTool: "auto", ClientTool: "auto"}
	for _, input := range []string{bin, filepath.Dir(bin), "\"" + filepath.Dir(bin) + "\""} {
		t.Run(filepath.Base(input), func(t *testing.T) {
			tools, err := DiscoverInDirectory(context.Background(), database, input)
			if err != nil {
				t.Fatal(err)
			}
			if !filepath.IsAbs(tools.Dump.Path) || !filepath.IsAbs(tools.Client.Path) {
				t.Fatalf("tools must contain absolute paths: %+v", tools)
			}
			if !strings.HasPrefix(tools.Dump.Path, bin) || !strings.HasPrefix(tools.Client.Path, bin) {
				t.Fatalf("tools were not resolved from selected bin folder: %+v", tools)
			}
			if tools.Dump.Version == "" || tools.Client.Version == "" {
				t.Fatalf("tool versions were not validated: %+v", tools)
			}
		})
	}
}

func TestDiscoverInDirectoryRejectsMissingPairAndRelativePath(t *testing.T) {
	t.Setenv("FIVEMESH_TEST_DISCOVERY_CLIENT", "1")
	bin := fixtureClientDirectory(t, true, false)
	if _, err := DiscoverInDirectory(context.Background(), config.Database{Flavor: "mariadb"}, bin); err == nil {
		t.Fatal("accepted directory with dump but no client")
	}
	if _, err := DiscoverInDirectory(context.Background(), config.Database{Flavor: "mariadb"}, "relative/path"); err == nil {
		t.Fatal("accepted relative installation directory")
	}
}

func TestDiscoverClientFindsClientNextToDumpToolOffPath(t *testing.T) {
	t.Setenv("FIVEMESH_TEST_DISCOVERY_CLIENT", "1")
	t.Setenv("PATH", t.TempDir())
	bin := fixtureClientDirectory(t, true, true)
	dump := filepath.Join(bin, "mariadb-dump")
	if runtime.GOOS == "windows" {
		dump += ".exe"
	}
	client, err := DiscoverClient(context.Background(), config.Database{Flavor: "mariadb", DumpTool: dump, ClientTool: "auto"})
	if err != nil {
		t.Fatalf("restore/check could not find a client that backup can use: %v", err)
	}
	if !strings.HasPrefix(client.Path, bin) {
		t.Fatalf("client resolved outside the dump tool folder: %s", client.Path)
	}
	if _, err := DiscoverClient(context.Background(), config.Database{Flavor: "mariadb", DumpTool: dump, ClientTool: filepath.Join(bin, "missing")}); err == nil {
		t.Fatal("an explicit client path must never be silently replaced")
	}
}
