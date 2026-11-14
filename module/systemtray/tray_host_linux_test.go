//go:build !server && linux && !android

package systemtray

import (
	"testing"
	"time"
)

func TestTrayStartWithoutSessionBusLeavesAppRunningWithoutTray(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+t.TempDir()+"/no-bus")
	ran := make(chan struct{}, 1)
	prev := trayRun
	trayRun = func(func(), func()) { ran <- struct{}{} }
	t.Cleanup(func() { trayRun = prev })

	if err := trayStart(&SystemTrayModule{}); err != nil {
		t.Fatalf("trayStart must not fail the app when there is no session bus: %v", err)
	}
	select {
	case <-ran:
		t.Fatal("systray.Run started with no session bus; it dereferences a nil D-Bus connection and crashes the process")
	case <-time.After(300 * time.Millisecond):
	}
}
