package headless

import (
	"testing"

	"github.com/tradalab/scorix/window"
)

// TestCloseVeto proves EventData.PreventDefault vetoes a close (window stays open) and that, without a veto, Close completes.
func TestCloseVeto(t *testing.T) {
	rt, _ := driver{}.NewRuntime(window.RuntimeConfig{})
	mgr := rt.Windows()

	// 1. A handler that vetoes → window not closed.
	wv, _ := mgr.New(window.Options{})
	veto := true
	wv.On(window.EventClose, func(d window.EventData) {
		if veto {
			d.PreventDefault()
		}
	})
	wv.Close()
	if wv.(*win).Closed() {
		t.Fatal("Close was vetoed via PreventDefault but window reports closed")
	}

	// 2. Same window, no veto this time → closes.
	veto = false
	wv.Close()
	if !wv.(*win).Closed() {
		t.Fatal("Close without veto should complete")
	}

	// 3. A window with no handler closes normally.
	wn, _ := mgr.New(window.Options{})
	wn.Close()
	if !wn.(*win).Closed() {
		t.Fatal("Close with no handler should complete")
	}
}
