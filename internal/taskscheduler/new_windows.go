//go:build windows

package taskscheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
)

const (
	taskCreateOrUpdate  = 6
	hresultTaskNotFound = uintptr(0x8004130F)
	hresultFileNotFound = uintptr(0x80070002)
	hresultPathNotFound = uintptr(0x80070003)
)

// comClient talks to Task Scheduler through COM. elevate, when set, performs a
// background (S4U) registration through a UAC-approved copy of this executable
// when the current process is not elevated.
type comClient struct {
	elevate func(ctx context.Context, name string, data []byte) error
}

func (comClient) elevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

func isRemoteDrive(path string) bool {
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' {
		return false
	}
	root, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil {
		return false
	}
	return windows.GetDriveType(root) == windows.DRIVE_REMOTE
}

func New(paths config.Paths) (*Scheduler, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve current executable: %w", err)
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}
	currentUser, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("resolve current Windows user: %w", err)
	}
	resolved, err := absolutePaths(paths)
	if err != nil {
		return nil, err
	}
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("resolve current Windows user SID: %w", err)
	}
	if tokenUser.User.Sid == nil {
		return nil, errors.New("resolve current Windows user SID: token returned no SID")
	}
	scheduler := &Scheduler{
		BinaryPath:     binary,
		Paths:          resolved,
		CurrentUser:    currentUser.Username,
		CurrentUserSID: tokenUser.User.Sid.String(),
	}
	scheduler.Client = comClient{elevate: scheduler.registerElevated}
	return scheduler, nil
}

func absolutePaths(paths config.Paths) (config.Paths, error) {
	values := []*string{&paths.Config, &paths.Credentials, &paths.StateDir}
	for _, value := range values {
		absolute, err := filepath.Abs(*value)
		if err != nil {
			return config.Paths{}, fmt.Errorf("resolve scheduler path %q: %w", *value, err)
		}
		*value = absolute
	}
	return paths, nil
}

func (comClient) Validate(ctx context.Context, data []byte) error {
	return withTaskService(ctx, func(service, _ *ole.IDispatch) error {
		definition, err := newDefinition(service, data)
		if err != nil {
			return err
		}
		definition.Release()
		return nil
	})
}

func (comClient) Get(ctx context.Context, name string) (taskState, error) {
	var state taskState
	err := withTaskService(ctx, func(_, folder *ole.IDispatch) error {
		task, err := getTask(folder, name)
		if err != nil {
			if isCOMNotFound(err) {
				return nil
			}
			return err
		}
		defer task.Release()
		state.Exists = true
		state.Enabled, err = taskBool(task, "Enabled")
		if err != nil {
			return err
		}
		state.State, err = taskInt32(task, "State")
		if err != nil {
			return err
		}
		state.Next, err = taskNextRun(task)
		if err != nil {
			return err
		}
		state.LastRun, err = taskTime(task, "LastRunTime")
		if err != nil {
			return err
		}
		state.LastResult, err = taskInt32(task, "LastTaskResult")
		if err != nil {
			return err
		}
		xmlText, err := taskString(task, "Xml")
		if err != nil {
			return err
		}
		state.XML = []byte(xmlText)
		return nil
	})
	return state, err
}

func (c comClient) Register(
	ctx context.Context,
	name string,
	data []byte,
	registration taskRegistration,
) error {
	if registration.LogonType == taskLogonS4U && !c.elevated() {
		if c.elevate == nil {
			return errors.New("background mode needs administrator approval; run this command from an elevated terminal")
		}
		return c.elevate(ctx, name, data)
	}
	return registerTask(ctx, name, data, registration)
}

