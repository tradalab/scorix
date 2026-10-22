//go:build linux && !android

package bluetooth

import (
	"bytes"
	"os"
	"testing"

	"github.com/godbus/dbus/v5"
)

const testService = "6b1d0001-5a3c-4b8e-9d2f-7a0c3e1f0001"

// Needs a real adapter and bluetoothd, so it runs only with SCORIX_BLUEZ=1.
func TestServeRegistersWithBlueZ(t *testing.T) {
	if os.Getenv("SCORIX_BLUEZ") == "" {
		t.Skip("SCORIX_BLUEZ not set")
	}
	long := bytes.Repeat([]byte("0123456789"), 40)
	var got []byte
	p, err := Serve(Service{UUID: testService, Chars: []Characteristic{
		{UUID: "6b1d0002-5a3c-4b8e-9d2f-7a0c3e1f0001", Read: func() ([]byte, error) { return long, nil }, Notify: true},
		{UUID: "6b1d0003-5a3c-4b8e-9d2f-7a0c3e1f0001", Write: func(b []byte) error { got = b; return nil }},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// The system bus lets only bluetoothd call these objects, so they are called directly here.
	tail, derr := p.chars["6b1d0002-5a3c-4b8e-9d2f-7a0c3e1f0001"].ReadValue(
		map[string]dbus.Variant{"offset": dbus.MakeVariant(uint16(390))})
	if derr != nil || !bytes.Equal(tail, long[390:]) {
		t.Fatalf("read at offset: %q %v", tail, derr)
	}
	if _, derr := p.chars["6b1d0002-5a3c-4b8e-9d2f-7a0c3e1f0001"].ReadValue(
		map[string]dbus.Variant{"offset": dbus.MakeVariant(uint16(401))}); derr == nil || derr.Name != errInvalidOffs {
		t.Fatalf("read past the end: %v", derr)
	}
	if derr := p.chars["6b1d0003-5a3c-4b8e-9d2f-7a0c3e1f0001"].WriteValue([]byte("hi"), nil); derr != nil || string(got) != "hi" {
		t.Fatalf("write delivered %q %v", got, derr)
	}
	if err := p.Notify("6B1D0002-5A3C-4B8E-9D2F-7A0C3E1F0001", []byte("n")); err != nil {
		t.Fatal(err)
	}
	if err := p.Advertise(true); err != nil {
		t.Logf("advertising refused on this adapter: %v", err)
	} else if err := p.Advertise(false); err != nil {
		t.Fatal(err)
	}
}
