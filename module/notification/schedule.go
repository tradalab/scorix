package notification

import (
	"context"
	"fmt"

	"github.com/tradalab/scorix/fault"
)

type ScheduledNotify struct {
	// At is Unix milliseconds; a time already past is dropped.
	At      int64    `json:"at"`
	Title   string   `json:"title"`
	Text    string   `json:"text"`
	ID      string   `json:"id,omitempty"`
	Actions []Action `json:"actions,omitempty"`
}

type ScheduleRequest struct {
	Items []ScheduledNotify `json:"items"`
}

// Stored whole in the host's preferences and rewritten on every fire.
const maxScheduled = 500

// Schedule replaces every pending notification, so an app recomputes its list on each start
// rather than tracking what it set before; an empty list cancels them all. Android only.
// JS: scorix.invoke("mod:notification:Schedule", { items: [{ at: Date.now() + 3600e3, title: "...", text: "..." }] })
func (m *NotificationModule) Schedule(ctx context.Context, req ScheduleRequest) (string, error) {
	if len(req.Items) > maxScheduled {
		return "", fault.New("invalid_request", fmt.Sprintf("%d notifications, over the %d limit", len(req.Items), maxScheduled))
	}
	for _, it := range req.Items {
		if it.Title == "" && it.Text == "" {
			return "", fault.New("invalid_request", "a scheduled notification needs a title or a text")
		}
	}
	if err := schedule(ctx, req.Items, m.scheme); err != nil {
		return "", err
	}
	return "ok", nil
}
