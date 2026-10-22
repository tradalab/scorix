//go:build linux && !android

package bluetooth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	ifaceProfileMgr = "org.bluez.ProfileManager1"
	ifaceProfile    = "org.bluez.Profile1"
)

// Listener accepts RFCOMM connections from devices bonded with this machine in its Bluetooth
// settings. It needs no LE advertising, so it also works where the controller or kernel refuses it.
type Listener struct {
	conn  *dbus.Conn
	path  dbus.ObjectPath
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

// ListenRFCOMM registers a serial-port style profile under uuid; BlueZ publishes it in SDP so a
// phone finds the channel by uuid alone. Only bonded devices may connect.
func ListenRFCOMM(uuid, name string) (*Listener, error) {
	// A private connection: Close closes it, and the shared one belongs to the rest of the process.
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	id := make([]byte, 4)
	_, _ = rand.Read(id)
	l := newListener(dbus.ObjectPath("/dev/scorix/bluetooth/rfcomm" + hex.EncodeToString(id)))
	l.conn = conn
	if err := conn.Export(l, l.path, ifaceProfile); err != nil {
		conn.Close()
		return nil, err
	}
	opts := map[string]dbus.Variant{
		"Name":                  dbus.MakeVariant(name),
		"Role":                  dbus.MakeVariant("server"),
		"RequireAuthentication": dbus.MakeVariant(true),
		"RequireAuthorization":  dbus.MakeVariant(false),
		"AutoConnect":           dbus.MakeVariant(false),
	}
	err = conn.Object(bluez, "/org/bluez").Call(ifaceProfileMgr+".RegisterProfile", 0, l.path, strings.ToLower(uuid), opts).Err
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: register RFCOMM profile: %v", ErrUnavailable, err)
	}
	return l, nil
}

func newListener(path dbus.ObjectPath) *Listener {
	return &Listener{path: path, conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *Listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

// Close withdraws the profile; BlueZ drops it anyway when the bus connection goes.
func (l *Listener) Close() error {
	var err error
	l.once.Do(func() {
		close(l.done)
		if l.conn != nil {
			err = l.conn.Object(bluez, "/org/bluez").Call(ifaceProfileMgr+".UnregisterProfile", 0, l.path).Err
			err = errors.Join(err, l.conn.Close())
		}
	})
	return err
}

func (l *Listener) Addr() net.Addr { return rfcommAddr(l.path) }

// NewConnection is BlueZ handing over one connected socket. It is called on the bus goroutine,
// so it only queues the socket; nobody accepting means the connection is refused.
func (l *Listener) NewConnection(dev dbus.ObjectPath, fd dbus.UnixFD, _ map[string]dbus.Variant) *dbus.Error {
	// Non-blocking first, so the runtime poller takes the fd and deadlines work.
	if err := syscall.SetNonblock(int(fd), true); err != nil {
		syscall.Close(int(fd))
		return dbus.MakeFailedError(err)
	}
	c := &rfcommConn{File: os.NewFile(uintptr(fd), "rfcomm"), remote: rfcommAddr(dev), local: l.Addr()}
	select {
	case l.conns <- c:
		return nil
	case <-l.done:
	case <-time.After(5 * time.Second):
	}
	c.Close()
	return dbus.NewError(errFailed, []any{"not accepting"})
}

func (l *Listener) RequestDisconnection(dbus.ObjectPath) *dbus.Error { return nil }
func (l *Listener) Release() *dbus.Error                             { return nil }

// rfcommConn is the socket as a net.Conn; net.FileConn refuses AF_BLUETOOTH.
type rfcommConn struct {
	*os.File
	local, remote net.Addr
}

func (c *rfcommConn) LocalAddr() net.Addr  { return c.local }
func (c *rfcommConn) RemoteAddr() net.Addr { return c.remote }

// rfcommAddr is a BlueZ object path standing in for an address: the device's is in its path.
type rfcommAddr dbus.ObjectPath

func (a rfcommAddr) Network() string { return "rfcomm" }
func (a rfcommAddr) String() string  { return string(a) }
