package bluetooth

import (
	"context"

	"github.com/tradalab/scorix/native"
)

// Device is a computer the user chose in the system's pairing dialog. Address is what Connect
// takes; it is stable as long as the other side advertises from a public address.
type Device struct {
	Address string `json:"address"`
	Name    string `json:"name"`
}

// Associate shows Android's companion-device chooser for devices advertising service. Fault
// codes: canceled, failed, unavailable.
func Associate(ctx context.Context, service string) (Device, error) {
	var d Device
	if err := native.Call(ctx, "bluetooth", "associate", map[string]string{"service": service}, &d); err != nil {
		return Device{}, mapErr(err)
	}
	return d, nil
}

// Connect opens a GATT connection, raises the MTU and discovers services. Fault codes: denied
// (nearby devices permission), off, failed, unavailable.
func Connect(ctx context.Context, address string) error {
	return mapErr(native.Call(ctx, "bluetooth", "connect", map[string]string{"address": address}, nil))
}

// Read reads one characteristic whole; Android follows a value longer than the MTU itself.
func Read(ctx context.Context, address, service, char string) ([]byte, error) {
	var out struct {
		Value []byte `json:"value"`
	}
	err := native.Call(ctx, "bluetooth", "read",
		map[string]string{"address": address, "service": service, "characteristic": char}, &out)
	if err != nil {
		return nil, mapErr(err)
	}
	return out.Value, nil
}

// Write writes value with a response. Keep it within the MTU Connect got: see WriteValue in
// peripheral_linux.go.
func Write(ctx context.Context, address, service, char string, value []byte) error {
	return mapErr(native.Call(ctx, "bluetooth", "write", map[string]any{
		"address": address, "service": service, "characteristic": char, "value": value}, nil))
}

// Observe asks Android to wake the app's CompanionDeviceService when address comes in range, or
// to stop. Android 12 and later; the device must have come from Associate.
func Observe(ctx context.Context, address string, on bool) error {
	return mapErr(native.Call(ctx, "bluetooth", "observe", map[string]any{"address": address, "on": on}, nil))
}

func Disconnect(ctx context.Context, address string) error {
	return mapErr(native.Call(ctx, "bluetooth", "disconnect", map[string]string{"address": address}, nil))
}

// Bonded lists the devices paired in the phone's Bluetooth settings, the only ones Exchange reaches.
func Bonded(ctx context.Context) ([]Device, error) {
	var out struct {
		Devices []Device `json:"devices"`
	}
	if err := native.Call(ctx, "bluetooth", "bonded", nil, &out); err != nil {
		return nil, mapErr(err)
	}
	return out.Devices, nil
}

// Exchange sends one line to the RFCOMM service uuid on a bonded device and returns the line it
// answers with, without the newline. The socket stays open for the next call until Hangup.
func Exchange(ctx context.Context, address, uuid string, line []byte) ([]byte, error) {
	var out struct {
		Line []byte `json:"line"`
	}
	err := native.Call(ctx, "bluetooth", "exchange", map[string]any{"address": address, "uuid": uuid, "line": line}, &out)
	if err != nil {
		return nil, mapErr(err)
	}
	return out.Line, nil
}

func Hangup(ctx context.Context, address string) error {
	return mapErr(native.Call(ctx, "bluetooth", "hangup", map[string]string{"address": address}, nil))
}
