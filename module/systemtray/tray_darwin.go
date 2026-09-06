//go:build !server && darwin

package systemtray

import (
	"sync"

	"github.com/ebitengine/purego/objc"
	"github.com/tradalab/scorix/internal/mac"
	"github.com/tradalab/scorix/logger"
)

// AppKit constants, spelled out because purego has no headers to read them from.
const (
	variableStatusItemLength       = -1.0 // NSVariableStatusItemLength: as wide as its content
	controlStateOn           int64 = 1    // NSControlStateValueOn
)

var (
	itemMu     sync.Mutex
	statusItem objc.ID
	actionSeq  int64
	actions    = map[int64]func(){}
)

// A status item owning a menu opens it on either button, so there is no
// left-click hook to map Show onto the way Windows and Linux do; the Show role
// in the menu is the way in.
func trayStart(m *SystemTrayModule) error {
	if err := mac.Init(); err != nil {
		return err
	}
	nodes := m.nodes()
	icon, title, tooltip := m.icon, m.cfg.Title, m.cfg.Tooltip

	mac.DispatchMain(func() {
		defer mac.Recover("trayStart")
		bar := mac.Class("NSStatusBar").Send(mac.Sel("systemStatusBar"))
		it := mac.SendFloat(bar, mac.Sel("statusItemWithLength:"), variableStatusItemLength)
		if it == 0 {
			logger.Error("[systemtray] no status item: is NSApplication running?")
			return
		}
		// The status bar hands back an object it does not keep for us.
		it.Send(mac.Sel("retain"))

		itemMu.Lock()
		statusItem = it
		itemMu.Unlock()

		applyIcon(it, icon)
		applyTitle(it, title)
		applyTooltip(it, tooltip)
		it.Send(mac.Sel("setMenu:"), buildMenu(nodes))
	})
	return nil
}

func trayStop() {
	mac.DispatchMain(func() {
		defer mac.Recover("trayStop")
		itemMu.Lock()
		it := statusItem
		statusItem = 0
		itemMu.Unlock()
		if it == 0 {
			return
		}
		mac.Class("NSStatusBar").Send(mac.Sel("systemStatusBar")).
			Send(mac.Sel("removeStatusItem:"), it)
		it.Send(mac.Sel("release"))
	})
}

func traySetIcon(b []byte)    { onItem(func(it objc.ID) { applyIcon(it, b) }) }
func traySetTitle(s string)   { onItem(func(it objc.ID) { applyTitle(it, s) }) }
func traySetTooltip(s string) { onItem(func(it objc.ID) { applyTooltip(it, s) }) }

// onItem does nothing when the tray was never created: a setter arriving before
// OnStart or after OnStop is not an error.
func onItem(fn func(objc.ID)) {
	mac.DispatchMain(func() {
		defer mac.Recover("traySet")
		itemMu.Lock()
		it := statusItem
		itemMu.Unlock()
		if it != 0 {
			fn(it)
		}
	})
}

func button(it objc.ID) objc.ID { return it.Send(mac.Sel("button")) }

// Used as given, not as a template mask. Template is the macOS convention and
// adapts to a dark menu bar, but forcing it flattens the colored PNG apps
// already ship into a silhouette; ship a monochrome icon to get the native look.
func applyIcon(it objc.ID, b []byte) {
	if len(b) == 0 {
		return
	}
	data := mac.NSData(b)
	if data == 0 {
		return
	}
	img := mac.Class("NSImage").Send(mac.Sel("alloc")).Send(mac.Sel("initWithData:"), data)
	if img == 0 {
		logger.Warn("[systemtray] icon data is not an image macOS can read")
		return
	}
	// Without this a full-size app icon is drawn at its own size and pushes the
	// menu bar around; 18pt is the content height of a 22pt bar.
	mac.SendSize(img, mac.Sel("setSize:"), mac.Size{W: 18, H: 18})
	button(it).Send(mac.Sel("setImage:"), img)
	img.Send(mac.Sel("release"))
}

func applyTitle(it objc.ID, s string) {
	button(it).Send(mac.Sel("setTitle:"), mac.NSString(s))
}

func applyTooltip(it objc.ID, s string) {
	button(it).Send(mac.Sel("setToolTip:"), mac.NSString(s))
}

func buildMenu(nodes []node) objc.ID {
	m := mac.Class("NSMenu").Send(mac.Sel("alloc")).Send(mac.Sel("init"))
	// AppKit greys out any item whose target does not answer validateMenuItem:,
	// so disabling its automatic pass is what makes setEnabled: mean anything.
	m.Send(mac.Sel("setAutoenablesItems:"), false)
	for _, n := range nodes {
		m.Send(mac.Sel("addItem:"), buildItem(n))
	}
	// Built once and kept for the process: no balancing release, because an
	// over-release on a path that has never run is the worse bet.
	return m
}

func buildItem(n node) objc.ID {
	if n.separator {
		return mac.Class("NSMenuItem").Send(mac.Sel("separatorItem"))
	}
	item := mac.Class("NSMenuItem").Send(mac.Sel("alloc")).
		Send(mac.Sel("initWithTitle:action:keyEquivalent:"),
			mac.NSString(n.label), mac.Sel("scorixTrayAction:"), mac.NSString(""))
	if n.tooltip != "" {
		item.Send(mac.Sel("setToolTip:"), mac.NSString(n.tooltip))
	}
	if n.checked {
		item.Send(mac.Sel("setState:"), controlStateOn)
	}
	if len(n.children) > 0 {
		item.Send(mac.Sel("setSubmenu:"), buildMenu(n.children))
		item.Send(mac.Sel("setEnabled:"), !n.disabled)
		return item
	}
	// Enabled only when something happens: an item with no action that still
	// highlights reads as broken rather than as a label.
	item.Send(mac.Sel("setEnabled:"), !n.disabled && n.onClick != nil)
	if target := trayTarget(); n.onClick != nil && target != 0 {
		item.Send(mac.Sel("setTarget:"), target)
		item.Send(mac.Sel("setTag:"), registerAction(n.onClick))
	}
	return item
}

func registerAction(fn func()) int64 {
	itemMu.Lock()
	defer itemMu.Unlock()
	actionSeq++
	actions[actionSeq] = fn
	return actionSeq
}

var (
	targetOnce sync.Once
	targetObj  objc.ID
)

// One instance for every menu item, created once and never released: AppKit's
// setTarget: does not retain, so a per-item object would be a dangling target
// the moment anything released it.
func trayTarget() objc.ID {
	targetOnce.Do(func() {
		_, err := objc.RegisterClass(
			"ScorixTrayTarget",
			objc.GetClass("NSObject"),
			nil,
			nil,
			[]objc.MethodDef{{
				Cmd: mac.Sel("scorixTrayAction:"),
				Fn: func(_ objc.ID, _ objc.SEL, sender objc.ID) {
					defer mac.Recover("scorixTrayAction")
					tag := objc.Send[int64](sender, mac.Sel("tag"))
					itemMu.Lock()
					fn := actions[tag]
					itemMu.Unlock()
					if fn == nil {
						return
					}
					// Off the main thread on purpose: this runs inside the menu's
					// tracking loop, where a handler that blocks freezes the whole
					// menu bar, not just this app.
					go func() {
						defer mac.Recover("trayClick")
						fn()
					}()
				},
			}},
		)
		if err != nil {
			logger.Error("[systemtray] cannot register menu target", "err", err)
			return
		}
		targetObj = mac.Class("ScorixTrayTarget").Send(mac.Sel("new"))
	})
	return targetObj
}
