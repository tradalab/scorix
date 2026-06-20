//go:build server || !(windows || linux)

package systemtray

import (
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
)

// No-op stub for headless/server and darwin builds; args kept for API compatibility.
type SystemTrayModule struct{}

type Option func(*SystemTrayModule)

func WithMenu(items ...MenuItem) Option {
	return func(*SystemTrayModule) {}
}

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
