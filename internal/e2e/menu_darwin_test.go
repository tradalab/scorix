//go:build darwin && !ios

package e2e

import (
	"sync"
	"testing"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/tradalab/scorix/internal/mac"
	"github.com/tradalab/scorix/menu"
)

const (
	nsEventTypeKeyDown uint64 = 10
	modShift           uint64 = 1 << 17
	modCommand         uint64 = 1 << 20

	keyCodeA uint16 = 0
	keyCodeC uint16 = 8
	keyCodeK uint16 = 40
)

type point struct{ X, Y float64 }

var (
	keyEventOnce sync.Once
	keyEventErr  error
	keyEvent     func(objc.ID, objc.SEL, uint64, point, uint64, float64, int64, objc.ID, objc.ID, objc.ID, bool, uint16) objc.ID
)

func onMain[T any](t *testing.T, fn func() T) T {
	t.Helper()
	ch := make(chan T, 1)
	mac.DispatchMain(func() { ch <- fn() })
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("the main queue did not run the work in 5s")
	}
	var zero T
	return zero
}

func nsApp() objc.ID { return mac.Class("NSApplication").Send(mac.Sel("sharedApplication")) }

func appWindow() objc.ID {
	app := nsApp()
	if w := app.Send(mac.Sel("keyWindow")); w != 0 {
		return w
	}
	ws := app.Send(mac.Sel("windows"))
	if ws == 0 || objc.Send[uint64](ws, mac.Sel("count")) == 0 {
		return 0
	}
	return objc.Send[objc.ID](ws, mac.Sel("objectAtIndex:"), uint64(0))
}

func pressKey(t *testing.T, chars string, code uint16, mods uint64) {
	t.Helper()
	keyEventOnce.Do(func() {
		lib, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_GLOBAL|purego.RTLD_NOW)
		if err != nil {
			keyEventErr = err
			return
		}
		purego.RegisterLibFunc(&keyEvent, lib, "objc_msgSend")
	})
	if keyEventErr != nil {
		t.Fatal(keyEventErr)
	}
	sent := onMain(t, func() bool {
		w := appWindow()
		if w == 0 {
			return false
		}
		w.Send(mac.Sel("makeKeyAndOrderFront:"), objc.ID(0))
		w.Send(mac.Sel("makeFirstResponder:"), w.Send(mac.Sel("contentView")))
		num := objc.Send[int64](w, mac.Sel("windowNumber"))
		ev := keyEvent(mac.Class("NSEvent"),
			mac.Sel("keyEventWithType:location:modifierFlags:timestamp:windowNumber:context:characters:charactersIgnoringModifiers:isARepeat:keyCode:"),
			nsEventTypeKeyDown, point{}, mods, 0, num, 0, mac.NSString(chars), mac.NSString(chars), false, code)
		if ev == 0 {
			return false
		}
		nsApp().Send(mac.Sel("sendEvent:"), ev)
		return true
	})
	if !sent {
		t.Fatalf("no window to send %q to", chars)
	}
}

func pasteboardText(t *testing.T) string {
	t.Helper()
	return onMain(t, func() string {
		pb := mac.Class("NSPasteboard").Send(mac.Sel("generalPasteboard"))
		return mac.GoString(pb.Send(mac.Sel("stringForType:"), mac.NSString("public.utf8-plain-text")))
	})
}

func menuTitles(t *testing.T) []string {
	t.Helper()
	return onMain(t, func() []string {
		m := nsApp().Send(mac.Sel("mainMenu"))
		if m == 0 {
			return nil
		}
		n := objc.Send[int64](m, mac.Sel("numberOfItems"))
		out := make([]string, 0, n)
		for i := int64(0); i < n; i++ {
			it := objc.Send[objc.ID](m, mac.Sel("itemAtIndex:"), i)
			out = append(out, mac.GoString(it.Send(mac.Sel("title"))))
		}
		return out
	})
}

func hasKeyEquivalent(t *testing.T, key, action string) bool {
	t.Helper()
	return onMain(t, func() bool {
		item := nsApp().Send(mac.Sel("mainMenu")).Send(mac.Sel("itemAtIndex:"), int64(0)).Send(mac.Sel("submenu"))
		if item == 0 {
			return false
		}
		n := objc.Send[int64](item, mac.Sel("numberOfItems"))
		for i := int64(0); i < n; i++ {
			it := objc.Send[objc.ID](item, mac.Sel("itemAtIndex:"), i)
			if mac.GoString(it.Send(mac.Sel("keyEquivalent"))) == key && objc.Send[objc.SEL](it, mac.Sel("action")) == mac.Sel(action) {
				return true
			}
		}
		return false
	})
}

func TestTheAppMenuQuitsOnCmdQThroughTerminate(t *testing.T) {
	harness(t)
	if !hasKeyEquivalent(t, "q", "terminate:") {
		t.Fatalf("no Cmd+Q -> terminate: in the app menu; menu bar is %q", menuTitles(t))
	}
}

func TestCmdAAndCmdCReachThePageThroughTheMenuBar(t *testing.T) {
	harness(t)
	const text = "scorix menu bar"
	if !askBool(t, `(function(){var i=document.createElement("input");i.id="e2e-menu";i.value="`+text+`";document.body.appendChild(i);i.focus();i.setSelectionRange(0,0);return document.activeElement===i})()`) {
		t.Fatal("the input did not take focus")
	}
	t.Cleanup(func() { ask(t, `(document.getElementById("e2e-menu").remove(), true)`) })

	pressKey(t, "a", keyCodeA, modCommand)
	eventually(t, "Cmd+A to select the whole input", func() bool {
		return askBool(t, `(function(){var i=document.getElementById("e2e-menu");return i.selectionStart===0&&i.selectionEnd===i.value.length})()`)
	})

	onMain(t, func() bool {
		mac.Class("NSPasteboard").Send(mac.Sel("generalPasteboard")).Send(mac.Sel("clearContents"))
		return true
	})
	pressKey(t, "c", keyCodeC, modCommand)
	eventually(t, "Cmd+C to put the selection on the pasteboard", func() bool {
		return pasteboardText(t) == text
	})
}

func TestAMenuTheAppSetsKeepsEditAndFiresOnItsAccelerator(t *testing.T) {
	a := harness(t)
	w := a.MainWindow()
	clicked := make(chan struct{}, 1)
	w.SetMenu(menu.Menu{menu.Sub("Tools", menu.Item{Label: "Ping", Accelerator: "Ctrl+Shift+K", OnClick: func() {
		select {
		case clicked <- struct{}{}:
		default:
		}
	}})})
	t.Cleanup(func() { w.SetMenu(nil) })

	eventually(t, "the app's menu on the bar", func() bool {
		titles := menuTitles(t)
		return len(titles) == 4 && titles[1] == "Edit" && titles[2] == "Tools" && titles[3] == "Window"
	})

	pressKey(t, "K", keyCodeK, modCommand|modShift)
	select {
	case <-clicked:
	case <-time.After(5 * time.Second):
		t.Fatalf("Cmd+Shift+K never reached the Ping item; menu bar is %q", menuTitles(t))
	}
}
