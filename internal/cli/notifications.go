package cli

import (
	"fmt"
	"io"

	"github.com/FiveMesh/fivemesh-backup/internal/notify"
)

// notifyLoader is replaced in tests.
var notifyLoader = func(paths pathFlags) *notify.Notifier {
	return notify.Load(pathValues(paths))
}

// sendNotification delivers an event best-effort. Failures are reported as a
// warning and never change the command's result.
func sendNotification(paths pathFlags, event notify.Event, stderr io.Writer) {
	notifier := notifyLoader(paths)
	if notifier == nil {
		return
	}
	if err := notifier.Notify(event); err != nil {
		fmt.Fprintf(stderr, "warning: Discord notification was not delivered: %v\n", err)
	}
}
