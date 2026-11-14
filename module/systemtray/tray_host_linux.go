//go:build !server && linux && !android

package systemtray

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

func trayHostReady() error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("the status notifier needs a D-Bus session bus: %w", err)
	}
	return conn.Close()
}
