//go:build server || !(windows || linux)

package systemtray

import (
	"github.com/tradalab/scorix/module"
	"github.com/tradalab/scorix/logger"
)

// SystemTrayModule is a no-op stub for headless/server and darwin builds.
type SystemTrayModule struct{}

type Option func(*SystemTrayModule)

// WithMenu is accepted for API compatibility; the stub renders no menu.
func WithMenu(items ...MenuItem) Option {
	return func(*SystemTrayModule) {}
}

// The icon parameter is accepted for API compatibility but unused.
func New(icon []byte, opts ...Option) *SystemTrayModule {
	return &SystemTrayModule{}
}

func (m *SystemTrayModule) Name() string { return "systemtray" }

func (m *SystemTrayModule) Version() string { return "1.0.0" }

func (m *SystemTrayModule) OnLoad(ctx *module.Context) error {
	logger.Info("[systemtray] stub loaded (no native tray on this platform/build)")
	return nil
}

func (m *SystemTrayModule) OnStart() error { return nil }

func (m *SystemTrayModule) OnStop() error { return nil }

func (m *SystemTrayModule) OnUnload() error { return nil }
