package bluetooth

import (
	"context"

	"github.com/tradalab/scorix/internal/hostapi"
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
	d, err := hostapi.Bluetooth.Associate(ctx, &hostapi.BtService{Service: service})
	if err != nil {
		return Device{}, mapErr(err)
	}
	return Device{Address: d.Address, Name: d.Name}, nil
}

// Connect opens a GATT connection, raises the MTU and discovers services. Fault codes: denied
// (nearby devices permission), off, failed, unavailable.
func Connect(ctx context.Context, address string) error {
	_, err := hostapi.Bluetooth.Connect(ctx, &hostapi.BtAddress{Address: address})
	return mapErr(err)
}

// Read reads one characteristic whole; Android follows a value longer than the MTU itself.
func Read(ctx context.Context, address, service, char string) ([]byte, error) {
	out, err := hostapi.Bluetooth.Read(ctx, &hostapi.BtCharacteristic{Address: address, Service: service, Characteristic: char})
	if err != nil {
		return nil, mapErr(err)
	}
	return out.Value, nil
}

// Write writes value with a response. Keep it within the MTU Connect got: see WriteValue in
// peripheral_linux.go.
func Write(ctx context.Context, address, service, char string, value []byte) error {
	_, err := hostapi.Bluetooth.Write(ctx, &hostapi.BtWrite{Address: address, Service: service, Characteristic: char, Value: value})
	return mapErr(err)
}

// Observe asks Android to wake the app's CompanionDeviceService when address comes in range, or
// to stop. Android 12 and later; the device must have come from Associate.
func Observe(ctx context.Context, address string, on bool) error {
	_, err := hostapi.Bluetooth.Observe(ctx, &hostapi.BtObserve{Address: address, On: on})
	return mapErr(err)
}

func Disconnect(ctx context.Context, address string) error {
	_, err := hostapi.Bluetooth.Disconnect(ctx, &hostapi.BtAddress{Address: address})
	return mapErr(err)
}

// Bonded lists the devices paired in the phone's Bluetooth settings, the only ones Exchange reaches.
func Bonded(ctx context.Context) ([]Device, error) {
	out, err := hostapi.Bluetooth.Bonded(ctx, &hostapi.Empty{})
	if err != nil {
		return nil, mapErr(err)
	}
	devices := make([]Device, len(out.Devices))
	for i, d := range out.Devices {
		devices[i] = Device{Address: d.Address, Name: d.Name}
	}
	return devices, nil
}

// Exchange sends one line to the RFCOMM service uuid on a bonded device and returns the line it
// answers with, without the newline. The socket stays open for the next call until Hangup.
func Exchange(ctx context.Context, address, uuid string, line []byte) ([]byte, error) {
	out, err := hostapi.Bluetooth.Exchange(ctx, &hostapi.BtLine{Address: address, Uuid: uuid, Line: line})
	if err != nil {
		return nil, mapErr(err)
	}
	return out.Line, nil
}

func Hangup(ctx context.Context, address string) error {
	_, err := hostapi.Bluetooth.Hangup(ctx, &hostapi.BtAddress{Address: address})
	return mapErr(err)
}
