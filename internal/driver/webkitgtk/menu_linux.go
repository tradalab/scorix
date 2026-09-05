//go:build linux

package webkitgtk

import (
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/tradalab/scorix/window"
)

const (
	gtkOrientationVertical int32 = 1
	gtkAccelVisible        int32 = 1

	gdkShiftMask   uint32 = 1 << 0
	gdkControlMask uint32 = 1 << 2
	gdkMod1Mask    uint32 = 1 << 3
	gdkSuperMask   uint32 = 1 << 26

	gdkGravityNorthWest int32 = 1
	gdkGravitySouthEast int32 = 9
)

type gdkRectangle struct{ x, y, width, height int32 }

var (
	menuCmds    sync.Map // cmd id -> func()
	popupIDs    sync.Map // GtkMenu ptr -> []uintptr cmd ids, released on hide
	menuSeqMu   sync.Mutex
	menuSeq     uintptr
	menuCBOnce  sync.Once
	activateCB  uintptr
	popupHideCB uintptr
)

func initMenuSignals() {
	menuCBOnce.Do(func() {
		activateCB = purego.NewCallback(func(_ uintptr, data uintptr) uintptr {
			defer recoverCB("menu-activate")
			if v, ok := menuCmds.Load(data); ok {
				if fn := v.(func()); fn != nil {
					fn()
				}
			}
			return 0
		})
		popupHideCB = purego.NewCallback(func(menu uintptr, _ uintptr) uintptr {
			defer recoverCB("menu-popup-hide")
			v, _ := popupIDs.LoadAndDelete(menu)
			// Both halves have to wait for the idle turn. GTK hides the menu
			// BEFORE it activates the chosen item, so releasing the commands
			// here would make every click a silent no-op; and a widget must not
			// be destroyed from inside its own signal emission.
			dispatchMain(func() {
				if ids, ok := v.([]uintptr); ok {
					for _, id := range ids {
						menuCmds.Delete(id)
					}
				}
				gtkWidgetDestroy(menu)
			})
			return 0
		})
	})
}

func nextMenuID() uintptr {
	menuSeqMu.Lock()
	defer menuSeqMu.Unlock()
	menuSeq++
	return menuSeq
}

type gtkMenuBuild struct {
	group uintptr // accel group; zero for popups
	ids   []uintptr
}

func (b *gtkMenuBuild) fill(shell uintptr, items []window.MenuItem) {
	for _, it := range items {
		var item uintptr
		switch {
		case it.Separator:
			item = gtkSeparatorMenuItem()
		case it.Checked:
			item = gtkCheckMenuItemNew(it.Label)
			gtkCheckMenuItemSet(item, 1) // before "activate" is connected: set_active emits it
		default:
			item = gtkMenuItemNewLabel(it.Label)
		}
		if it.Disabled {
			gtkWidgetSetSensitive(item, 0)
		}
		switch {
		case len(it.Submenu) > 0:
			sub := gtkMenuNew()
			b.fill(sub, it.Submenu)
			gtkMenuItemSetSubmenu(item, sub)
		case !it.Separator:
			id := nextMenuID()
			menuCmds.Store(id, it.OnClick)
			b.ids = append(b.ids, id)
			signalConnect(item, "activate", activateCB, id)
			if !it.Accel.IsZero() {
				if key := gdkKeyvalFromName(gdkKeyName(it.Accel.Key)); key != 0 {
					b.setAccel(item, key, gdkMods(it.Accel), it.AccelHint)
				}
			}
		}
		gtkMenuShellAppend(shell, item)
	}
}

// setAccel binds the key when we own it, and otherwise only paints it on the
// label. A GtkAccelGroup on the window runs BEFORE the focused widget sees the
// key, so binding Ctrl+Z here would take undo away from the page - and a popup
// (group 0) has no window to bind against at all.
func (b *gtkMenuBuild) setAccel(item uintptr, key, mods uint32, hint bool) {
	if b.group != 0 && !hint {
		gtkWidgetAddAccel(item, "activate", b.group, key, mods, gtkAccelVisible)
		return
	}
	if label := gtkBinGetChild(item); label != 0 {
		gtkAccelLabelSetAccel(label, key, mods)
	}
}

