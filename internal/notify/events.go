package notify

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/history"
)

// BackupEvent describes a finished backup run from its history record.
func BackupEvent(profileName string, profile config.Profile, record history.Record) Event {
	event := Event{
		Profile: profileName,
		Time:    record.FinishedAt,
		Fields: []Field{
			{Name: "Database", Value: databaseLabel(profile.Database), Inline: true},
			{Name: "Duration", Value: formatDuration(time.Duration(record.DurationMS) * time.Millisecond), Inline: true},
		},
	}
	if record.Status == history.StatusSuccess {
		event.Kind = BackupSucceeded
		event.Title = "Backup succeeded"
		event.Fields = append(event.Fields,
			Field{Name: "Size", Value: fmt.Sprintf("%s (%s of SQL)", formatBytes(record.ArchiveSize), formatBytes(record.UncompressedSize)), Inline: true},
			Field{Name: "Archive", Value: "`" + filepath.Base(record.Archive) + "`"},
		)
		if record.Verified {
			event.Summary = "Archive written and verified."
		}
		if record.RetentionDeleted > 0 {
			event.Fields = append(event.Fields, Field{
				Name:  "Retention",
				Value: fmt.Sprintf("Removed %d old archive(s), freed %s", record.RetentionDeleted, formatBytes(record.RetentionFreedBytes)),
			})
		}
		if record.RetentionError != "" {
			event.Fields = append(event.Fields, Field{Name: "Retention warning", Value: record.RetentionError})
		}
		return event
	}
	event.Kind = BackupFailed
	event.Title = "Backup failed"
	if record.Status == history.StatusInterrupted {
		event.Title = "Backup interrupted"
	}
	event.Error = record.Error
	if event.Error == "" {
		event.Error = "no error details were recorded"
	}
	event.Fields = append(event.Fields, Field{Name: "Stopped at", Value: record.Phase, Inline: true})
	if record.ID != "" {
		event.Fields = append(event.Fields, Field{Name: "Full log", Value: "`fivemesh-backup logs " + record.ID + "`"})
	}
	return event
}

// RestoreEvent describes a finished restore.
func RestoreEvent(profileName, archivePath, target string, duration time.Duration, err error) Event {
	event := Event{
		Profile: profileName,
		Time:    time.Now(),
		Fields: []Field{
			{Name: "Target database", Value: "`" + target + "`", Inline: true},
			{Name: "Duration", Value: formatDuration(duration), Inline: true},
			{Name: "Archive", Value: "`" + filepath.Base(archivePath) + "`"},
		},
	}
	if err != nil {
		event.Kind = RestoreFailed
		event.Title = "Restore failed"
		event.Error = err.Error()
		return event
	}
	event.Kind = RestoreSucceeded
	event.Title = "Restore completed"
	event.Summary = "The restore client finished without errors. Check your application data before going live."
	return event
}

// ConfigEvent describes a configuration or schedule change.
func ConfigEvent(profileName, title, summary string, fields ...Field) Event {
	return Event{Kind: ConfigChanged, Profile: profileName, Title: title, Summary: summary, Fields: fields, Time: time.Now()}
}

// ProfileFields summarises a profile for configuration events. It never
// includes credentials.
func ProfileFields(profile config.Profile) []Field {
	return []Field{
		{Name: "Database", Value: databaseLabel(profile.Database), Inline: true},
		{Name: "Schedule", Value: ScheduleLabel(profile.Schedule), Inline: true},
		{Name: "Backup folder", Value: "`" + profile.Backup.Directory + "`"},
	}
}

// TestEvent is sent when notifications are set up or tested.
func TestEvent(settings config.DiscordNotifications) Event {
	events := settings.Events
	if len(events) == 0 {
		events = config.AllNotifyEvents
	}
	return Event{
		Kind:    Test,
		Title:   "Notifications are working",
		Summary: "FiveMesh Backup will post here.",
		Fields:  []Field{{Name: "Events", Value: describeEvents(events)}},
		Time:    time.Now(),
	}
}

// ScheduleLabel is a short human description of a schedule.
func ScheduleLabel(schedule config.Schedule) string {
	switch schedule.Mode {
	case config.ScheduleModeWindows:
		windows := schedule.Windows
		label := "daily at " + windows.StartTime
		if windows.Frequency == config.WindowsFrequencyInterval {
			if windows.IntervalMinutes%60 == 0 {
				label = fmt.Sprintf("every %d h from %s", windows.IntervalMinutes/60, windows.StartTime)
			} else {
				label = fmt.Sprintf("every %d min from %s", windows.IntervalMinutes, windows.StartTime)
			}
		}
		if schedule.Paused {
			label += " (paused)"
		}
		return label
	case config.ScheduleModeSystemd:
		label := schedule.OnCalendar
		if schedule.Timezone != "" {
			label += " " + schedule.Timezone
		}
		if schedule.Paused {
			label += " (paused)"
		}
		return label
	default:
		return "manual only"
	}
}

func describeEvents(events []string) string {
	names := map[string]string{
		config.NotifySuccess: "successful backups",
		config.NotifyFailure: "failed backups",
		config.NotifyRestore: "restores",
		config.NotifyConfig:  "configuration changes",
	}
	label := ""
	for index, event := range events {
		if index > 0 {
			label += ", "
		}
		if name, ok := names[event]; ok {
			label += name
		} else {
			label += event
		}
	}
	return label
}

func databaseLabel(database config.Database) string {
	where := database.Socket
	if where == "" {
		where = net.JoinHostPort(database.Host, strconv.Itoa(database.Port))
	}
	return "`" + database.Database + "` on " + where
}

func formatBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	div, exp := int64(unit), 0
	for n := value / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(div), "KMGTPE"[exp])
}

func formatDuration(duration time.Duration) string {
	switch {
	case duration < time.Second:
		return duration.Round(time.Millisecond).String()
	case duration < time.Minute:
		return duration.Round(100 * time.Millisecond).String()
	default:
		return duration.Round(time.Second).String()
	}
}
