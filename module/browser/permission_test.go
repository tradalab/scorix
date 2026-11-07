package browser

import (
	"testing"

	"github.com/tradalab/scorix/module/moduletest"
)

func TestHandlersAskForBrowserNotTheWholeShell(t *testing.T) {
	r := moduletest.Load(t, New(), nil)
	for _, h := range []string{"OpenUrl", "RevealPath", "OpenPath"} {
		if got := r.Needs(h); got != "browser" {
			t.Errorf("%s needs %q, want browser", h, got)
		}
	}
}
