package app

import (
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/menu"
	"github.com/tradalab/scorix/window"
)

// SetMenu installs the menu bar on the main window; before Run it is kept and
// applied once the window exists.
func (a *App) SetMenu(m menu.Menu) {
	a.mu.Lock()
	a.menu = m
	a.menuSet = true
	main := a.main
	a.mu.Unlock()
	if main != nil {
		a.applyMenu(main, m)
	}
}

func (aw *AppWindow) SetMenu(m menu.Menu) { aw.app.applyMenu(aw, m) }

// PopupMenu shows a context menu at logical client coords; negative x/y means
// at the pointer. It returns before the user picks anything.
func (aw *AppWindow) PopupMenu(m menu.Menu, x, y int) { aw.app.popupMenu(aw, m, x, y) }

func (a *App) applyMenu(aw *AppWindow, m menu.Menu) {
	s, ok := aw.Window.(window.MenuBarSetter)
	if !ok {
		logger.Warn("app: menu bar is not supported by this driver")
		return
	}
	s.SetMenuBar(a.resolveMenu(aw, m))
}

func (a *App) popupMenu(aw *AppWindow, m menu.Menu, x, y int) {
	p, ok := aw.Window.(window.ContextMenuPopper)
	if !ok {
		logger.Warn("app: context menus are not supported by this driver")
		return
	}
	p.PopupMenu(a.resolveMenu(aw, m), x, y)
}

func (a *App) resolveMenu(aw *AppWindow, items menu.Menu) []window.MenuItem {
	out := make([]window.MenuItem, 0, len(items))
	for _, it := range items {
		if it.Separator {
			out = append(out, window.MenuItem{Separator: true})
			continue
		}
		mi := window.MenuItem{Label: it.Label, Disabled: it.Disabled, Checked: it.Checked, AccelHint: it.AccelHint}
		accel := it.Accelerator
		if spec, ok := it.Role.Spec(); ok {
			if mi.Label == "" {
				mi.Label = spec.Label
			}
			if accel == "" {
				accel = spec.Accel
			}
			mi.AccelHint = mi.AccelHint || spec.Editing
		} else if it.Role != "" {
			logger.Warn("app: unknown menu role", "role", string(it.Role), "label", it.Label)
		}
		if mi.Label == "" {
			logger.Warn("app: menu item without a label dropped", "id", it.ID, "role", string(it.Role))
			continue
		}
		if accel != "" {
			acc, err := window.ParseAccel(accel)
			switch {
			case err != nil:
				logger.Warn("app: menu accelerator rejected", "accel", accel, "err", err)
			case !mi.AccelHint && !acc.Ctrl && !acc.Alt && !acc.Super && !acc.IsFunctionKey():
				logger.Warn("app: menu accelerator dropped, it would swallow typing in the webview; set AccelHint to show it without binding", "accel", accel) // the table fires before the page sees the key
			default:
				mi.Accel = acc
			}
		}
		if len(it.Submenu) > 0 {
			mi.Submenu = a.resolveMenu(aw, it.Submenu)
			out = append(out, mi)
			continue
		}
		fn := it.OnClick
		if fn == nil {
			fn = a.roleAction(aw, it.Role)
		}
		if fn == nil {
			id, label := it.ID, mi.Label
			fn = func() { a.EmitTo(aw.Client, "sys:menu", map[string]string{"id": id, "label": label}) }
		}
		mi.OnClick = func() { go fn() } // drivers click on the UI thread; a handler that opens a window or dialog would deadlock it
		out = append(out, mi)
	}
	return out
}

var editCommands = map[menu.Role]window.EditCommand{
	menu.RoleUndo:      window.EditUndo,
	menu.RoleRedo:      window.EditRedo,
	menu.RoleCut:       window.EditCut,
	menu.RoleCopy:      window.EditCopy,
	menu.RolePaste:     window.EditPaste,
	menu.RoleSelectAll: window.EditSelectAll,
}

func (a *App) roleAction(aw *AppWindow, r menu.Role) func() {
	switch r {
	case menu.RoleQuit:
		return a.Quit
	case menu.RoleMinimize:
		return aw.Minimize
	case menu.RoleClose:
		return aw.Close
	case menu.RoleToggleFullscreen:
		return func() { aw.SetFullscreen(!aw.IsFullscreen()) }
	case menu.RoleDevTools:
		return aw.View().OpenDevTools
	case menu.RoleReload:
		return func() { aw.View().Eval("location.reload()") }
	}
	if cmd, ok := editCommands[r]; ok {
		return func() {
			if ec, ok := aw.Window.(window.EditCommander); ok {
				ec.EditCommand(cmd)
			}
		}
	}
	return nil
}
