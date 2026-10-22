//go:build !linux || android

package bluetooth

import "net"

// Peripheral and Listener are Linux only; elsewhere they report unavailable.
type Peripheral struct{}

func Serve(Service) (*Peripheral, error)        { return nil, ErrUnavailable }
func (*Peripheral) Advertise(bool) error        { return ErrUnavailable }
func (*Peripheral) Notify(string, []byte) error { return ErrUnavailable }
func (*Peripheral) Close() error                { return nil }

type Listener struct{}

func ListenRFCOMM(string, string) (*Listener, error) { return nil, ErrUnavailable }
func (*Listener) Accept() (net.Conn, error)          { return nil, ErrUnavailable }
func (*Listener) Close() error                       { return nil }
func (*Listener) Addr() net.Addr                     { return nil }
