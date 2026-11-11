//go:build unix

package mysql

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestCancellationTerminatesIgnoringDescendants(t *testing.T) {
	originalGrace := processTerminationGrace.Load()
	processTerminationGrace.Store(int64(100 * time.Millisecond))
	defer processTerminationGrace.Store(originalGrace)

	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperProcess", "--")
	cmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=1",
		"HELPER_MODE=spawn-ignore",
		"HELPER_PID_FILE="+pidFile,
	)
	configureProcess(cmd)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	cmd.WaitDelay = processWaitDelay
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cleanup, err := attachProcess(cmd)
	if err != nil {
		t.Fatal(err)
	}

	childPID := waitForPIDFile(t, pidFile)
	start := time.Now()
	cancel()
	waitErr := cmd.Wait()
	cleanup()
	elapsed := time.Since(start)
	if waitErr == nil {
		t.Fatal("cancelled helper exited successfully")
	}
	if elapsed > 4*time.Second {
		t.Fatalf("descendant cleanup took too long: %s", elapsed)
	}
	if processExists(childPID) {
		t.Fatalf("descendant process %d survived cancellation", childPID)
	}
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(string(bytes.TrimSpace(data)))
			if parseErr != nil {
				t.Fatalf("parse child PID: %v", parseErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child PID file was not created at %s", path)
	return 0
}

func processExists(pid int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}
