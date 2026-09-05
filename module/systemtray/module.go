//go:build !server && (windows || linux)

// Package systemtray is a system tray integration module.
package systemtray

import (
	"context"
	"fmt"

	"github.com/energye/systray"
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/menu"
	"github.com/tradalab/scorix/module"
)

type Config struct {
	Title   string `json:"title"`
	Tooltip string `json:"tooltip"`
}

type SystemTrayModule struct {
	ctx  *module.Context
	cfg  Config
	icon []byte
	menu menu.Menu // nil → default Open/Quit menu
}

type Option func(*SystemTrayModule)

// WithMenu replaces the default Open/Quit menu. Click handlers run on the tray's
// goroutine - offload long work to another goroutine.
func WithMenu(items ...menu.Item) Option {
	return func(m *SystemTrayModule) { m.menu = items }
}

func New(icon []byte, opts ...Option) *SystemTrayModule {
	m := &SystemTrayModule{icon: icon}
	for _, o := range opts {
		o(m)
	}
	return m
}

func (m *SystemTrayModule) Name() string    { return "systemtray" }
func (m *SystemTrayModule) Version() string { return "1.0.0" }

func (m *SystemTrayModule) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[systemtray] loading (v%s)", m.Version()))

	m.ctx = ctx

	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}

	if m.cfg.Title == "" {
		m.cfg.Title = ctx.AppName
	}
	if m.cfg.Tooltip == "" {
		m.cfg.Tooltip = ctx.AppName
	}

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

func (m *SystemTrayModule) onReady() {
	if len(m.icon) > 0 {
		systray.SetIcon(m.icon)
	}
	systray.SetTitle(m.cfg.Title)
	systray.SetTooltip(m.cfg.Tooltip)

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

	items := m.menu
	if len(items) == 0 {
		items = defaultMenu()
	}
	addNodes(resolveTray(items, m.ctx.App, 0), nil)
}

// A nil parent is the top level, the only level systray lets a separator into.
func addNodes(nodes []node, parent *systray.MenuItem) {
	for _, n := range nodes {
		if n.separator {
			systray.AddSeparator()
			continue
		}
		var mi *systray.MenuItem
		switch { // the checkbox constructors reserve a check column, so only a checked item gets one
		case parent == nil && n.checked:
			mi = systray.AddMenuItemCheckbox(n.label, n.tooltip, true)
		case parent == nil:
			mi = systray.AddMenuItem(n.label, n.tooltip)
		case n.checked:
			mi = parent.AddSubMenuItemCheckbox(n.label, n.tooltip, true)
		default:
			mi = parent.AddSubMenuItem(n.label, n.tooltip)
		}
		if n.disabled {
			mi.Disable()
		}
		if n.onClick != nil {
			mi.Click(n.onClick)
		}
		addNodes(n.children, mi)
	}
}

func (m *SystemTrayModule) onExit() {
	logger.Info("[systemtray] tray exited")
}

type SetIconRequest struct {
	Icon []byte `json:"icon"`
}

// JS: scorix.invoke("mod:systemtray:SetIcon", { icon: <base64-encoded-bytes> })
func (m *SystemTrayModule) SetIcon(_ context.Context, req SetIconRequest) (interface{}, error) {
	if len(req.Icon) == 0 {
		return nil, fmt.Errorf("icon data is empty")
	}
	systray.SetIcon(req.Icon)
	return "ok", nil
}

type SetTooltipRequest struct {
	Tooltip string `json:"tooltip"`
}

// JS: scorix.invoke("mod:systemtray:SetTooltip", { tooltip: "New tooltip" })
func (m *SystemTrayModule) SetTooltip(_ context.Context, req SetTooltipRequest) (interface{}, error) {
	systray.SetTooltip(req.Tooltip)
	return "ok", nil
}

type SetTitleRequest struct {
	Title string `json:"title"`
}

// JS: scorix.invoke("mod:systemtray:SetTitle", { title: "New Title" })
func (m *SystemTrayModule) SetTitle(_ context.Context, req SetTitleRequest) (interface{}, error) {
	systray.SetTitle(req.Title)
	return "ok", nil
}

func (m *SystemTrayModule) Capability() string { return "tray" }
