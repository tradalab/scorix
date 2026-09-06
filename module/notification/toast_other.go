//go:build !windows && !linux

package notification

import (
	"context"

	"github.com/ncruces/zenity"
)

// No click-callback path here yet: macOS needs an NSUserNotification bridge and
// the other targets have no notification service at all. The notification still
// shows, and Notify's caller is told the click will not come back.
func showToast(_ context.Context, _ appInfo, req NotifyRequest, _ string) error {
	return zenity.Notify(req.Text, zenity.Title(req.Title), levelIcon(req.Level))
}

func levelIcon(level string) zenity.Option {
	switch level {
	case "warning":
		return zenity.Icon(zenity.WarningIcon)
	case "error":
		return zenity.Icon(zenity.ErrorIcon)
	default:
		return zenity.Icon(zenity.InfoIcon)
	}
}
