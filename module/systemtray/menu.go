package systemtray

import (
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/menu"
	"github.com/tradalab/scorix/module"
)

// node is a resolved tray entry, so the platform binding stays a dumb walk.
type node struct {
	label     string
	tooltip   string
	checked   bool
	disabled  bool
	separator bool
	children  []node
	onClick   func()
}

// A tray has no keyboard and no window of its own, so resolveTray drops what it
// cannot honor WITH a warning: a menu that silently swallows half its input is
// worse than one that refuses it.
func resolveTray(items []menu.Item, app module.AppController, depth int) []node {
	if app == nil && depth == 0 {
		// Once here, not once per item: the cause is the missing controller, and
		// blaming each role would send the reader to the wrong file.
		logger.Warn("systemtray: no AppController (web/server mode), show/quit items will do nothing")
	}
	out := make([]node, 0, len(items))
	for _, it := range items {
		if it.Separator {
			if depth > 0 {
				logger.Warn("systemtray: separator dropped, systray has no separator inside a submenu")
				continue
			}
			out = append(out, node{separator: true})
			continue
		}
		n := node{label: it.Label, tooltip: it.Tooltip, checked: it.Checked, disabled: it.Disabled}
		spec, known := it.Role.Spec()
		switch {
		case known && n.label == "":
			n.label = spec.Label
		case !known && it.Role != "":
			logger.Warn("systemtray: unknown menu role", "role", string(it.Role), "label", it.Label)
		}
		if n.label == "" {
			logger.Warn("systemtray: menu item without a label dropped", "id", it.ID, "role", string(it.Role))
			continue
		}
		if it.Accelerator != "" {
			logger.Warn("systemtray: accelerator ignored, a tray menu has no keyboard shortcuts",
				"label", n.label, "accel", it.Accelerator)
		}
		if len(it.Submenu) > 0 {
			n.children = resolveTray(it.Submenu, app, depth+1)
			out = append(out, n)
			continue
		}
		n.onClick = it.OnClick
		if n.onClick == nil {
			n.onClick = trayAction(it.Role, app)
		}
		// A bare label with nothing behind it is display-only, which is supported.
		// An ID or a role behind nothing is a mistake: the tray menu is declared in
		// Go, so no sys:menu event catches the click the way the window bar does.
		if n.onClick == nil {
			switch {
			case it.ID != "":
				logger.Warn("systemtray: id-only item does nothing, the tray has no sys:menu fallback - set OnClick",
					"id", it.ID, "label", n.label)
			case known && !isTrayRole(it.Role): // a tray role with no app is reported once, above
				logger.Warn("systemtray: role has no tray action, item is display-only - set OnClick if it should do something",
					"role", string(it.Role), "label", n.label)
			}
		}
		out = append(out, n)
	}
	return out
}

// Roles other than these belong to a window (undo, reload, fullscreen) or have
// no framework action anywhere (about); the caller reports them.
func trayAction(r menu.Role, app module.AppController) func() {
	if app == nil {
		return nil
	}
	switch r {
	case menu.RoleShow:
		return app.Show
	case menu.RoleQuit:
		return app.Close
	}
	return nil
}

func isTrayRole(r menu.Role) bool { return r == menu.RoleShow || r == menu.RoleQuit }

func defaultMenu() menu.Menu {
	return menu.Menu{
		{Role: menu.RoleShow, Tooltip: "Open Application"},
		{Role: menu.RoleQuit, Tooltip: "Quit Application"},
	}
}
