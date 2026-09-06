//go:build (!windows && !linux && !darwin) || (darwin && server)

package notification

import "context"

// No notification service to talk to on these targets, so the click never comes
// back and Notify's caller is told so.
func showToast(_ context.Context, _ appInfo, req NotifyRequest, _ string) error {
	return zenityNotify(req)
}

func clickable(NotifyRequest, string) bool { return false }

func prepare() {}
