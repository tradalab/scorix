package app

import (
	"context"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/webview"
)

// Capture returns what the window's page shows as a PNG. It faults unavailable
// when the driver cannot draw the page to an image.
func (aw *AppWindow) Capture(ctx context.Context) ([]byte, error) {
	c, ok := aw.View().(webview.Capturer)
	if !ok {
		return nil, fault.New(fault.CodeUnavailable, "win: this driver cannot capture the page")
	}
	return c.Capture(ctx)
}
