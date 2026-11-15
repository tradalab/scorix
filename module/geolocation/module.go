// Package geolocation reads one position fix on Android; desktop reports unavailable.
// The app must declare android.permission.ACCESS_FINE_LOCATION or ACCESS_COARSE_LOCATION itself.
package geolocation

import (
	"context"
	"errors"
	"fmt"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/internal/hostapi"
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
	"github.com/tradalab/scorix/native"
)

type Config struct {
	Enabled bool `json:"enabled"`
}

type Module struct {
	cfg Config
}

func New() *Module { return &Module{} }

func (m *Module) Name() string       { return "geolocation" }
func (m *Module) Version() string    { return "1.0.0" }
func (m *Module) Capability() string { return "geolocation" }

func (m *Module) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[geolocation] loading (v%s)", m.Version()))
	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	module.Expose(m, "Current", ctx.IPC)
	return nil
}

type Request struct {
	MaxAgeMs  int64 `json:"maxAgeMs,omitempty"`
	TimeoutMs int64 `json:"timeoutMs,omitempty"`
	Coarse    bool  `json:"coarse,omitempty"`
}

type Position struct {
	Latitude   float64 `json:"latitude"`
	Longitude  float64 `json:"longitude"`
	AccuracyM  float64 `json:"accuracyM"`
	AltitudeM  float64 `json:"altitudeM"`
	SpeedMps   float64 `json:"speedMps"`
	BearingDeg float64 `json:"bearingDeg"`
	HasMotion  bool    `json:"hasMotion"`
	TimeMs     int64   `json:"timeMs"`
	Provider   string  `json:"provider"`
}

const maxTimeoutMs = 5 * 60 * 1000

// Current asks for the permission first. Errors carry code denied, disabled (location off),
// timeout (no fix within timeoutMs, 30 s when unset) or unavailable.
// JS: scorix.invoke("mod:geolocation:Current", { maxAgeMs: 60000 }) -> { latitude, longitude, ... }
func (m *Module) Current(ctx context.Context, r Request) (Position, error) {
	if r.MaxAgeMs < 0 || r.TimeoutMs < 0 || r.TimeoutMs > maxTimeoutMs {
		return Position{}, fault.New("invalid_request", fmt.Sprintf("maxAgeMs and timeoutMs may not be negative, and timeoutMs is at most %d", maxTimeoutMs))
	}
	p, err := hostapi.Location.Current(ctx, &hostapi.LocationRequest{MaxAgeMs: r.MaxAgeMs, TimeoutMs: r.TimeoutMs, Coarse: r.Coarse})
	if err != nil {
		return Position{}, mapErr(err)
	}
	return Position{
		Latitude:   p.Latitude,
		Longitude:  p.Longitude,
		AccuracyM:  p.AccuracyM,
		AltitudeM:  p.AltitudeM,
		SpeedMps:   p.SpeedMps,
		BearingDeg: p.BearingDeg,
		HasMotion:  p.HasMotion,
		TimeMs:     p.TimeMs,
		Provider:   p.Provider,
	}, nil
}

func mapErr(err error) error {
	if errors.Is(err, native.ErrUnavailable) {
		return fault.New(fault.CodeUnavailable, "location is not available on this platform")
	}
	var ne *native.Error
	if errors.As(err, &ne) {
		return fault.New(ne.Code, ne.Message)
	}
	return err
}
