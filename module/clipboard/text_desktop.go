//go:build !android

package clipboard

import (
	"context"
	"time"

	"github.com/atotto/clipboard"

	"github.com/tradalab/scorix/internal/nativeclip"
)

var (
	toolRead  = clipboard.ReadAll
	toolWrite = clipboard.WriteAll
)

func readText(context.Context) (string, error) {
	c := nativeclip.Installed()
	if c == nil {
		return toolRead()
	}
	text, err := c.ReadText()
	if err == nil {
		return text, nil
	}
	if viaTool, toolErr := toolRead(); toolErr == nil {
		return viaTool, nil
	}
	return "", err
}

func writeAll(text string) error {
	c := nativeclip.Installed()
	if c == nil {
		return toolWrite(text)
	}
	err := c.WriteText(text)
	if err == nil {
		return nil
	}
	if toolWrite(text) == nil {
		return nil
	}
	return err
}

func writeText(ctx context.Context, text string, _ bool, clearAfter time.Duration) error {
	if err := writeAll(text); err != nil {
		return err
	}
	if clearAfter > 0 {
		time.AfterFunc(clearAfter, func() {
			if now, err := readText(ctx); err == nil && now == text {
				_ = writeAll("")
			}
		})
	}
	return nil
}
