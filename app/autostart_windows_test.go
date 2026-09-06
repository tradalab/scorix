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

// The GitHub Windows runner has no Run key at all, and every call used to fail
// with ERROR_FILE_NOT_FOUND instead of reporting "not enabled".
func TestAutostartWithoutExistingRunKey(t *testing.T) {
	parent := fmt.Sprintf(`Software\scorix-autostart-test-%d`, os.Getpid())
	saved := runKey
	runKey = parent + `\Run`
	t.Cleanup(func() {
		runKey = saved
		_ = registry.DeleteKey(registry.CURRENT_USER, parent+`\Run`)
		_ = registry.DeleteKey(registry.CURRENT_USER, parent)
	})

	a := &App{}
	a.opts.Identifier = "com.scorix.autostart-nokey"

	if on, err := a.AutostartEnabled(); err != nil || on {
		t.Fatalf("missing key should read as disabled, got %v %v", on, err)
	}
	if err := a.SetAutostart(false); err != nil {
		t.Fatalf("disable with no key: %v", err)
	}
	if err := a.SetAutostart(true); err != nil {
		t.Fatalf("enable must create the key: %v", err)
	}
	if on, err := a.AutostartEnabled(); err != nil || !on {
		t.Fatalf("after enable = %v %v", on, err)
	}
	if err := a.SetAutostart(false); err != nil {
		t.Fatalf("disable: %v", err)
	}
}
