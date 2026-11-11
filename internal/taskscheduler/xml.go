package taskscheduler

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
)

func (s *Scheduler) GenerateXML(profileName string, profile config.Profile) ([]byte, error) {
	if err := s.validateProfile(profileName, profile); err != nil {
		return nil, err
	}
	if !validPrincipal(s.CurrentUser) {
		return nil, fmt.Errorf("current Windows user cannot be used as a task principal")
	}
	name, err := s.taskName(profileName)
	if err != nil {
		return nil, err
	}
	boundary, err := nextStartBoundary(profile.Schedule.Windows.StartTime, s.now())
	if err != nil {
		return nil, err
	}
	arguments := buildArguments(profileName, s.Paths)
	workingDirectory := filepath.Dir(s.BinaryPath)
	logonType := xmlLogonType(profile.Schedule.Windows.EffectiveLogonMode())
	repetition := ""
	if profile.Schedule.Windows.Frequency == config.WindowsFrequencyInterval {
		repetition = fmt.Sprintf(
			"<Repetition><Interval>%s</Interval><Duration>P1D</Duration><StopAtDurationEnd>false</StopAtDurationEnd></Repetition>",
			xmlDuration(profile.Schedule.Windows.IntervalMinutes),
		)
	}
	var output bytes.Buffer
	output.WriteString(`<?xml version="1.0" encoding="UTF-16"?>` + "\n")
	output.WriteString(`<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">` + "\n")
	output.WriteString("  <RegistrationInfo><Description>")
	writeEscaped(&output, s.taskDescription(profileName, false))
	output.WriteString("</Description><URI>\\")
	writeEscaped(&output, name)
	output.WriteString("</URI></RegistrationInfo>\n")
	output.WriteString("  <Triggers><CalendarTrigger>")
	output.WriteString(repetition)
	output.WriteString("<StartBoundary>")
	output.WriteString(boundary.Format("2006-01-02T15:04:05"))
	output.WriteString("</StartBoundary><Enabled>true</Enabled><ScheduleByDay><DaysInterval>1</DaysInterval></ScheduleByDay></CalendarTrigger></Triggers>\n")
	output.WriteString("  <Principals><Principal id=\"Author\"><UserId>")
	writeEscaped(&output, s.CurrentUser)
	output.WriteString("</UserId><LogonType>")
	output.WriteString(logonType)
	output.WriteString("</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>\n")
	output.WriteString("  <Settings>")
	output.WriteString("<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>")
	output.WriteString("<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>")
	output.WriteString("<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>")
	output.WriteString("<AllowHardTerminate>true</AllowHardTerminate>")
	fmt.Fprintf(&output, "<StartWhenAvailable>%t</StartWhenAvailable>", profile.Schedule.Persistent)
	output.WriteString("<RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>")
	output.WriteString("<IdleSettings><StopOnIdleEnd>true</StopOnIdleEnd><RestartOnIdle>false</RestartOnIdle></IdleSettings>")
	output.WriteString("<AllowStartOnDemand>true</AllowStartOnDemand><Enabled>true</Enabled><Hidden>false</Hidden>")
	output.WriteString("<RunOnlyIfIdle>false</RunOnlyIfIdle><WakeToRun>false</WakeToRun>")
	output.WriteString("<ExecutionTimeLimit>PT0S</ExecutionTimeLimit><Priority>7</Priority></Settings>\n")
	output.WriteString("  <Actions Context=\"Author\"><Exec><Command>")
	writeEscaped(&output, s.BinaryPath)
	output.WriteString("</Command><Arguments>")
	writeEscaped(&output, arguments)
	output.WriteString("</Arguments><WorkingDirectory>")
	writeEscaped(&output, workingDirectory)
	output.WriteString("</WorkingDirectory></Exec></Actions>\n")
	output.WriteString("</Task>\n")
	return output.Bytes(), nil
}

func xmlLogonType(mode string) string {
	switch mode {
	case config.WindowsLogonPassword:
		return "Password"
	case config.WindowsLogonBackground:
		return "S4U"
	default:
		return "InteractiveToken"
	}
}

func buildArguments(profileName string, paths config.Paths) string {
	values := []string{
		"backup",
		"--profile", profileName,
		"--config", paths.Config,
		"--credentials", paths.Credentials,
		"--state-dir", paths.StateDir,
	}
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = quoteWindowsArgument(value)
	}
	return strings.Join(quoted, " ")
}

func quoteWindowsArgument(value string) string {
	if value == "" {
		return `""`
	}
	if !strings.ContainsAny(value, " \t\n\v\"") {
		return value
	}
	var output strings.Builder
	output.WriteByte('"')
	backslashes := 0
	for _, char := range value {
		switch char {
		case '\\':
			backslashes++
		case '"':
			output.WriteString(strings.Repeat(`\`, backslashes*2+1))
			output.WriteRune(char)
			backslashes = 0
		default:
			output.WriteString(strings.Repeat(`\`, backslashes))
			backslashes = 0
			output.WriteRune(char)
		}
	}
	output.WriteString(strings.Repeat(`\`, backslashes*2))
	output.WriteByte('"')
	return output.String()
}

func xmlDuration(minutes int) string {
	switch {
	case minutes == 24*60:
		return "P1D"
	case minutes%60 == 0:
		return fmt.Sprintf("PT%dH", minutes/60)
	default:
		return fmt.Sprintf("PT%dM", minutes)
	}
}

func nextStartBoundary(clock string, now time.Time) (time.Time, error) {
	hour, minute, err := config.ParseClock(clock)
	if err != nil {
		return time.Time{}, err
	}
	location := now.Location()
	candidate := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, location)
	if !candidate.After(now) {
		candidate = time.Date(now.Year(), now.Month(), now.Day()+1, hour, minute, 0, 0, location)
	}
	return candidate, nil
}

func writeEscaped(output *bytes.Buffer, value string) {
	_ = xml.EscapeText(output, []byte(value))
}
