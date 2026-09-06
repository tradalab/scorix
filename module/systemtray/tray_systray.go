//go:build !server && (windows || linux)

package systemtray

import (
	"fmt"

	"github.com/energye/systray"
	"github.com/tradalab/scorix/logger"
)

func trayStart(m *SystemTrayModule) error {
	go systray.Run(m.onReady, m.onExit)
	return nil
}

func trayStop()               { systray.Quit() }
func traySetIcon(b []byte)    { systray.SetIcon(b) }
func traySetTitle(s string)   { systray.SetTitle(s) }
func traySetTooltip(s string) { systray.SetTooltip(s) }

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

	addNodes(m.nodes(), nil)
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
