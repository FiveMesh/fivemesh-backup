//go:build windows

package winsec

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const fullFileAccess = windows.ACCESS_MASK(
	windows.STANDARD_RIGHTS_ALL | 0x1ff,
)

var setNamedSecurityInfo = windows.SetNamedSecurityInfo

type Principal struct {
	SID         *windows.SID
	Name        string
	TrusteeType windows.TRUSTEE_TYPE
}

func ProtectDirectory(path string) error {
	return protect(path, true)
}

func ProtectFile(path string) error {
	return protect(path, false)
}

func VerifyDirectory(path string) error {
	return verify(path, true)
}

func VerifyFile(path string) error {
	return verify(path, false)
}

func protect(path string, directory bool) error {
	info, err := inspectPath(path)
	if err != nil {
		return err
	}
	if directory != info.IsDir() {
		return fmt.Errorf("sensitive path %q has unexpected object type", path)
	}

	principals, err := requiredPrincipals()
	if err != nil {
		return err
	}
	var pinner runtime.Pinner
	defer pinner.Unpin()
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(principals))
	inheritance := uint32(windows.NO_INHERITANCE)
	if directory {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	for _, principal := range principals {
		pinner.Pin(principal.SID)
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inheritance,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  principal.TrusteeType,
				TrusteeValue: windows.TrusteeValueFromSID(principal.SID),
			},
		})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("build sensitive file ACL for %q: %w", path, err)
	}
	if err := setNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		principals[0].SID,
		nil,
		acl,
		nil,
	); err != nil {
		return fmt.Errorf("set sensitive file ACL for %q: %w", path, err)
	}
	if directory {
		return VerifyDirectory(path)
	}
	return VerifyFile(path)
}

func inspectPath(path string) (os.FileInfo, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("inspect sensitive path %q: %w", path, err)
	}
	attributes, err := windows.GetFileAttributes(pathPointer)
	if err != nil {
		return nil, fmt.Errorf("inspect sensitive path %q: %w", path, err)
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, fmt.Errorf("refusing to change ACL on reparse point %q", path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect sensitive path %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to change ACL on symbolic link %q", path)
	}
	return info, nil
}

func verify(path string, directory bool) error {
	info, err := inspectPath(path)
	if err != nil {
		return err
	}
	if directory != info.IsDir() {
		return fmt.Errorf("sensitive path %q has unexpected object type", path)
	}
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read sensitive file ACL for %q: %w", path, err)
	}
	if descriptor == nil {
		return fmt.Errorf("sensitive path %q has no security descriptor", path)
	}
	owner, defaulted, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("read sensitive path owner for %q: %w", path, err)
	}
	if owner == nil || defaulted {
		return fmt.Errorf("sensitive path %q has no explicit owner", path)
	}
	currentUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("resolve current user for ACL verification: %w", err)
	}
	if currentUser.User.Sid == nil {
		return fmt.Errorf("resolve current user SID for ACL verification")
	}
	if !owner.Equals(currentUser.User.Sid) {
		return fmt.Errorf(
			"sensitive path %q is owned by %s; expected current account %s",
			path,
			owner.String(),
			currentUser.User.Sid.String(),
		)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read sensitive ACL control for %q: %w", path, err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("sensitive path %q has inheritable parent ACL; disable inheritance before use", path)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read sensitive DACL for %q: %w", path, err)
	}
	if dacl == nil {
		return fmt.Errorf("sensitive path %q has no DACL", path)
	}
	required, err := requiredPrincipals()
	if err != nil {
		return err
	}
	expected := make(map[string]windows.TRUSTEE_TYPE, len(required))
	for _, principal := range required {
		expected[principal.SID.String()] = principal.TrusteeType
	}
	seen := make(map[string]uint8, len(required))
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			return fmt.Errorf("read ACE from sensitive DACL for %q: %w", path, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("sensitive path %q contains unsupported ACE type %d", path, ace.Header.AceType)
		}
		if ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			return fmt.Errorf("sensitive path %q contains inherited ACE", path)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		sidString := sid.String()
		if _, ok := expected[sidString]; !ok {
			return fmt.Errorf("sensitive path %q grants access to unexpected principal %s", path, sidString)
		}
		if directory {
			const inheritFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE | windows.INHERIT_ONLY_ACE
			switch ace.Header.AceFlags {
			case 0:
				if !isFullAccess(ace.Mask) {
					return fmt.Errorf("sensitive directory %q has non-full access ACE mask %#x", path, uint32(ace.Mask))
				}
				if seen[sidString]&1 != 0 {
					return fmt.Errorf("sensitive directory %q contains duplicate effective ACE for %s", path, sidString)
				}
				seen[sidString] |= 1
			case inheritFlags:
				if ace.Mask != windows.ACCESS_MASK(windows.GENERIC_ALL) {
					return fmt.Errorf("sensitive directory %q has incorrect inherit-only ACE mask %#x", path, uint32(ace.Mask))
				}
				if seen[sidString]&2 != 0 {
					return fmt.Errorf("sensitive directory %q contains duplicate inherit-only ACE for %s", path, sidString)
				}
				seen[sidString] |= 2
			default:
				return fmt.Errorf("sensitive directory %q has unexpected ACE flags %#x", path, ace.Header.AceFlags)
			}
		} else {
			if ace.Header.AceFlags != 0 {
				return fmt.Errorf("sensitive file %q contains unexpected inheritance flags", path)
			}
			if !isFullAccess(ace.Mask) {
				return fmt.Errorf("sensitive path %q has non-full access ACE mask %#x", path, uint32(ace.Mask))
			}
			if seen[sidString]&1 != 0 {
				return fmt.Errorf("sensitive file %q contains duplicate principal %s", path, sidString)
			}
			seen[sidString] |= 1
		}
	}
	if len(seen) != len(expected) {
		missing := make([]string, 0, len(expected)-len(seen))
		for sid := range expected {
			if _, ok := seen[sid]; !ok {
				missing = append(missing, sid)
			}
		}
		sort.Strings(missing)
		return fmt.Errorf("sensitive path %q is missing principals: %s", path, strings.Join(missing, ", "))
	}
	if directory {
		for sid, roles := range seen {
			if roles != 3 {
				return fmt.Errorf("sensitive directory %q has incomplete ACL roles for principal %s", path, sid)
			}
		}
	}
	return nil
}

func isFullAccess(mask windows.ACCESS_MASK) bool {
	return mask == windows.ACCESS_MASK(windows.GENERIC_ALL) || mask == fullFileAccess
}

func requiredPrincipals() ([]Principal, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("resolve current user for ACL protection: %w", err)
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, fmt.Errorf("resolve SYSTEM SID: %w", err)
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, fmt.Errorf("resolve Administrators SID: %w", err)
	}
	principals := []Principal{
		{SID: user.User.Sid, Name: "current user", TrusteeType: windows.TRUSTEE_IS_USER},
		{SID: systemSID, Name: "SYSTEM", TrusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP},
		{SID: adminSID, Name: "Administrators", TrusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP},
	}
	seen := make(map[string]struct{}, len(principals))
	unique := principals[:0]
	for _, principal := range principals {
		if principal.SID == nil {
			return nil, errors.New("resolved nil SID for sensitive ACL")
		}
		key := principal.SID.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, principal)
	}
	return unique, nil
}
