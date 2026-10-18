// Package biometric asks for a fingerprint, face or device PIN. Android only;
// desktop reports unavailable.
package biometric

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

func (m *Module) Name() string       { return "biometric" }
func (m *Module) Version() string    { return "1.0.0" }
func (m *Module) Capability() string { return "biometric" }

func (m *Module) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[biometric] loading (v%s)", m.Version()))
	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	module.Expose(m, "Status", ctx.IPC)
	module.Expose(m, "Authenticate", ctx.IPC)
	return nil
}

func (m *Module) OnStart() error  { return nil }
func (m *Module) OnStop() error   { return nil }
func (m *Module) OnUnload() error { return nil }

type Status struct {
	Biometric        bool `json:"biometric"`
	DeviceCredential bool `json:"deviceCredential"`
}

// JS: scorix.invoke("mod:biometric:Status") -> {biometric, deviceCredential}
func (m *Module) Status(ctx context.Context, _ struct{}) (*Status, error) {
	var s Status
	if err := native.Call(ctx, "biometric", "status", nil, &s); err != nil {
		return nil, mapErr(err)
	}
	return &s, nil
}

type Prompt struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	// Nil means true: without the PIN, a phone with no fingerprint enrolled could never unlock.
	AllowDeviceCredential *bool  `json:"allowDeviceCredential,omitempty"`
	CancelLabel           string `json:"cancelLabel,omitempty"`
}

// Fault codes: canceled, lockout, unavailable, failed.
// JS: scorix.invoke("mod:biometric:Authenticate", { title: "Unlock" })
func (m *Module) Authenticate(ctx context.Context, p Prompt) (string, error) {
	if err := native.Call(ctx, "biometric", "authenticate", p, nil); err != nil {
		return "", mapErr(err)
	}
	return "ok", nil
}

func mapErr(err error) error {
	if errors.Is(err, native.ErrUnavailable) {
		return fault.New(fault.CodeUnavailable, "biometric authentication is not available on this platform")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fault.Wrap(fault.CodeCanceled, err)
	}
	var ne *native.Error
	if errors.As(err, &ne) {
		return fault.New(ne.Code, ne.Message)
	}
	return err
}
