//go:build !windows && !linux

package notification

import "github.com/ncruces/zenity"

// zenityNotify is the lowest common denominator: it shows something and reports
// nothing back. macOS falls here when the binary is not inside a .app bundle.
func zenityNotify(req NotifyRequest) error {
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
