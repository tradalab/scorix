package notification

import (
	"context"
	"errors"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/internal/hostapi"
	"github.com/tradalab/scorix/native"
)

// A click comes back as the activation url on the app's scheme, through the deep-link path, as on
// Windows and Linux.
func showToast(ctx context.Context, _ appInfo, req NotifyRequest, scheme string) error {
	c := &hostapi.NotificationContent{Key: req.ID, Title: req.Title, Text: req.Text, Url: activationURL(scheme, req.ID, DefaultAction)}
	if c.Url != "" {
		for _, a := range req.Actions {
			c.Buttons = append(c.Buttons, hostapi.NotificationButton{Label: a.Label, Url: activationURL(scheme, req.ID, a.Key)})
		}
	}
	_, err := hostapi.Notification.Show(ctx, c)
	if errors.Is(err, native.ErrUnavailable) {
		return fault.New(fault.CodeUnavailable, "no native host to show the notification")
	}
	var ne *native.Error
	if errors.As(err, &ne) {
		return fault.New(ne.Code, ne.Message)
	}
	return err
}

func clickable(req NotifyRequest, scheme string) bool { return req.ID != "" && scheme != "" }

// The host makes the channel on first use.
func prepare() {}
