//go:build darwin && !server

package notification

import "testing"

// A go test binary is not inside a .app, so the UserNotifications path must not
// be claimed. This is the same check that keeps currentNotificationCenter from
// raising an Objective-C exception, which would be a crash rather than an error.
func TestClickableNeedsABundle(t *testing.T) {
	if clickable(NotifyRequest{ID: "build-1"}, "myapp") {
		t.Fatal("outside a bundle the click cannot come back, so Notify must not promise it")
	}
	if clickable(NotifyRequest{}, "") {
		t.Fatal("no id, no click")
	}
}
