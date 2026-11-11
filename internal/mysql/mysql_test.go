package mysql

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
)

func TestDumpArgumentsDoNotContainPassword(t *testing.T) {
	optionsPath := filepath.Join(t.TempDir(), "client.cnf")
	database := config.Database{
		Host:              "127.0.0.1",
		Port:              3306,
		Username:          "backup",
		Database:          "fxserver",
		SingleTransaction: true,
		IncludeRoutines:   true,
		IncludeEvents:     true,
	}
	args := buildDumpArgs(optionsPath, database)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "secret") || strings.Contains(joined, "--password") {
		t.Fatalf("password material leaked into arguments: %v", args)
	}
	if args[len(args)-1] != "fxserver" || args[len(args)-2] != "--" {
		t.Fatalf("database is not isolated as a positional argument: %v", args)
	}
}

func TestConnectionTestUsesSelectedDatabase(t *testing.T) {
	args := buildConnectionTestArgs("/tmp/client.cnf", "fxserver")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--database=fxserver") {
		t.Fatalf("connection test does not select the configured database: %v", args)
	}
	if strings.Contains(joined, "--password") {
		t.Fatalf("connection test contains password args: %v", args)
	}
}

func TestTransientConnectionErrorClassification(t *testing.T) {
	transient := []string{
		"Got error: 2002: Can't connect to local server",
		"Got error: 2003: Can't connect to MySQL server",
		"ERROR 1040 (HY000): Too many connections",
		"ERROR 1053: Server shutdown in progress",
	}
	permanent := []string{
		"ERROR 1044 (42000): Access denied for user to database",
		"ERROR 1045 (28000): Access denied for user",
		"ERROR 1049 (42000): Unknown database 'missing'",
		"ERROR 1142 (42000): SELECT command denied",
	}
	for _, message := range transient {
		if !IsTransientConnectionError(errors.New(message)) {
			t.Errorf("expected transient: %s", message)
		}
	}
	for _, message := range permanent {
		if IsTransientConnectionError(errors.New(message)) {
			t.Errorf("expected permanent: %s", message)
		}
	}
	if !IsTransientConnectionError(context.DeadlineExceeded) {
		t.Error("probe deadline should be transient")
	}
	if IsTransientConnectionError(context.Canceled) {
		t.Error("cancellation should not be retried")
	}
}

func TestOptionFileEscapesPassword(t *testing.T) {
	password := `p"a\ss`
	path, err := createOptionFile(config.Database{
		Host:     "127.0.0.1",
		Port:     3306,
		Username: "backup",
		Database: "fxserver",
	}, password)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `password="p\"a\\ss"`) {
		t.Fatalf("password was not escaped in option file: %s", data)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("option file mode = %04o, want 0600", info.Mode().Perm())
		}
	}
}

func TestDumpContextCancellationReapsProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperProcess", "--")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "HELPER_MODE=sleep")
	stderr := newLimitedBuffer(maxDiagnosticBytes)
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, copyErr := bytes.NewBuffer(nil).ReadFrom(stdout)
	waitErr := cmd.Wait()
	if copyErr != nil {
		t.Fatalf("read helper output: %v", copyErr)
	}
	if ctx.Err() == nil {
		t.Fatal("helper exited before timeout")
	}
	if waitErr == nil {
		t.Fatal("cancelled helper exited successfully")
	}
	// Unix reports a signal-terminated child as !Exited(), but a non-nil
	// ProcessState after Wait is the portable proof that it was reaped.
	if cmd.ProcessState == nil {
		t.Fatal("cancelled process was not reaped")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("cancelled process took too long to reap: %s", elapsed)
	}
}

func TestLimitedBufferKeepsRecentDiagnostics(t *testing.T) {
	buffer := newLimitedBuffer(8)
	if _, err := buffer.Write([]byte("abcdefghijkl")); err != nil {
		t.Fatal(err)
	}
	if got := buffer.String(); got != "efghijkl" {
		t.Fatalf("buffer = %q, want %q", got, "efghijkl")
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	switch os.Getenv("HELPER_MODE") {
	case "sleep":
		fmtPrint("started\n")
		time.Sleep(10 * time.Second)
	case "ignore":
		signal.Ignore(syscall.SIGTERM)
		if path := os.Getenv("HELPER_PID_FILE"); path != "" {
			_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600)
		}
		fmtPrint("child-started\n")
		time.Sleep(20 * time.Second)
	case "spawn-ignore":
		signal.Ignore(syscall.SIGTERM)
		child := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--")
		child.Env = append(os.Environ(),
			"GO_WANT_HELPER_PROCESS=1",
			"HELPER_MODE=ignore",
			"HELPER_PID_FILE="+os.Getenv("HELPER_PID_FILE"),
		)
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			fmtPrint("child start failed\n")
			os.Exit(20)
		}
		time.Sleep(20 * time.Second)
		os.Exit(21)
	default:
		fmtPrint("unknown helper mode\n")
		os.Exit(2)
	}
	os.Exit(0)
}

func fmtPrint(message string) {
	_, _ = os.Stdout.WriteString(message)
}
