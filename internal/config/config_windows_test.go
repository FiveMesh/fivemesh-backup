//go:build windows

package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/FiveMesh/fivemesh-backup/internal/winsec"
	"golang.org/x/sys/windows"
)

func TestWindowsSensitivePersistenceHasVerifiedACLs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "FiveMesh secure config Ω")
	paths := Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "separate credentials Ω", "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	cfg := validConfig()
	cfg.Profiles["production"] = withBackupDirectory(cfg.Profiles["production"], filepath.Join(root, "backups"))
	credentials := CredentialFile{Profiles: map[string]Credential{
		"production": {Password: "acl-test-secret"},
	}}
	if err := SaveInitial(cfg, credentials, paths); err != nil {
		t.Fatal(err)
	}
	assertWindowsACLs(t, paths)

	temp, err := writeTemporary(paths.Credentials, []byte("temporary secret"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := winsec.VerifyFile(temp); err != nil {
		t.Fatalf("temporary sensitive file ACL: %v", err)
	}
	if err := os.Remove(temp); err != nil {
		t.Fatal(err)
	}
	if matches, _ := filepath.Glob(filepath.Join(filepath.Dir(paths.Credentials), ".credentials.toml.partial-*")); len(matches) != 0 {
		t.Fatalf("temporary sensitive files remain: %v", matches)
	}
}

func TestWindowsReconfigureAndRollbackPreserveACLs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "FiveMesh rollback Ω")
	paths := Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	cfg := validConfig()
	cfg.Profiles["production"] = withBackupDirectory(cfg.Profiles["production"], filepath.Join(root, "backups"))
	if err := SaveInitial(cfg, CredentialFile{Profiles: map[string]Credential{
		"production": {Password: "old-secret"},
	}}, paths); err != nil {
		t.Fatal(err)
	}

	originalReplace := replaceFileAtomic
	defer func() { replaceFileAtomic = originalReplace }()
	failed := false
	replaceFileAtomic = func(oldPath, newPath string) error {
		if !failed && newPath == paths.Config {
			failed = true
			return errors.New("injected configuration replacement failure")
		}
		return originalReplace(oldPath, newPath)
	}
	updated := cfg
	profile := updated.Profiles["production"]
	profile.Description = "updated"
	updated.Profiles["production"] = profile
	err := SaveReconfigured(updated, CredentialFile{Profiles: map[string]Credential{
		"production": {Password: "new-secret"},
	}}, paths)
	if err == nil {
		t.Fatal("expected injected replacement failure")
	}
	password, err := LoadCredential(paths.Credentials, "production")
	if err != nil || password != "old-secret" {
		t.Fatalf("prior credential was not restored safely: %q %v", password, err)
	}
	assertWindowsACLs(t, paths)
	if matches, _ := filepath.Glob(filepath.Join(root, ".*.partial-*")); len(matches) != 0 {
		t.Fatalf("rollback left temporary files: %v", matches)
	}
}

func TestWindowsCanceledReconfigureRestoresACLsAndTemporaries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "FiveMesh canceled rollback Î©")
	paths := Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	cfg := validConfig()
	cfg.Profiles["production"] = withBackupDirectory(cfg.Profiles["production"], filepath.Join(root, "backups"))
	if err := SaveInitial(cfg, CredentialFile{Profiles: map[string]Credential{
		"production": {Password: "prior-secret"},
	}}, paths); err != nil {
		t.Fatal(err)
	}

	originalReplace := replaceFileAtomic
	defer func() { replaceFileAtomic = originalReplace }()
	cancelled := false
	replaceFileAtomic = func(oldPath, newPath string) error {
		if !cancelled && newPath == paths.Config {
			cancelled = true
			return context.Canceled
		}
		return originalReplace(oldPath, newPath)
	}
	updated := cfg
	profile := updated.Profiles["production"]
	profile.Description = "must not survive cancellation"
	updated.Profiles["production"] = profile
	err := SaveReconfigured(updated, CredentialFile{Profiles: map[string]Credential{
		"production": {Password: "replacement-secret"},
	}}, paths)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	password, err := LoadCredential(paths.Credentials, "production")
	if err != nil || password != "prior-secret" {
		t.Fatalf("prior credential was not restored after cancellation: %q %v", password, err)
	}
	loaded, err := Load(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Profiles["production"].Description == "must not survive cancellation" {
		t.Fatal("canceled configuration was persisted")
	}
	assertWindowsACLs(t, paths)
	if matches, _ := filepath.Glob(filepath.Join(root, ".*.partial-*")); len(matches) != 0 {
		t.Fatalf("cancellation left temporary files: %v", matches)
	}
}

func TestWindowsLoadRepairsBroadCredentialACL(t *testing.T) {
	root := filepath.Join(t.TempDir(), "FiveMesh ACL repair Ω")
	paths := Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	cfg := validConfig()
	cfg.Profiles["production"] = withBackupDirectory(cfg.Profiles["production"], filepath.Join(root, "backups"))
	if err := SaveInitial(cfg, CredentialFile{Profiles: map[string]Credential{
		"production": {Password: "repair-secret"},
	}}, paths); err != nil {
		t.Fatal(err)
	}
	if err := setEveryoneFullControl(paths.Credentials); err != nil {
		t.Fatal(err)
	}
	if err := winsec.VerifyFile(paths.Credentials); err == nil {
		t.Fatal("broad credentials ACL unexpectedly verified")
	}
	password, err := LoadCredential(paths.Credentials, "production")
	if err != nil || password != "repair-secret" {
		t.Fatalf("credential repair failed: %q %v", password, err)
	}
	assertWindowsACLs(t, paths)
}

func assertWindowsACLs(t *testing.T, paths Paths) {
	t.Helper()
	for _, directory := range []string{
		filepath.Dir(paths.Config),
		filepath.Dir(paths.Credentials),
		paths.StateDir,
	} {
		if err := winsec.VerifyDirectory(directory); err != nil {
			t.Fatalf("directory ACL %q: %v", directory, err)
		}
	}
	for _, file := range []string{paths.Config, paths.Credentials} {
		if err := winsec.VerifyFile(file); err != nil {
			t.Fatalf("file ACL %q: %v", file, err)
		}
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
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(everyone),
		},
	}}, nil)
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
