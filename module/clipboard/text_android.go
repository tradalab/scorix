//go:build android

package clipboard

import (
	"context"
	"time"

	"github.com/tradalab/scorix/internal/hostapi"
)

func readText(ctx context.Context) (string, error) {
	out, err := hostapi.Clipboard.Read(ctx, &hostapi.Empty{})
	if err != nil {
		return "", err
	}
	return out.Text, nil
}

// The clear is timed by the host, not here: Go may be frozen with the app in
// the background, exactly when a copied code should expire.
func writeText(ctx context.Context, text string, sensitive bool, clearAfter time.Duration) error {
	_, err := hostapi.Clipboard.Write(ctx, &hostapi.ClipWrite{Text: text, Sensitive: sensitive, ClearAfterMs: clearAfter.Milliseconds()})
	return err
}
