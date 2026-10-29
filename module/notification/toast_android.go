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
	_, err := hostapi.Notification.Show(ctx, content(req.ID, req.Title, req.Text, req.Actions, scheme))
	return mapErr(err)
}

func schedule(ctx context.Context, items []ScheduledNotify, scheme string) error {
	s := &hostapi.NotificationSchedule{}
	for _, it := range items {
		s.Items = append(s.Items, hostapi.ScheduledNotification{AtMs: it.At, Content: *content(it.ID, it.Title, it.Text, it.Actions, scheme)})
	}
	_, err := hostapi.Notification.Schedule(ctx, s)
	return mapErr(err)
}

func content(id, title, text string, actions []Action, scheme string) *hostapi.NotificationContent {
	c := &hostapi.NotificationContent{Key: id, Title: title, Text: text, Url: activationURL(scheme, id, DefaultAction)}
	if c.Url != "" {
		for _, a := range actions {
			c.Buttons = append(c.Buttons, hostapi.NotificationButton{Label: a.Label, Url: activationURL(scheme, id, a.Key)})
		}
	}
	return c
}

func mapErr(err error) error {
	if errors.Is(err, native.ErrUnavailable) {
		return fault.New(fault.CodeUnavailable, "no native host for notifications")
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
