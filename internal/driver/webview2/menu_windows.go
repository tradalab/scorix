//go:build windows

package webview2

import (
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/tradalab/scorix/window"
)

var (
	procCreateMenu              = user32.NewProc("CreateMenu")
	procCreatePopupMenu         = user32.NewProc("CreatePopupMenu")
	procAppendMenuW             = user32.NewProc("AppendMenuW")
	procSetMenu                 = user32.NewProc("SetMenu")
	procDrawMenuBar             = user32.NewProc("DrawMenuBar")
	procDestroyMenu             = user32.NewProc("DestroyMenu")
	procTrackPopupMenu          = user32.NewProc("TrackPopupMenu")
	procGetCursorPos            = user32.NewProc("GetCursorPos")
	procClientToScreen          = user32.NewProc("ClientToScreen")
	procCreateAcceleratorTableW = user32.NewProc("CreateAcceleratorTableW")
	procDestroyAcceleratorTable = user32.NewProc("DestroyAcceleratorTable")
	procTranslateAcceleratorW   = user32.NewProc("TranslateAcceleratorW")
	procGetAncestor             = user32.NewProc("GetAncestor")
	procSendInput               = user32.NewProc("SendInput")
)

const (
	wmCommand    uint32 = 0x0111
	wmKeyDown    uint32 = 0x0100
	wmSysKeyDown uint32 = 0x0104

	mfString    uintptr = 0x0000
	mfGrayed    uintptr = 0x0001
	mfChecked   uintptr = 0x0008
	mfPopup     uintptr = 0x0010
	mfSeparator uintptr = 0x0800

	tpmRightButton uintptr = 0x0002
	tpmReturnCmd   uintptr = 0x0100

	fVirtKey uint8 = 0x01
	fShift   uint8 = 0x04
	fControl uint8 = 0x08
	fAlt     uint8 = 0x10

	gaRoot uintptr = 2

	inputKeyboard uint32 = 1
	keyEventKeyUp uint32 = 0x0002
	vkControl     uint16 = 0x11

	ctrlMoveFocus         = 12 // ICoreWebView2Controller::MoveFocus; put_Bounds=6 anchors the slot count
	moveFocusProgrammatic = 0

	menuCmdFirst uint16 = 1000 // never 0: TrackPopupMenu reports "nothing chosen" as 0, and the bar keeps a distinct range
)

type accelEntry struct {
	fVirt uint8
	key   uint16
	cmd   uint16
}

type keyInput struct {
	typ   uint32
	_     uint32
	vk    uint16
	scan  uint16
	flags uint32
	time  uint32
	extra uintptr
	_     [8]byte // INPUT is sized by its MOUSEINPUT member: 40 bytes on x64
}

type menuState struct {
	bar   windows.Handle
	accel windows.Handle
	cmds  map[uint16]func()
}

type menuBuilder struct {
	next   uint16
	cmds   map[uint16]func()
	accels []accelEntry
}

func (b *menuBuilder) fill(h uintptr, items []window.MenuItem) {
	for _, it := range items {
		switch {
		case it.Separator:
			procAppendMenuW.Call(h, mfSeparator, 0, 0)
		case len(it.Submenu) > 0:
			sub, _, _ := procCreatePopupMenu.Call()
			b.fill(sub, it.Submenu)
			procAppendMenuW.Call(h, mfPopup|itemFlags(it), sub, uintptr(unsafe.Pointer(mustUTF16(it.Label))))
		default:
			id := b.next
			b.next++
			b.cmds[id] = it.OnClick
			text := it.Label
			if !it.Accel.IsZero() {
				text += "\t" + it.Accel.String()
				if !it.AccelHint {
					mods, vk := it.Accel.Win32()
					b.accels = append(b.accels, accelEntry{fVirt: fVirtKey | accelFlags(mods), key: uint16(vk), cmd: id})
				}
			}
			procAppendMenuW.Call(h, mfString|itemFlags(it), uintptr(id), uintptr(unsafe.Pointer(mustUTF16(text))))
		}
	}
}

func itemFlags(it window.MenuItem) uintptr {
	var f uintptr
	if it.Disabled {
		f |= mfGrayed
	}
	if it.Checked {
		f |= mfChecked
	}
	return f
}

func accelFlags(mods uint32) uint8 {
	var f uint8
	if mods&0x1 != 0 {
		f |= fAlt
	}
	if mods&0x2 != 0 {
		f |= fControl
	}
	if mods&0x4 != 0 {
		f |= fShift
	}
	return f // MOD_WIN has no ACCEL flag: the shell owns Win+key
}

func (w *win) SetMenuBar(items []window.MenuItem) {
	w.rt.Dispatch(func() { w.applyMenuBar(items) })
}

