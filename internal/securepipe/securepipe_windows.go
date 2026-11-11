//go:build windows

package securepipe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

func ServePasswordPipe(ctx context.Context, name string, secret []byte) error {
	if !ValidName(name) {
		return errors.New("invalid secure pipe name")
	}
	if len(secret) == 0 || len(secret) > MaxSecretBytes {
		return fmt.Errorf("secure password length must be between 1 and %d bytes", MaxSecretBytes)
	}
	currentUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("resolve current user for secure pipe: %w", err)
	}
	if currentUser.User.Sid == nil {
		return errors.New("resolve current user SID for secure pipe")
	}
	sddl := "O:" + currentUser.User.Sid.String() + "D:P(A;;GA;;;" + currentUser.User.Sid.String() + ")"
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("create secure pipe DACL: %w", err)
	}
	var pinner runtime.Pinner
	pinner.Pin(descriptor)
	defer pinner.Unpin()
	attributes := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	path, err := windows.UTF16PtrFromString(`\\.\pipe\` + name)
	if err != nil {
		return err
	}
	handle, err := windows.CreateNamedPipe(
		path,
		windows.PIPE_ACCESS_OUTBOUND|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		1,
		uint32(len(secret)+4),
		0,
		0,
		&attributes,
	)
	if err != nil {
		return fmt.Errorf("create secure password pipe: %w", err)
	}
	closed := false
	closePipe := func() {
		if !closed {
			closed = true
			_ = windows.CloseHandle(handle)
		}
	}
	defer closePipe()
	if err := verifyPipeACL(handle, currentUser.User.Sid); err != nil {
		return err
	}
	connected := make(chan error, 1)
	go func() {
		connected <- windows.ConnectNamedPipe(handle, nil)
	}()
	select {
	case <-ctx.Done():
		_ = windows.CancelIoEx(handle, nil)
		closePipe()
		return ctx.Err()
	case connectErr := <-connected:
		if connectErr != nil && !errors.Is(connectErr, windows.ERROR_PIPE_CONNECTED) {
			return fmt.Errorf("accept secure password pipe client: %w", connectErr)
		}
	}
	file := os.NewFile(uintptr(handle), `\\.\pipe\`+name)
	if file == nil {
		return errors.New("open connected secure password pipe")
	}
	closed = true
	defer file.Close()
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(secret)))
	if _, err := file.Write(header[:]); err != nil {
		return fmt.Errorf("write secure password length: %w", err)
	}
	if _, err := file.Write(secret); err != nil {
		return fmt.Errorf("write secure password: %w", err)
	}
	return nil
}

func ReadPassword(ctx context.Context, name string) (string, error) {
	if !ValidName(name) {
		return "", errors.New("invalid secure pipe name")
	}
	path := `\\.\pipe\` + name
	var file *os.File
	var lastErr error
	for {
		var err error
		file, err = os.OpenFile(path, os.O_RDONLY, 0)
		if err == nil {
			break
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer file.Close()
	stopWatcher := make(chan struct{})
	defer close(stopWatcher)
	go func() {
		select {
		case <-ctx.Done():
			_ = file.Close()
		case <-stopWatcher:
		}
	}()
	var header [4]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return "", fmt.Errorf("read secure password length: %w (last connect error: %v)", err, lastErr)
	}
	length := int(binary.LittleEndian.Uint32(header[:]))
	if length <= 0 || length > MaxSecretBytes || length%2 != 0 {
		return "", errors.New("secure password pipe returned an invalid length")
	}
	data := make([]byte, length)
	defer clearBytes(data)
	if _, err := io.ReadFull(file, data); err != nil {
		return "", fmt.Errorf("read secure password: %w", err)
	}
	codeUnits := make([]uint16, length/2)
	for index := range codeUnits {
		codeUnits[index] = binary.LittleEndian.Uint16(data[index*2:])
	}
	secret := utf16.Decode(codeUnits)
	clear(codeUnits)
	if len(secret) == 0 {
		return "", errors.New("secure password pipe returned an empty password")
	}
	for _, char := range secret {
		if char == 0 || char == '\r' || char == '\n' {
			return "", errors.New("secure password contains unsupported control characters")
		}
	}
	return string(secret), nil
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func verifyPipeACL(handle windows.Handle, expectedSID *windows.SID) error {
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_KERNEL_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read secure password pipe ACL: %w", err)
	}
	owner, defaulted, err := descriptor.Owner()
	if err != nil || owner == nil || defaulted || !owner.Equals(expectedSID) {
		return errors.New("secure password pipe owner is not the current user")
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("secure password pipe DACL is not protected")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		return errors.New("secure password pipe must have exactly one DACL entry")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return fmt.Errorf("read secure password pipe ACE: %w", err)
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 {
		return errors.New("secure password pipe has an unexpected ACE")
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !sid.Equals(expectedSID) {
		return errors.New("secure password pipe grants access to an unexpected principal")
	}
	return nil
}
