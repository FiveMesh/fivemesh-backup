//go:build windows

package winsec

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestProtectAndVerifyFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sensitive data Ω")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "credentials Ω.toml")
	if err := os.WriteFile(path, []byte("password = \"not-a-real-password\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ProtectFile(path); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(path); err != nil {
		t.Fatal(err)
	}
	assertOnlyExpectedPrincipals(t, path)
}

func assertOnlyExpectedPrincipals(t *testing.T, path string) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	owner, defaulted, err := descriptor.Owner()
	if err != nil {
		t.Fatal(err)
	}
	currentUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if owner == nil || defaulted || !owner.Equals(currentUser.User.Sid) {
		t.Fatalf("sensitive path owner is not the current account: %v", owner)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	actualSet := make(map[string]struct{})
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		actualSet[sid.String()] = struct{}{}
	}
	actual := make([]string, 0, len(actualSet))
	for sid := range actualSet {
		actual = append(actual, sid)
	}
	principals, err := requiredPrincipals()
	if err != nil {
		t.Fatal(err)
	}
	expected := make([]string, 0, len(principals))
	for _, principal := range principals {
		expected = append(expected, principal.SID.String())
	}
	sort.Strings(actual)
	sort.Strings(expected)
	if strings.Join(actual, ",") != strings.Join(expected, ",") {
		t.Fatalf("unexpected ACL principals: actual=%v expected=%v", actual, expected)
	}
}

func TestProtectAndVerifyDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config data Ω")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ProtectDirectory(root); err != nil {
		t.Fatal(err)
	}
	if err := VerifyDirectory(root); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "child.txt")
	if err := os.WriteFile(child, []byte("child"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ProtectFile(child); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(child); err != nil {
		t.Fatal(err)
	}
}

func TestProtectRepairsBroadACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wide credentials.toml")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setEveryoneFullControl(path); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(path); err == nil {
		t.Fatal("broad ACL unexpectedly passed verification")
	}
	if err := ProtectFile(path); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(path); err != nil {
		t.Fatal(err)
	}
}

func TestProtectDirectoryRepairsBroadACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wide config Ω")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := setEveryoneFullControl(path); err != nil {
		t.Fatal(err)
	}
	if err := VerifyDirectory(path); err == nil {
		t.Fatal("broad directory ACL unexpectedly passed verification")
	}
	if err := ProtectDirectory(path); err != nil {
		t.Fatal(err)
	}
	if err := VerifyDirectory(path); err != nil {
		t.Fatal(err)
	}
	assertOnlyExpectedPrincipals(t, path)
}

func TestProtectDoesNotChangeParentDirectoryACL(t *testing.T) {
	parent := t.TempDir()
	before := securityDescriptorString(t, parent)
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ProtectDirectory(child); err != nil {
		t.Fatal(err)
	}
	after := securityDescriptorString(t, parent)
	if before != after {
		t.Fatalf("parent ACL changed:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestProtectMissingPathFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.toml")
	if err := ProtectFile(path); err == nil {
		t.Fatal("protect succeeded for a missing file")
	}
	if err := VerifyFile(path); err == nil {
		t.Fatal("verification succeeded for a missing file")
	}
}

func TestProtectRejectsReparsePoints(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "actual credentials")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "redirected credentials")
	if err := os.Symlink(target, link); err != nil {
		output, junctionErr := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).CombinedOutput()
		if junctionErr != nil {
			t.Skipf("Windows reparse-point creation is unavailable: symlink=%v junction=%v output=%s", err, junctionErr, output)
		}
	}
	if err := ProtectDirectory(link); err == nil || !strings.Contains(err.Error(), "reparse point") {
		t.Fatalf("reparse point was not rejected safely: %v", err)
	}
	if err := VerifyDirectory(link); err == nil || !strings.Contains(err.Error(), "reparse point") {
		t.Fatalf("reparse point verification did not fail safely: %v", err)
	}
}

func TestProtectFailsClosedOnAccessDenied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access-denied.toml")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := setNamedSecurityInfo
	defer func() { setNamedSecurityInfo = original }()
	setNamedSecurityInfo = func(string, windows.SE_OBJECT_TYPE, windows.SECURITY_INFORMATION, *windows.SID, *windows.SID, *windows.ACL, *windows.ACL) error {
		return windows.ERROR_ACCESS_DENIED
	}
	if err := ProtectFile(path); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("ACL access denial did not fail closed: %v", err)
	}
}

func setEveryoneFullControl(path string) error {
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		return err
	}
	var pinner runtime.Pinner
	defer pinner.Unpin()
	pinner.Pin(everyone)
	entries := []windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(everyone),
		},
	}}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	)
}

func securityDescriptorString(t *testing.T, path string) string {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	return descriptor.String()
}
