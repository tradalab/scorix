//go:build linux && !android

package bluetooth

import (
	"bufio"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// BlueZ's side is a socket handed over by fd; a socketpair stands in for the RFCOMM one.
func TestListenerTurnsHandedSocketsIntoConns(t *testing.T) {
	l := newListener("/test/rfcomm")
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	phone := os.NewFile(uintptr(fds[1]), "phone")
	defer phone.Close()
	go func() {
		if derr := l.NewConnection("/org/bluez/hci0/dev_AA", dbus.UnixFD(fds[0]), nil); derr != nil {
			t.Error(derr)
		}
	}()
	c, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.RemoteAddr().String() != "/org/bluez/hci0/dev_AA" {
		t.Fatalf("remote %s", c.RemoteAddr())
	}
	if _, err := phone.WriteString("hello\n"); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || line != "hello\n" {
		t.Fatalf("read %q %v", line, err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Accept(); err == nil {
		t.Fatal("accept after close")
	}
}

func TestListenRFCOMMRegistersWithBlueZ(t *testing.T) {
	if os.Getenv("SCORIX_BLUEZ") == "" {
		t.Skip("SCORIX_BLUEZ not set")
	}
	l, err := ListenRFCOMM("6b1d0009-5a3c-4b8e-9d2f-7a0c3e1f0001", "scorix test")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}
