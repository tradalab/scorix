//go:build server

package systemtray

import (
	"github.com/tradalab/scorix/kernel/core/module"
	"github.com/tradalab/scorix/logger"
)

// SystemTrayModule is a no-op stub for headless/server builds.
type SystemTrayModule struct{}

// New creates a no-op SystemTrayModule for server builds.
// The icon parameter is accepted for API compatibility but unused.
func New(icon []byte) *SystemTrayModule {
	return &SystemTrayModule{}
}

func (m *SystemTrayModule) Name() string    { return "systemtray" }

func (m *SystemTrayModule) Version() string { return "1.0.0" }

func (m *SystemTrayModule) OnLoad(ctx *module.Context) error {
	logger.Info("[systemtray] stub loaded (server mode, no-op)")
	return nil
}

func (m *SystemTrayModule) OnStart() error  { return nil }

func (m *SystemTrayModule) OnStop() error   { return nil }

func (m *SystemTrayModule) OnUnload() error { return nil }
