//go:build darwin && !ios

package wkwebview

import (
	"fmt"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego/objc"

	"github.com/tradalab/scorix/window"
)

const (
	nsControlStateOn int64 = 1

	nsTerminateCancel uint64 = 0

	popupActionGrace = time.Second
)

type menuBuild struct {
	ids []int64
}

var (
	menuMu      sync.Mutex
	menuSeq     int64
	menuActions = map[int64]func(){}
	barIDs      []int64

	menuTargetOnce sync.Once
	menuTargetObj  objc.ID
)

func (b *menuBuild) register(fn func()) int64 {
	menuMu.Lock()
	defer menuMu.Unlock()
	menuSeq++
	menuActions[menuSeq] = fn
	b.ids = append(b.ids, menuSeq)
	return menuSeq
}

func forgetActions(ids []int64) {
	menuMu.Lock()
	defer menuMu.Unlock()
	for _, id := range ids {
		delete(menuActions, id)
	}
}

func menuTarget() objc.ID {
	menuTargetOnce.Do(func() {
		_, err := objc.RegisterClass(
			"ScorixMenuTarget",
			cls("NSObject"),
			nil,
			nil,
			[]objc.MethodDef{{
				Cmd: sel("scorixMenuAction:"),
				Fn: func(_ objc.ID, _ objc.SEL, sender objc.ID) {
					defer recoverCB("scorixMenuAction:")
					tag := objc.Send[int64](sender, sel("tag"))
					menuMu.Lock()
					fn := menuActions[tag]
					menuMu.Unlock()
					if fn != nil {
						fn()
					}
				},
			}},
		)
		if err != nil {
			panic(fmt.Sprintf("wkwebview: register menu target: %v", err))
		}
		menuTargetObj = objc.ID(cls("ScorixMenuTarget")).Send(sel("new"))
	})
	return menuTargetObj
}

func newMenu(title string) objc.ID {
	m := objc.ID(cls("NSMenu")).Send(sel("alloc")).Send(sel("initWithTitle:"), nsString(title))
	m.Send(sel("setAutoenablesItems:"), false)
	return m
}

func newMenuItem(title, action, key string, mods uint64) objc.ID {
	var s objc.SEL
	if action != "" {
		s = sel(action)
	}
	item := objc.ID(cls("NSMenuItem")).Send(sel("alloc")).
		Send(sel("initWithTitle:action:keyEquivalent:"), nsString(title), s, nsString(key))
	if key != "" {
		item.Send(sel("setKeyEquivalentModifierMask:"), mods)
	}
	return item
}

func addOwned(m, item objc.ID) {
	m.Send(sel("addItem:"), item)
	item.Send(sel("release"))
}

func addSeparator(m objc.ID) {
	m.Send(sel("addItem:"), objc.ID(cls("NSMenuItem")).Send(sel("separatorItem")))
}

func addSubmenu(parent objc.ID, title string, sub objc.ID) objc.ID {
	item := newMenuItem(title, "", "", 0)
	item.Send(sel("setSubmenu:"), sub)
	sub.Send(sel("release"))
	addOwned(parent, item)
	return item
}

func (b *menuBuild) fill(m objc.ID, items []window.MenuItem) {
	for _, it := range items {
		if it.Separator {
			addSeparator(m)
			continue
		}
		if len(it.Submenu) > 0 {
			sub := newMenu(it.Label)
			b.fill(sub, it.Submenu)
			item := addSubmenu(m, it.Label, sub)
			item.Send(sel("setEnabled:"), !it.Disabled)
			continue
		}
		var key string
		var mods uint64
		if it.Edit != "" || !it.AccelHint {
			key, mods = keyEquivalent(it.Accel)
		}
		var item objc.ID
		if action, ok := editSelectors[it.Edit]; ok {
			item = newMenuItem(it.Label, action, key, mods)
		} else {
			item = newMenuItem(it.Label, "scorixMenuAction:", key, mods)
			item.Send(sel("setTarget:"), menuTarget())
			item.Send(sel("setTag:"), b.register(it.OnClick))
		}
		if it.Checked {
			item.Send(sel("setState:"), nsControlStateOn)
		}
		item.Send(sel("setEnabled:"), !it.Disabled && (it.Edit != "" || it.OnClick != nil))
		addOwned(m, item)
	}
}

func processName() string {
	return nsStringToGo(objc.ID(cls("NSProcessInfo")).Send(sel("processInfo")).Send(sel("processName")))
}

func appMenu(name string) objc.ID {
	m := newMenu(name)
	addOwned(m, newMenuItem("About "+name, "orderFrontStandardAboutPanel:", "", 0))
	addSeparator(m)
	addOwned(m, newMenuItem("Hide "+name, "hide:", "h", nsModCommand))
	addOwned(m, newMenuItem("Hide Others", "hideOtherApplications:", "h", nsModCommand|nsModOption))
	addOwned(m, newMenuItem("Show All", "unhideAllApplications:", "", 0))
	addSeparator(m)
	addOwned(m, newMenuItem("Quit "+name, "terminate:", "q", nsModCommand))
	return m
}