func (w *win) applyMenuBar(items []window.MenuItem) {
	w.mu.Lock()
	old := w.menu
	w.menu = menuState{}
	w.mu.Unlock()
	if old.bar != 0 {
		procSetMenu.Call(uintptr(w.hwnd), 0)
		procDestroyMenu.Call(uintptr(old.bar))
	}
	if old.accel != 0 {
		procDestroyAcceleratorTable.Call(uintptr(old.accel))
	}
	if len(items) > 0 {
		b := &menuBuilder{next: menuCmdFirst, cmds: map[uint16]func(){}}
		bar, _, _ := procCreateMenu.Call()
		b.fill(bar, items)
		procSetMenu.Call(uintptr(w.hwnd), bar)
		var accel uintptr
		if len(b.accels) > 0 {
			accel, _, _ = procCreateAcceleratorTableW.Call(uintptr(unsafe.Pointer(&b.accels[0])), uintptr(len(b.accels)))
		}
		w.mu.Lock()
		w.menu = menuState{bar: windows.Handle(bar), accel: windows.Handle(accel), cmds: b.cmds}
		w.mu.Unlock()
	}
	procDrawMenuBar.Call(uintptr(w.hwnd))
	w.updateBounds() // SetMenu moves the client rect without a WM_SIZE
}

func (w *win) releaseMenu() {
	w.mu.Lock()
	accel := w.menu.accel
	w.menu = menuState{} // the bar itself dies with the HWND
	w.mu.Unlock()
	if accel != 0 {
		procDestroyAcceleratorTable.Call(uintptr(accel))
	}
}

func (w *win) fireMenuCommand(wParam uintptr) bool {
	if wParam>>16 > 1 { // 0 = menu, 1 = accelerator; higher values are control notifications
		return false
	}
	w.mu.Lock()
	fn := w.menu.cmds[uint16(wParam)]
	w.mu.Unlock()
	if fn == nil {
		return false
	}
	fn()
	return true
}

func (r *runtime) translateAccelerator(m *tagMSG) bool {
	if m.message != wmKeyDown && m.message != wmSysKeyDown {
		return false
	}
	root, _, _ := procGetAncestor.Call(uintptr(m.hwnd), gaRoot) // keystrokes land on WebView2's child HWNDs
	w := r.manager.byHandle(windows.Handle(root))
	if w == nil {
		return false
	}
	w.mu.Lock()
	accel := w.menu.accel
	w.mu.Unlock()
	if accel == 0 {
		return false
	}
	ret, _, _ := procTranslateAcceleratorW.Call(uintptr(w.hwnd), uintptr(accel), uintptr(unsafe.Pointer(m)))
	return ret != 0
}

func (w *win) PopupMenu(items []window.MenuItem, x, y int) {
	w.rt.Dispatch(func() {
		var pt tagPOINT
		if x < 0 || y < 0 {
			procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
		} else {
			dpi := w.windowDPI()
			pt = tagPOINT{x: int32(toPhysical(x, dpi)), y: int32(toPhysical(y, dpi))}
			procClientToScreen.Call(uintptr(w.hwnd), uintptr(unsafe.Pointer(&pt)))
		}
		b := &menuBuilder{next: 1, cmds: map[uint16]func(){}}
		h, _, _ := procCreatePopupMenu.Call()
		b.fill(h, items)
		procSetForegroundWin.Call(uintptr(w.hwnd)) // else the menu outlives a click outside it
		cmd, _, _ := procTrackPopupMenu.Call(h, tpmReturnCmd|tpmRightButton, uintptr(pt.x), uintptr(pt.y), 0, uintptr(w.hwnd), 0)
		procDestroyMenu.Call(h)
		if fn := b.cmds[uint16(cmd)]; fn != nil {
			fn()
		}
	})
}

var editKeys = map[window.EditCommand]uint16{
	window.EditUndo:      'Z',
	window.EditRedo:      'Y',
	window.EditCut:       'X',
	window.EditCopy:      'C',
	window.EditPaste:     'V',
	window.EditSelectAll: 'A',
}

// EditCommand replays the keystroke Chromium already binds: WebView2 has no
// edit API, and execCommand from ExecuteScript carries no user activation for
// the clipboard checks.
func (w *win) EditCommand(cmd window.EditCommand) {
	vk, ok := editKeys[cmd]
	if !ok {
		return
	}
	w.rt.Dispatch(func() {
		w.mu.Lock()
		controller := w.controller
		w.mu.Unlock()
		if controller != nil {
			comCall(controller, ctrlMoveFocus, moveFocusProgrammatic) // the menu click left focus on the frame
		}
		sendChord(vk)
	})
}

func sendChord(vk uint16) {
	in := [4]keyInput{
		{typ: inputKeyboard, vk: vkControl},
		{typ: inputKeyboard, vk: vk},
		{typ: inputKeyboard, vk: vk, flags: keyEventKeyUp},
		{typ: inputKeyboard, vk: vkControl, flags: keyEventKeyUp},
	}
	procSendInput.Call(uintptr(len(in)), uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0]))
}