func registerTask(
	ctx context.Context,
	name string,
	data []byte,
	registration taskRegistration,
) error {
	return withTaskService(ctx, func(service, folder *ole.IDispatch) error {
		definition, err := newDefinition(service, data)
		if err != nil {
			return err
		}
		defer definition.Release()
		var userID any
		var password any
		var securityDescriptor any
		switch registration.LogonType {
		case taskLogonPassword:
			userID = registration.UserID
			password = registration.Password
		case taskLogonS4U:
			userID = registration.UserID
		}
		if registration.SecurityDescriptor != "" {
			securityDescriptor = registration.SecurityDescriptor
		}
		result, err := oleutil.CallMethod(
			folder,
			"RegisterTaskDefinition",
			name,
			definition,
			int32(taskCreateOrUpdate),
			userID,
			password,
			registration.LogonType,
			securityDescriptor,
		)
		if result != nil {
			defer result.Clear()
		}
		if err != nil {
			message := err.Error()
			if registration.Password != "" {
				message = strings.ReplaceAll(message, registration.Password, "[REDACTED]")
			}
			return fmt.Errorf("Task Scheduler rejected registration: %s", message)
		}
		return nil
	})
}

func (comClient) SetEnabled(ctx context.Context, name string, enabled bool) error {
	return withTaskService(ctx, func(_, folder *ole.IDispatch) error {
		task, err := getTask(folder, name)
		if err != nil {
			return err
		}
		defer task.Release()
		result, err := oleutil.PutProperty(task, "Enabled", enabled)
		if result != nil {
			defer result.Clear()
		}
		if err != nil {
			return fmt.Errorf("set Task Scheduler enabled state: %w", err)
		}
		return nil
	})
}

func (comClient) Remove(ctx context.Context, name string) error {
	return withTaskService(ctx, func(_, folder *ole.IDispatch) error {
		result, err := oleutil.CallMethod(folder, "DeleteTask", name, int32(0))
		if result != nil {
			defer result.Clear()
		}
		if err != nil && !isCOMNotFound(err) {
			return fmt.Errorf("delete Task Scheduler task: %w", err)
		}
		return nil
	})
}

func (comClient) Run(ctx context.Context, name string) error {
	return withTaskService(ctx, func(_, folder *ole.IDispatch) error {
		task, err := getTask(folder, name)
		if err != nil {
			return err
		}
		defer task.Release()
		result, err := oleutil.CallMethod(task, "Run", nil)
		if result != nil {
			defer result.Clear()
		}
		if err != nil {
			return fmt.Errorf("Task Scheduler rejected manual run: %w", err)
		}
		return nil
	})
}