func gdkKeyName(key string) string {
	switch key {
	case "SPACE":
		return "space"
	case "TAB":
		return "Tab"
	case "ENTER", "RETURN":
		return "Return"
	case "ESC", "ESCAPE":
		return "Escape"
	case "BACKSPACE":
		return "BackSpace"
	case "DELETE":
		return "Delete"
	case "HOME":
		return "Home"
	case "END":
		return "End"
	case "PAGEUP":
		return "Page_Up"
	case "PAGEDOWN":
		return "Page_Down"
	case "UP", "DOWN", "LEFT", "RIGHT":
		return key[:1] + strings.ToLower(key[1:])
	}
	if len(key) == 1 {
		return strings.ToLower(key)
	}
	return key // F1..F24
}

func gdkMods(a window.Accel) uint32 {
	var m uint32
	if a.Shift {
		m |= gdkShiftMask
	}
	if a.Ctrl {
		m |= gdkControlMask
	}
	if a.Alt {
		m |= gdkMod1Mask
	}
	if a.Super {
		m |= gdkSuperMask
	}
	return m
}

func (w *win) SetMenuBar(items []window.MenuItem) {
	dispatchMain(func() {
		initMenuSignals()
		w.dropMenuBar()
		if len(items) == 0 {
			return
		}
		group := gtkAccelGroupNew()
		gtkWindowAddAccel(w.gw, group)
		b := &gtkMenuBuild{group: group}
		bar := gtkMenuBarNew()
		b.fill(bar, items)
		gtkBoxPackStart(w.vbox, bar, 0, 0, 0)
		gtkBoxReorderChild(w.vbox, bar, 0)
		gtkWidgetShowAll(bar)
		w.mu.Lock()
		w.menubar, w.accelGroup, w.menuIDs = bar, group, b.ids
		w.mu.Unlock()
	})
}

// releaseMenuRefs is the destroy-path half of dropMenuBar: the widgets die
// with the window, only our accel-group ref and command closures remain.
func (w *win) releaseMenuRefs() {
	w.mu.Lock()
	group, ids := w.accelGroup, w.menuIDs
	w.menubar, w.accelGroup, w.menuIDs = 0, 0, nil
	w.mu.Unlock()
	for _, id := range ids {
		menuCmds.Delete(id)
	}
	if group != 0 {
		gObjectUnref(group)
	}
}

func (w *win) dropMenuBar() {
	w.mu.Lock()
	bar, group, ids := w.menubar, w.accelGroup, w.menuIDs
	w.menubar, w.accelGroup, w.menuIDs = 0, 0, nil
	w.mu.Unlock()
	for _, id := range ids {
		menuCmds.Delete(id)
	}
	if bar != 0 {
		gtkWidgetDestroy(bar)
	}
	if group != 0 {
		gtkWindowRemoveAccel(w.gw, group)
		gObjectUnref(group)
	}
}

// PopupMenu anchors to a rect: popup_at_pointer needs the triggering GdkEvent,
// and a request that arrived over IPC has none.
func (w *win) PopupMenu(items []window.MenuItem, x, y int) {
	dispatchMain(func() {
		initMenuSignals()
		gdkWin := gtkWidgetGetWindow(w.gw)
		if gdkWin == 0 {
			return
		}
		var px, py int32
		if x < 0 || y < 0 {
			pointer := gdkSeatGetPointer(gdkDisplayDefaultSeat(gdkDisplayGetDefault()))
			gdkWindowDevicePos(gdkWin, pointer, &px, &py, 0)
		} else {
			gtkWidgetTranslate(w.view.wk, w.gw, int32(x), int32(y), &px, &py) // webview coords -> toplevel coords
		}
		menu := gtkMenuNew()
		b := &gtkMenuBuild{}
		b.fill(menu, items)
		popupIDs.Store(menu, b.ids)
		signalConnect(menu, "hide", popupHideCB, 0)
		gtkWidgetShowAll(menu)
		rect := gdkRectangle{x: px, y: py, width: 1, height: 1}
		gtkMenuPopupAtRect(menu, gdkWin, unsafe.Pointer(&rect), gdkGravitySouthEast, gdkGravityNorthWest, 0)
	})
}

var wkEditCommands = map[window.EditCommand]string{
	window.EditUndo:      "Undo",
	window.EditRedo:      "Redo",
	window.EditCut:       "Cut",
	window.EditCopy:      "Copy",
	window.EditPaste:     "Paste",
	window.EditSelectAll: "SelectAll",
}

func (w *win) EditCommand(cmd window.EditCommand) {
	name, ok := wkEditCommands[cmd]
	if !ok {
		return
	}
	dispatchMain(func() { wkViewExecEditCmd(w.view.wk, name) })
}
