package dialog

import (
	"testing"

	"github.com/tradalab/scorix/module/moduletest"
)

func TestHandlersAskForDialogNotTheWholeShell(t *testing.T) {
	r := moduletest.Load(t, New(), nil)
	for _, h := range []string{"OpenFile", "OpenDirectory", "SaveFile"} {
		if got := r.Needs(h); got != "dialog" {
			t.Errorf("%s needs %q, want dialog", h, got)
		}
	}
}
