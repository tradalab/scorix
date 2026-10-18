//go:build !android

package clipboard

import (
	"context"
	"time"

	"github.com/atotto/clipboard"
)

func readText(context.Context) (string, error) { return clipboard.ReadAll() }

func writeText(_ context.Context, text string, _ bool, clearAfter time.Duration) error {
	if err := clipboard.WriteAll(text); err != nil {
		return err
	}
	if clearAfter > 0 {
		time.AfterFunc(clearAfter, func() {
			if now, err := clipboard.ReadAll(); err == nil && now == text {
				_ = clipboard.WriteAll("")
			}
		})
	}
	return nil
}
