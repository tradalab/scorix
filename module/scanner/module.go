// Package scanner reads QR codes on Android; desktop reports unavailable.
// The app must declare android.permission.CAMERA itself.
package scanner

import (
	"context"
	"errors"
	"fmt"

	"github.com/tradalab/scorix/fault"
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

func (m *Module) Name() string       { return "scanner" }
func (m *Module) Version() string    { return "1.0.0" }
func (m *Module) Capability() string { return "camera" }

func (m *Module) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[scanner] loading (v%s)", m.Version()))
	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	module.Expose(m, "ScanQR", ctx.IPC)
	module.Expose(m, "ScanImage", ctx.IPC)
	return nil
}

func (m *Module) OnStart() error  { return nil }
func (m *Module) OnStop() error   { return nil }
func (m *Module) OnUnload() error { return nil }

// ScanImage covers what the camera cannot: a code already on this phone's own screen.
// JS: scorix.invoke("mod:scanner:ScanImage") -> "otpauth://totp/..."
func (m *Module) ScanImage(ctx context.Context, _ struct{}) (string, error) {
	return m.call(ctx, "scanImage")
}

// JS: scorix.invoke("mod:scanner:ScanQR") -> "otpauth://totp/..."
func (m *Module) ScanQR(ctx context.Context, _ struct{}) (string, error) {
	return m.call(ctx, "scanQR")
}

func (m *Module) call(ctx context.Context, method string) (string, error) {
	var out struct {
		Text string `json:"text"`
	}
	err := native.Call(ctx, "scanner", method, nil, &out)
	switch {
	case err == nil:
		return out.Text, nil
	case native.IsCode(err, "canceled"):
		return "", nil
	case native.IsCode(err, "not_found"):
		return "", fault.New(fault.CodeNotFound, "no QR code in that image")
	case errors.Is(err, native.ErrUnavailable):
		return "", fault.New(fault.CodeUnavailable, "QR scanning is not available on this platform")
	}
	var ne *native.Error
	if errors.As(err, &ne) {
		return "", fault.New(ne.Code, ne.Message)
	}
	return "", err
}
