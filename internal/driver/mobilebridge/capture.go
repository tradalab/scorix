package mobilebridge

import (
	"context"

	"github.com/tradalab/scorix/internal/hostapi"
)

func (v *view) Capture(ctx context.Context) ([]byte, error) {
	out, err := hostapi.Capture.Page(ctx, &hostapi.Empty{})
	if err != nil {
		return nil, err
	}
	return out.Png, nil
}
