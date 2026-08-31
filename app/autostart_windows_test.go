//go:build windows

package app

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestAutostartRoundTrip(t *testing.T) {
	a := &App{}
	a.opts.Identifier = fmt.Sprintf("com.scorix.autostart-test-%d", os.Getpid())
	t.Cleanup(func() {
		if key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE); err == nil {
			_ = key.DeleteValue(a.autostartName())
			_ = key.Close()
		}
	})

	if on, err := a.AutostartEnabled(); err != nil || on {
		t.Fatalf("initial state = %v %v", on, err)
	}
	if err := a.SetAutostart(true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if on, err := a.AutostartEnabled(); err != nil || !on {
		t.Fatalf("after enable = %v %v", on, err)
	}
	if err := a.SetAutostart(false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if on, err := a.AutostartEnabled(); err != nil || on {
		t.Fatalf("after disable = %v %v", on, err)
	}
	if err := a.SetAutostart(false); err != nil {
		t.Fatalf("disable twice: %v", err)
	}
}
