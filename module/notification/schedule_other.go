//go:build !android

package notification

import (
	"context"

	"github.com/tradalab/scorix/fault"
)

// A desktop app is not running at the hour, and its OS has no store to leave a notification in.
func schedule(context.Context, []ScheduledNotify, string) error {
	return fault.New(fault.CodeUnavailable, "scheduled notifications exist only on Android")
}
