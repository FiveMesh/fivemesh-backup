package recovery

import (
	"context"
	"time"
)

const DefaultTimeout = 10 * time.Second

// Context detaches cleanup from the cancelled user operation while retaining a
// strict upper bound for rollback work.
func Context(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), DefaultTimeout)
}
