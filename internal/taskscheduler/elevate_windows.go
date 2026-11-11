//go:build windows

package taskscheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/schedule"
	"github.com/FiveMesh/fivemesh-backup/internal/winsec"
	"github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"
)

// ElevatedRegisterCommand is the hidden CLI command that performs a single
// background (S4U) task registration after UAC approval.
const ElevatedRegisterCommand = "elevated-register"

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	seeMaskFlagNoUI       = 0x00000400
	swHide                = 0
	errorCancelled        = windows.Errno(1223)
)

var (
	shell32            = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteEx = shell32.NewProc("ShellExecuteExW")
)

// shellExecuteInfo mirrors SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	size         uint32
	mask         uint32
	window       windows.Handle
	verb         *uint16
	file         *uint16
	parameters   *uint16
	directory    *uint16
	show         int32
	instApp      windows.Handle
	idList       uintptr
	class        *uint16
	classKey     windows.Handle
	hotKey       uint32
	iconOrMonitr windows.Handle
	process      windows.Handle
}

type elevatedRequest struct {
	Version  int          `json:"version"`
	UserSID  string       `json:"user_sid"`
	User     string       `json:"user"`
	Profile  string       `json:"profile"`
	TaskName string       `json:"task_name"`
	Paths    config.Paths `json:"paths"`
	XML      []byte       `json:"xml"`
}

type elevatedResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// registerElevated asks Windows for administrator approval and registers a
// background task through a short-lived elevated copy of this executable. The
// elevated process re-validates the task against its own identity before it
// registers anything, so it cannot be used to create arbitrary tasks.
func (s *Scheduler) registerElevated(ctx context.Context, name string, data []byte) error {
	profile, err := profileFromTask(data)
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "fivemesh-backup-elevate-")
	if err != nil {
		return fmt.Errorf("prepare administrator approval request: %w", err)
	}
	defer os.RemoveAll(directory)
	if err := winsec.ProtectDirectory(directory); err != nil {
		return fmt.Errorf("protect administrator approval request: %w", err)
	}
	request := elevatedRequest{
		Version:  1,
		UserSID:  s.CurrentUserSID,
		User:     s.CurrentUser,
		Profile:  profile,
		TaskName: name,
		Paths:    s.Paths,
		XML:      data,
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	requestPath := filepath.Join(directory, "request.json")
	if err := os.WriteFile(requestPath, encoded, 0o600); err != nil {
		return fmt.Errorf("write administrator approval request: %w", err)
	}
	resultPath := filepath.Join(directory, "result.json")
	exitCode, err := runElevated(ctx, s.BinaryPath, []string{
		ElevatedRegisterCommand,
		"--request", requestPath,
		"--result", resultPath,
	})
	if err != nil {
		return err
	}
	if raw, readErr := os.ReadFile(resultPath); readErr == nil {
		var result elevatedResult
		if json.Unmarshal(raw, &result) == nil {
			if result.OK {
				return nil
			}
			if result.Error != "" {
				return errors.New(result.Error)
			}
		}
	}
	return fmt.Errorf("administrator task registration failed with exit code %d", exitCode)
}

// ServeElevatedRegistration runs inside the UAC-approved process. It accepts a
// request only when it was approved by the same Windows account that owns the
// FiveMesh configuration and when the task is exactly the one this
// installation would generate for that profile.
func ServeElevatedRegistration(ctx context.Context, requestPath, resultPath string) error {
	err := serveElevatedRegistration(ctx, requestPath)
	result := elevatedResult{OK: err == nil}
	if err != nil {
		result.Error = err.Error()
	}
	if encoded, marshalErr := json.Marshal(result); marshalErr == nil && resultPath != "" {
		_ = os.WriteFile(resultPath, encoded, 0o600)
	}
	return err
}

func serveElevatedRegistration(ctx context.Context, requestPath string) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("administrator approval was not granted")
	}
	if err := winsec.VerifyDirectory(filepath.Dir(requestPath)); err != nil {
		return fmt.Errorf("administrator approval request is not protected: %w", err)
	}
	raw, err := os.ReadFile(requestPath)
	if err != nil {
		return fmt.Errorf("read administrator approval request: %w", err)
	}
	var request elevatedRequest
	if err := json.Unmarshal(raw, &request); err != nil || request.Version != 1 {
		return errors.New("administrator approval request is malformed")
	}
	scheduler, err := New(request.Paths)
	if err != nil {
		return err
	}
	if !strings.EqualFold(scheduler.CurrentUserSID, request.UserSID) {
		return fmt.Errorf(
			"background mode schedules backups for the account that approves it, but approval came from %s while setup runs as %s. "+
				"Run setup from an administrator account, or choose the \"only while signed in\" or \"stored password\" mode",
			scheduler.CurrentUser,
			request.User,
		)
	}
	expectedName, err := scheduler.taskName(request.Profile)
	if err != nil {
		return err
	}
	if expectedName != request.TaskName {
		return errors.New("administrator approval request names an unexpected task")
	}
	if !scheduler.isManagedTask(request.XML, request.TaskName, request.Profile, false) ||
		taskLogonMode(request.XML) != "background" {
		return errors.New("administrator approval request does not match this FiveMesh Backup installation")
	}
	registration, err := scheduler.registrationForTask(request.XML, schedule.LogonSecret{})
	if err != nil {
		return err
	}
	return registerTask(ctx, request.TaskName, request.XML, registration)
}

// runElevated starts executable with the "runas" verb, which shows the UAC
// consent prompt, and waits for it to exit.
func runElevated(ctx context.Context, executable string, arguments []string) (uint32, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED|ole.COINIT_DISABLE_OLE1DDE); err == nil {
		defer ole.CoUninitialize()
	}
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		quoted[index] = windows.EscapeArg(argument)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return 0, err
	}
	parameters, err := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	if err != nil {
		return 0, err
	}
	directory, _ := windows.UTF16PtrFromString(filepath.Dir(executable))
	info := shellExecuteInfo{
		mask:       seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskFlagNoUI,
		verb:       verb,
		file:       file,
		parameters: parameters,
		directory:  directory,
		show:       swHide,
	}
	info.size = uint32(unsafe.Sizeof(info))
	ok, _, callErr := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		if errors.Is(callErr, errorCancelled) {
			return 0, errors.New("administrator approval was declined; background scheduling needs a one-time approval to register the task")
		}
		return 0, fmt.Errorf("request administrator approval: %w", callErr)
	}
	if info.process == 0 {
		return 0, errors.New("request administrator approval: no process handle returned")
	}
	defer windows.CloseHandle(info.process)
	for {
		event, err := windows.WaitForSingleObject(info.process, 200)
		if err != nil {
			return 0, fmt.Errorf("wait for administrator task registration: %w", err)
		}
		if event == windows.WAIT_OBJECT_0 {
			break
		}
		if ctx.Err() != nil {
			// The elevated process performs one bounded COM call; give it a
			// short grace period rather than abandoning it mid-registration.
			if event, _ := windows.WaitForSingleObject(info.process, uint32((5 * time.Second).Milliseconds())); event != windows.WAIT_OBJECT_0 {
				return 0, ctx.Err()
			}
			break
		}
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		return 0, fmt.Errorf("read administrator task registration result: %w", err)
	}
	return code, nil
}
