//go:build android

package clipboard

import (
	"context"
	"time"

	"github.com/tradalab/scorix/native"
)

func readText(ctx context.Context) (string, error) {
	var out struct {
		Text string `json:"text"`
	}
	err := native.Call(ctx, "clipboard", "read", nil, &out)
	return out.Text, err
}

// The clear is timed by the host, not here: Go may be frozen with the app in
// the background, exactly when a copied code should expire.
func writeText(ctx context.Context, text string, sensitive bool, clearAfter time.Duration) error {
	return native.Call(ctx, "clipboard", "write", map[string]any{
		"text":         text,
		"sensitive":    sensitive,
		"clearAfterMs": clearAfter.Milliseconds(),
	}, nil)
}