func editMenu() objc.ID {
	m := newMenu("Edit")
	addOwned(m, newMenuItem("Undo", "undo:", "z", nsModCommand))
	addOwned(m, newMenuItem("Redo", "redo:", "Z", nsModCommand))
	addSeparator(m)
	addOwned(m, newMenuItem("Cut", "cut:", "x", nsModCommand))
	addOwned(m, newMenuItem("Copy", "copy:", "c", nsModCommand))
	addOwned(m, newMenuItem("Paste", "paste:", "v", nsModCommand))
	addOwned(m, newMenuItem("Select All", "selectAll:", "a", nsModCommand))
	return m
}

func windowMenu() objc.ID {
	m := newMenu("Window")
	addOwned(m, newMenuItem("Minimize", "performMiniaturize:", "m", nsModCommand))
	addOwned(m, newMenuItem("Zoom", "performZoom:", "", 0))
	addOwned(m, newMenuItem("Enter Full Screen", "toggleFullScreen:", "f", nsModCommand|nsModControl))
	addSeparator(m)
	addOwned(m, newMenuItem("Close", "performClose:", "w", nsModCommand))
	addSeparator(m)
	addOwned(m, newMenuItem("Bring All to Front", "arrangeInFront:", "", 0))
	return m
}

func installMenuBar(app objc.ID, items []window.MenuItem) {
	name := processName()
	root := newMenu("")
	addSubmenu(root, name, appMenu(name))
	b := &menuBuild{}
	if len(items) == 0 || !hasEditCommand(items) {
		addSubmenu(root, "Edit", editMenu())
	}
	b.fill(root, items)
	windows := windowMenu()
	addSubmenu(root, "Window", windows)
	app.Send(sel("setMainMenu:"), root)
	app.Send(sel("setWindowsMenu:"), windows)
	root.Send(sel("release"))

	menuMu.Lock()
	old := barIDs
	barIDs = b.ids
	menuMu.Unlock()
	forgetActions(old)
}

func (w *win) SetMenuBar(items []window.MenuItem) {
	dispatchMain(func() {
		w.mu.Lock()
		w.menu, w.ownMenu = items, true
		w.mu.Unlock()
		app := objc.ID(cls("NSApplication")).Send(sel("sharedApplication"))
		if key := app.Send(sel("keyWindow")); key == 0 || key == w.nw {
			installMenuBar(app, items)
		}
	})
}

func (w *win) menuBecameKey() {
	w.mu.Lock()
	items, own := w.menu, w.ownMenu
	w.mu.Unlock()
	if own {
		installMenuBar(objc.ID(cls("NSApplication")).Send(sel("sharedApplication")), items)
	}
}

func (w *win) EditCommand(cmd window.EditCommand) {
	action, ok := editSelectors[cmd]
	if !ok {
		return
	}
	dispatchMain(func() {
		if w.view.open() {
			w.view.wk.Send(sel("tryToPerform:with:"), sel(action), objc.ID(0))
		}
	})
}

func (w *win) PopupMenu(items []window.MenuItem, x, y int) {
	dispatchMain(func() {
		if !w.view.open() {
			return
		}
		at, ok := w.popupPoint(x, y)
		if !ok {
			return
		}
		b := &menuBuild{}
		m := newMenu("")
		b.fill(m, items)
		objc.Send[uint64](m, sel("popUpMenuPositioningItem:atLocation:inView:"), objc.ID(0), at, objc.ID(0))
		m.Send(sel("release"))
		time.AfterFunc(popupActionGrace, func() { dispatchMain(func() { forgetActions(b.ids) }) })
	})
}

func (w *win) popupPoint(x, y int) (nsPoint, bool) {
	if x < 0 || y < 0 {
		return pointOf(objc.ID(cls("NSEvent")), "mouseLocation")
	}
	frame, ok := rectOf(w.nw, "frame")
	if !ok {
		return nsPoint{}, false
	}
	content, ok := rectOf(w.view.wk, "frame")
	if !ok {
		return nsPoint{}, false
	}
	px, py := popupScreenPoint(frame.Origin.X, frame.Origin.Y, content.Origin.X, content.Origin.Y, content.Size.H, x, y)
	return nsPoint{X: px, Y: py}, true
}

func pointOf(obj objc.ID, key string) (nsPoint, bool) {
	v := obj.Send(sel("valueForKey:"), nsString(key))
	if v == 0 {
		return nsPoint{}, false
	}
	var p nsPoint
	msgSendGetValue(v, sel("getValue:size:"), unsafe.Pointer(&p), uint64(unsafe.Sizeof(p)))
	return p, true
}
