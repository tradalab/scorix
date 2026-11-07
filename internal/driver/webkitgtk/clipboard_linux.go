//go:build linux && !android

package webkitgtk

import "errors"

var errClipboardUnavailable = errors.New("webkitgtk: the GTK clipboard did not answer, is gtk_main running?")

type gtkClipboard struct{}

func clipboardHandle() uintptr {
	return gtkClipboardGet(gdkAtomIntern("CLIPBOARD", 0))
}

func (gtkClipboard) WriteText(text string) error {
	ok := onMainVal(func() bool {
		cb := clipboardHandle()
		if cb == 0 {
			return false
		}
		gtkClipboardSetText(cb, text, int32(len(text)))
		return true
	})
	if !ok {
		return errClipboardUnavailable
	}
	return nil
}

func (gtkClipboard) ReadText() (string, error) {
	type read struct {
		text string
		ok   bool
	}
	r := onMainVal(func() read {
		cb := clipboardHandle()
		if cb == 0 {
			return read{}
		}
		p := gtkClipboardWaitText(cb)
		if p == 0 {
			return read{ok: true}
		}
		defer gFree(p)
		return read{text: goString(p), ok: true}
	})
	if !r.ok {
		return "", errClipboardUnavailable
	}
	return r.text, nil
}
