//go:build unix

package mysql

import (
	"errors"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
)

var processTerminationGrace atomic.Int64

func init() {
	processTerminationGrace.Store(int64(2 * time.Second))
}

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		pid := cmd.Process.Pid
		if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		go func() {
			time.Sleep(time.Duration(processTerminationGrace.Load()))
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}()
		return nil
	}
}

func attachProcess(*exec.Cmd) (func(), error) {
	return func() {}, nil
}