func withTaskService(ctx context.Context, operation func(service, folder *ole.IDispatch) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		if oleErr, ok := err.(*ole.OleError); !ok || oleErr.Code() != uintptr(0x00000001) {
			return fmt.Errorf("initialize COM for Task Scheduler: %w", err)
		}
	}
	defer ole.CoUninitialize()

	unknown, err := oleutil.CreateObject("Schedule.Service")
	if err != nil {
		return fmt.Errorf("connect to Windows Task Scheduler service: %w", err)
	}
	defer unknown.Release()
	service, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return fmt.Errorf("resolve Task Scheduler COM interface: %w", err)
	}
	defer service.Release()
	connectResult, err := oleutil.CallMethod(service, "Connect", nil, nil, nil, nil)
	if connectResult != nil {
		defer connectResult.Clear()
	}
	if err != nil {
		return fmt.Errorf("Task Scheduler service is unavailable: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	folderResult, err := oleutil.CallMethod(service, "GetFolder", `\`)
	if err != nil {
		return fmt.Errorf("open Task Scheduler root folder: %w", err)
	}
	folder, err := variantDispatch(folderResult)
	if err != nil {
		return err
	}
	defer folder.Release()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := operation(service, folder); err != nil {
		return err
	}
	return ctx.Err()
}

func newDefinition(service *ole.IDispatch, data []byte) (*ole.IDispatch, error) {
	result, err := oleutil.CallMethod(service, "NewTask", int32(0))
	if err != nil {
		return nil, fmt.Errorf("create Task Scheduler definition: %w", err)
	}
	definition, err := variantDispatch(result)
	if err != nil {
		return nil, err
	}
	putResult, err := oleutil.PutProperty(definition, "XmlText", string(data))
	if putResult != nil {
		defer putResult.Clear()
	}
	if err != nil {
		definition.Release()
		return nil, fmt.Errorf("Task Scheduler rejected task XML: %w", err)
	}
	return definition, nil
}

func getTask(folder *ole.IDispatch, name string) (*ole.IDispatch, error) {
	result, err := oleutil.CallMethod(folder, "GetTask", name)
	if err != nil {
		return nil, err
	}
	return variantDispatch(result)
}

func variantDispatch(value *ole.VARIANT) (*ole.IDispatch, error) {
	if value == nil {
		return nil, errors.New("Task Scheduler returned an empty COM object")
	}
	dispatch := value.ToIDispatch()
	if dispatch == nil {
		value.Clear()
		return nil, errors.New("Task Scheduler returned a non-dispatch COM value")
	}
	dispatch.AddRef()
	value.Clear()
	return dispatch, nil
}

func taskBool(task *ole.IDispatch, property string) (bool, error) {
	value, err := oleutil.GetProperty(task, property)
	if err != nil {
		return false, fmt.Errorf("read task property %s: %w", property, err)
	}
	defer value.Clear()
	boolean, ok := value.Value().(bool)
	if !ok {
		return false, fmt.Errorf("task property %s has unexpected type %T", property, value.Value())
	}
	return boolean, nil
}

func taskInt32(task *ole.IDispatch, property string) (int32, error) {
	value, err := oleutil.GetProperty(task, property)
	if err != nil {
		return 0, fmt.Errorf("read task property %s: %w", property, err)
	}
	defer value.Clear()
	number, ok := value.Value().(int32)
	if !ok {
		return 0, fmt.Errorf("task property %s has unexpected type %T", property, value.Value())
	}
	return number, nil
}

func taskString(task *ole.IDispatch, property string) (string, error) {
	value, err := oleutil.GetProperty(task, property)
	if err != nil {
		return "", fmt.Errorf("read task property %s: %w", property, err)
	}
	defer value.Clear()
	text, ok := value.Value().(string)
	if !ok {
		return "", fmt.Errorf("task property %s has unexpected type %T", property, value.Value())
	}
	return text, nil
}

func taskNextRun(task *ole.IDispatch) (string, error) {
	return taskTime(task, "NextRunTime")
}

func taskTime(task *ole.IDispatch, property string) (string, error) {
	value, err := oleutil.GetProperty(task, property)
	if err != nil {
		return "", fmt.Errorf("read task property %s: %w", property, err)
	}
	defer value.Clear()
	switch next := value.Value().(type) {
	case time.Time:
		return formatTaskTime(next, time.Local), nil
	case float64:
		if next <= 0 {
			return "", nil
		}
		parsed, err := ole.GetVariantDate(uint64(value.Val))
		if err != nil {
			return "", fmt.Errorf("decode task property NextRunTime: %w", err)
		}
		return formatTaskTime(parsed, time.Local), nil
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("task property %s has unexpected type %T", property, next)
	}
}

func isCOMNotFound(err error) bool {
	var oleErr *ole.OleError
	if !errors.As(err, &oleErr) {
		return false
	}
	code := oleErr.Code() & 0xffffffff
	if isTaskMissingCode(code) {
		return true
	}
	switch subError := oleErr.SubError().(type) {
	case ole.EXCEPINFO:
		return isTaskMissingCode(uintptr(subError.SCODE()))
	case *ole.EXCEPINFO:
		return isTaskMissingCode(uintptr(subError.SCODE()))
	default:
		return false
	}
}

func isTaskMissingCode(code uintptr) bool {
	return code == hresultTaskNotFound ||
		code == hresultFileNotFound ||
		code == hresultPathNotFound
}
