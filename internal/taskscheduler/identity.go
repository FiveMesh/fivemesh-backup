package taskscheduler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/FiveMesh/fivemesh-backup/internal/schedule"
)

const taskDescriptionPrefix = "FiveMesh Backup profile "

type parsedTaskDefinition struct {
	RegistrationInfo struct {
		Description string `xml:"Description"`
		URI         string `xml:"URI"`
	} `xml:"RegistrationInfo"`
	Principals struct {
		Principal []struct {
			UserID    string `xml:"UserId"`
			LogonType string `xml:"LogonType"`
			RunLevel  string `xml:"RunLevel"`
		} `xml:"Principal"`
	} `xml:"Principals"`
	Settings struct {
		ExecutionTimeLimit      string `xml:"ExecutionTimeLimit"`
		MultipleInstancesPolicy string `xml:"MultipleInstancesPolicy"`
	} `xml:"Settings"`
	Actions struct {
		Exec []struct {
			Command          string `xml:"Command"`
			Arguments        string `xml:"Arguments"`
			WorkingDirectory string `xml:"WorkingDirectory"`
		} `xml:"Exec"`
	} `xml:"Actions"`
}

func (s *Scheduler) taskName(profile string) (string, error) {
	slug, err := taskSlug(profile)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(s.installationIdentity() + "\x00" + profile))
	return fmt.Sprintf("FiveMesh Backup %s %s", slug, hex.EncodeToString(hash[:8])), nil
}

func legacyTaskNames(profile string) ([]string, error) {
	slug, err := taskSlug(profile)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(profile))
	return []string{
		fmt.Sprintf("FiveMesh Backup %s %s", slug, hex.EncodeToString(hash[:8])),
		fmt.Sprintf("FiveMesh Backup %s %s", slug, hex.EncodeToString(hash[:4])),
	}, nil
}

func taskSlug(profile string) (string, error) {
	if strings.TrimSpace(profile) == "" {
		return "", errors.New("profile name is required for Windows tasks")
	}
	var slug strings.Builder
	for _, char := range strings.ToLower(profile) {
		switch {
		case char >= 'a' && char <= 'z':
			slug.WriteRune(char)
		case char >= '0' && char <= '9':
			slug.WriteRune(char)
		case char == '-', char == '_':
			slug.WriteRune(char)
		default:
			slug.WriteByte('-')
		}
	}
	name := strings.Trim(slug.String(), "-_")
	if name == "" {
		name = "profile"
	}
	if len(name) > 40 {
		name = name[:40]
	}
	return name, nil
}

func (s *Scheduler) installationIdentity() string {
	userIdentity := strings.TrimSpace(s.CurrentUserSID)
	if userIdentity == "" {
		userIdentity = strings.TrimSpace(s.CurrentUser)
	}
	configPath := canonicalPath(s.Paths.Config)
	hash := sha256.Sum256([]byte(configPath + "\x00" + strings.ToLower(userIdentity)))
	return hex.EncodeToString(hash[:8])
}

func (s *Scheduler) taskDescription(profileName string, legacy bool) string {
	description := taskDescriptionPrefix + profileName
	if !legacy {
		description += " installation " + s.installationIdentity()
	}
	return description
}

