//go:build android

package browser

import (
	"context"
	"errors"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/native"
)

// ACTION_VIEW, so a link another app claims (Play Store, maps) opens in that app.
func openURL(ctx context.Context, url string) error {
	err := native.Call(ctx, "browser", "open", map[string]string{"url": url}, nil)
	if errors.Is(err, native.ErrUnavailable) {
		return fault.New(fault.CodeUnavailable, "no native host to open the url")
	}
	var ne *native.Error
	if errors.As(err, &ne) {
		return fault.New(ne.Code, ne.Message)
	}
	return err
}

func reveal(string) error {
	return fault.New(fault.CodeUnavailable, "revealing a path is not supported on android")
}

func openPath(string) error {
	return fault.New(fault.CodeUnavailable, "opening a path is not supported on android")
}
