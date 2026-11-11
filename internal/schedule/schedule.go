package schedule

import (
	"context"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
)

// Lifecycle is the scheduling surface used by the interactive reconfiguration
// transaction. Implementations own only scheduler registration and state.
type Lifecycle interface {
	Mode() string
	Manages(string) bool
	Validate(context.Context, string, config.Profile) error
	Enable(context.Context, string, config.Profile, ...LogonSecret) (string, error)
	Disable(context.Context, string) error
	Remove(context.Context, string) error
	Snapshot(context.Context, string) ([]byte, error)
	SnapshotRequiresPassword([]byte) bool
	Restore(context.Context, string, []byte, ...LogonSecret) error
	Inspect(context.Context, string, config.Profile) (bool, string, error)
}

// LogonSecret carries an ephemeral Windows password from a secure prompt to the
// native scheduler. Implementations must never persist or log it.
type LogonSecret struct {
	WindowsPassword string
}

func NeedsWindowsPassword(profiles ...config.Profile) bool {
	for _, profile := range profiles {
		if profile.Schedule.Mode == config.ScheduleModeWindows &&
			profile.Schedule.Windows.EffectiveLogonMode() == config.WindowsLogonPassword {
			return true
		}
	}
	return false
}

type Manager interface {
	Lifecycle
	Status(context.Context, string, config.Profile) Status
	Next(context.Context, string, config.Profile) (string, error)
	Run(context.Context, string) error
	Logs(context.Context, string, int) ([]byte, error)
}

type Status struct {
	Profile      string
	Backend      string
	Mode         string
	Enabled      string
	Active       string
	Next         string
	LastRun      string
	LastResult   string
	Resource     string
	ResourceKind string
	LogonMode    string
	UnitError    string
	Stale        bool
	QueryError   error

	// Linux compatibility fields retained for the existing schedule output.
	Service string
	Timer   string
}
