// Package bluetooth drives Bluetooth from Go: on Linux a GATT server and an RFCOMM listener through
// BlueZ, on Android the client calls a phone makes. Other platforms report unavailable.
package bluetooth

import (
	"errors"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/native"
)

// Service is one primary GATT service with its characteristics.
type Service struct {
	UUID  string
	Chars []Characteristic
}

// Characteristic says what a client may do with it: Read nil means not readable, Write nil means
// not writable. Notify lets the server push values with Peripheral.Notify.
type Characteristic struct {
	UUID   string
	Read   func() ([]byte, error)
	Write  func([]byte) error
	Notify bool
}

// ErrUnavailable is this platform or this machine having no Bluetooth LE to offer.
var ErrUnavailable = fault.New(fault.CodeUnavailable, "bluetooth LE is not available here")

func mapErr(err error) error {
	if errors.Is(err, native.ErrUnavailable) {
		return ErrUnavailable
	}
	var ne *native.Error
	if errors.As(err, &ne) {
		return fault.New(ne.Code, ne.Message)
	}
	return err
}
