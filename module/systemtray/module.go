//go:build !server

// Package systemtray provides a system tray integration module for scorix applications.
//
// Enable in app.yaml:
//
//	modules:
//	  systemtray:
//	    enabled: true
//	    title: "MyApp"
//	    tooltip: "My Application"
package systemtray

import (
	"context"
	"fmt"

	"github.com/energye/systray"
	"github.com/tradalab/scorix/kernel/core/module"
	"github.com/tradalab/scorix/logger"
)

// Config holds the config block for this module.
// Fields are read from app.yaml → modules.systemtray.*
type Config struct {
	Title   string `json:"title"`
	Tooltip string `json:"tooltip"`
}

// ////////// Module ////////// ////////// ////////// ////////// ////////// //////////

// SystemTrayModule provides system tray icon and menu functionality.
type SystemTrayModule struct {
	ctx  *module.Context
	cfg  Config
	icon []byte
}

// New creates a new SystemTrayModule.
// icon is the tray icon data (typically from go:embed).
func New(icon []byte) *SystemTrayModule {
	return &SystemTrayModule{icon: icon}
}

func (m *SystemTrayModule) Name() string    { return "systemtray" }
func (m *SystemTrayModule) Version() string { return "1.0.0" }

// ////////// Lifecycle ////////// ////////// ////////// ////////// ////////// //////////

func (m *SystemTrayModule) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[systemtray] loading (v%s)", m.Version()))

	m.ctx = ctx

	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}

	// Fallback to app name if not configured.
	if m.cfg.Title == "" {
		m.cfg.Title = ctx.AppName
	}
	if m.cfg.Tooltip == "" {
		m.cfg.Tooltip = ctx.AppName
	}

	// Register IPC handlers.
	module.Expose(m, "SetIcon", ctx.IPC)
	module.Expose(m, "SetTooltip", ctx.IPC)
	module.Expose(m, "SetTitle", ctx.IPC)

	return nil
}

func (m *SystemTrayModule) OnStart() error {
	logger.Info("[systemtray] starting system tray")

	go systray.Run(m.onReady, m.onExit)

	return nil
}

func (m *SystemTrayModule) OnStop() error {
	logger.Info("[systemtray] stopping")
	systray.Quit()
	return nil
}

func (m *SystemTrayModule) OnUnload() error { return nil }

// ////////// System Tray Setup ////////// ////////// ////////// ////////// //////////

func (m *SystemTrayModule) onReady() {
	if len(m.icon) > 0 {
		systray.SetIcon(m.icon)
	}
	systray.SetTitle(m.cfg.Title)
	systray.SetTooltip(m.cfg.Tooltip)

	// Click handlers — show app on click/double-click.
	systray.SetOnClick(func(menu systray.IMenu) {
		if m.ctx.App != nil {
			m.ctx.App.Show()
		}
	})
	systray.SetOnDClick(func(menu systray.IMenu) {
		if m.ctx.App != nil {
			m.ctx.App.Show()
		}
	})
	systray.SetOnRClick(func(menu systray.IMenu) {
		if err := menu.ShowMenu(); err != nil {
			logger.Error(fmt.Sprintf("[systemtray] show menu error: %v", err))
		}
	})

	// Built-in menu items.
	systray.AddMenuItem("Open", "Open Application").Click(func() {
		if m.ctx.App != nil {
			m.ctx.App.Show()
		}
	})
	systray.AddMenuItem("Quit", "Quit Application").Click(func() {
		if m.ctx.App != nil {
			m.ctx.App.Close()
		}
	})
}

func (m *SystemTrayModule) onExit() {
	logger.Info("[systemtray] tray exited")
}

// ////////// IPC Handlers ////////// ////////// ////////// ////////// ////////// //////////

// SetIconRequest is the IPC payload for SetIcon.
type SetIconRequest struct {
	Icon []byte `json:"icon"`
}

// SetIcon updates the tray icon at runtime.
// JS: scorix.invoke("mod:systemtray:SetIcon", { icon: <base64-encoded-bytes> })
func (m *SystemTrayModule) SetIcon(_ context.Context, req SetIconRequest) (interface{}, error) {
	if len(req.Icon) == 0 {
		return nil, fmt.Errorf("icon data is empty")
	}
	systray.SetIcon(req.Icon)
	return "ok", nil
}

// SetTooltipRequest is the IPC payload for SetTooltip.
type SetTooltipRequest struct {
	Tooltip string `json:"tooltip"`
}

// SetTooltip updates the tray tooltip at runtime.
// JS: scorix.invoke("mod:systemtray:SetTooltip", { tooltip: "New tooltip" })
func (m *SystemTrayModule) SetTooltip(_ context.Context, req SetTooltipRequest) (interface{}, error) {
	systray.SetTooltip(req.Tooltip)
	return "ok", nil
}

// SetTitleRequest is the IPC payload for SetTitle.
type SetTitleRequest struct {
	Title string `json:"title"`
}

// SetTitle updates the tray title at runtime.
// JS: scorix.invoke("mod:systemtray:SetTitle", { title: "New Title" })
func (m *SystemTrayModule) SetTitle(_ context.Context, req SetTitleRequest) (interface{}, error) {
	systray.SetTitle(req.Title)
	return "ok", nil
}