func (s *Scheduler) isManagedTask(data []byte, name, profileName string, legacy bool) bool {
	definition, err := parseTaskDefinition(data)
	if err != nil {
		return false
	}
	if definition.RegistrationInfo.Description != s.taskDescription(profileName, legacy) ||
		definition.RegistrationInfo.URI != `\`+name {
		return false
	}
	if len(definition.Principals.Principal) != 1 {
		return false
	}
	principal := definition.Principals.Principal[0]
	if !s.matchesCurrentUser(principal.UserID) ||
		!knownLogonType(principal.LogonType) ||
		(principal.RunLevel != "" && principal.RunLevel != "LeastPrivilege") {
		return false
	}
	if definition.Settings.MultipleInstancesPolicy != "IgnoreNew" ||
		definition.Settings.ExecutionTimeLimit != "PT0S" {
		return false
	}
	if len(definition.Actions.Exec) != 1 {
		return false
	}
	action := definition.Actions.Exec[0]
	if !sameWindowsPath(action.Command, s.BinaryPath) ||
		!sameWindowsPath(action.WorkingDirectory, filepath.Dir(s.BinaryPath)) ||
		action.Arguments != buildArguments(profileName, s.Paths) {
		return false
	}
	return true
}

func (s *Scheduler) registrationForTask(data []byte, secret schedule.LogonSecret) (taskRegistration, error) {
	definition, err := parseTaskDefinition(data)
	if err != nil {
		return taskRegistration{}, fmt.Errorf("parse generated task identity: %w", err)
	}
	if len(definition.Principals.Principal) != 1 {
		return taskRegistration{}, errors.New("generated task must have exactly one principal")
	}
	switch definition.Principals.Principal[0].LogonType {
	case "InteractiveToken":
		return taskRegistration{LogonType: taskLogonInteractiveToken}, nil
	case "S4U":
		if s.CurrentUserSID == "" {
			return taskRegistration{}, errors.New("background mode requires the current Windows user SID")
		}
		return taskRegistration{
			LogonType:          taskLogonS4U,
			UserID:             s.CurrentUser,
			SecurityDescriptor: backgroundTaskSDDL(s.CurrentUserSID),
		}, nil
	case "Password":
		if secret.WindowsPassword == "" {
			return taskRegistration{}, errors.New("Windows password is required for unattended Task Scheduler mode")
		}
		return taskRegistration{
			LogonType: taskLogonPassword,
			UserID:    s.CurrentUser,
			Password:  secret.WindowsPassword,
		}, nil
	default:
		return taskRegistration{}, errors.New("generated task has an unsupported logon mode")
	}
}

func taskLogonMode(data []byte) string {
	definition, err := parseTaskDefinition(data)
	if err != nil || len(definition.Principals.Principal) != 1 {
		return "unknown"
	}
	switch definition.Principals.Principal[0].LogonType {
	case "InteractiveToken":
		return "interactive"
	case "Password":
		return "password"
	case "S4U":
		return "background"
	default:
		return "unknown"
	}
}

func knownLogonType(value string) bool {
	switch value {
	case "InteractiveToken", "Password", "S4U":
		return true
	}
	return false
}

// backgroundTaskSDDL grants Administrators, SYSTEM, and the owning account full
// control of the task object. Only creating or updating an S4U task requires
// elevation; with this descriptor the owner can query, run, enable, disable,
// and delete it from a normal console.
func backgroundTaskSDDL(userSID string) string {
	return "D:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;FA;;;" + userSID + ")"
}

func parseTaskDefinition(data []byte) (parsedTaskDefinition, error) {
	data = bytes.TrimSpace(data)
	if bytes.HasPrefix(data, []byte("<?xml")) {
		if end := bytes.Index(data, []byte("?>")); end >= 0 {
			data = bytes.TrimSpace(data[end+2:])
		}
	}
	var definition parsedTaskDefinition
	if err := xml.Unmarshal(data, &definition); err != nil {
		return parsedTaskDefinition{}, err
	}
	return definition, nil
}

func (s *Scheduler) matchesCurrentUser(userID string) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}
	return (s.CurrentUserSID != "" && strings.EqualFold(userID, s.CurrentUserSID)) ||
		(s.CurrentUser != "" && strings.EqualFold(userID, s.CurrentUser))
}

func sameWindowsPath(actual, expected string) bool {
	actual = strings.TrimSpace(actual)
	expected = strings.TrimSpace(expected)
	if actual == "" || expected == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(actual), filepath.Clean(expected))
}

func canonicalPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	absolute = filepath.Clean(absolute)
	if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
		return strings.ToLower(filepath.Clean(resolved))
	}
	current := absolute
	var suffix []string
	for {
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
		if resolved, resolveErr := filepath.EvalSymlinks(current); resolveErr == nil {
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return strings.ToLower(filepath.Clean(resolved))
		}
	}
	return strings.ToLower(filepath.Clean(absolute))
}

// profileFromTask recovers the profile name from a generated task description.
// Callers must still verify the task with isManagedTask.
func profileFromTask(data []byte) (string, error) {
	definition, err := parseTaskDefinition(data)
	if err != nil {
		return "", fmt.Errorf("parse generated task: %w", err)
	}
	description := definition.RegistrationInfo.Description
	if !strings.HasPrefix(description, taskDescriptionPrefix) {
		return "", errors.New("generated task is not a FiveMesh Backup task")
	}
	rest := strings.TrimPrefix(description, taskDescriptionPrefix)
	index := strings.LastIndex(rest, " installation ")
	if index <= 0 {
		return "", errors.New("generated task has no installation identity")
	}
	return rest[:index], nil
}
