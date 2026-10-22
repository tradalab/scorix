//go:build linux && !android

package bluetooth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

const (
	bluez          = "org.bluez"
	ifaceProps     = "org.freedesktop.DBus.Properties"
	ifaceObjMgr    = "org.freedesktop.DBus.ObjectManager"
	ifaceGattMgr   = "org.bluez.GattManager1"
	ifaceAdvMgr    = "org.bluez.LEAdvertisingManager1"
	ifaceService   = "org.bluez.GattService1"
	ifaceChar      = "org.bluez.GattCharacteristic1"
	ifaceAdv       = "org.bluez.LEAdvertisement1"
	errNotSupport  = "org.bluez.Error.NotSupported"
	errFailed      = "org.bluez.Error.Failed"
	errInvalidOffs = "org.bluez.Error.InvalidOffset"
)

// Peripheral is a GATT service registered with BlueZ. It is reachable by devices already paired
// or bonded at once; Advertise makes it findable by everybody else in range.
type Peripheral struct {
	conn    *dbus.Conn
	adapter dbus.ObjectPath
	app     dbus.ObjectPath
	adv     dbus.ObjectPath
	chars   map[string]*char

	mu          sync.Mutex
	advertising bool
}

// Serve registers svc on the first adapter. The session user can do this: BlueZ's D-Bus policy
// lets any user register, which is what lets the app stay unprivileged.
func Serve(svc Service) (*Peripheral, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	adapter, err := findAdapter(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	id := make([]byte, 4)
	if _, err := rand.Read(id); err != nil {
		conn.Close()
		return nil, err
	}
	p := &Peripheral{conn: conn, adapter: adapter, chars: map[string]*char{},
		app: dbus.ObjectPath("/dev/scorix/bluetooth/app" + hex.EncodeToString(id))}
	p.adv = p.app + "/adv0"
	if err := p.export(svc); err != nil {
		conn.Close()
		return nil, err
	}
	mgr := conn.Object(bluez, adapter)
	if err := mgr.Call(ifaceGattMgr+".RegisterApplication", 0, p.app, map[string]dbus.Variant{}).Err; err != nil {
		conn.Close()
		return nil, fmt.Errorf("bluetooth: register GATT application: %w", err)
	}
	return p, nil
}

// Advertise starts or stops announcing the service UUID. Only one UUID fits next to the flags in
// 31 bytes of legacy advertising, so nothing else is announced.
func (p *Peripheral) Advertise(on bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if on == p.advertising {
		return nil
	}
	mgr := p.conn.Object(bluez, p.adapter)
	var err error
	if on {
		err = mgr.Call(ifaceAdvMgr+".RegisterAdvertisement", 0, p.adv, map[string]dbus.Variant{}).Err
	} else {
		err = mgr.Call(ifaceAdvMgr+".UnregisterAdvertisement", 0, p.adv).Err
	}
	if err != nil {
		return fmt.Errorf("bluetooth: advertise %v: %w", on, err)
	}
	p.advertising = on
	return nil
}

// Notify pushes value to every client subscribed to the characteristic.
func (p *Peripheral) Notify(uuid string, value []byte) error {
	c, ok := p.chars[strings.ToLower(uuid)]
	if !ok || !c.def.Notify {
		return fmt.Errorf("bluetooth: %s does not notify", uuid)
	}
	return p.conn.Emit(c.path, ifaceProps+".PropertiesChanged", ifaceChar,
		map[string]dbus.Variant{"Value": dbus.MakeVariant(value)}, []string{})
}

// Close withdraws the service and the advertisement; BlueZ also drops both when the bus
// connection goes, so a crash does not leave the machine announcing a dead service.
func (p *Peripheral) Close() error {
	_ = p.Advertise(false)
	mgr := p.conn.Object(bluez, p.adapter)
	err := mgr.Call(ifaceGattMgr+".UnregisterApplication", 0, p.app).Err
	return errors.Join(err, p.conn.Close())
}

func findAdapter(conn *dbus.Conn) (dbus.ObjectPath, error) {
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := conn.Object(bluez, "/").Call(ifaceObjMgr+".GetManagedObjects", 0).Store(&objs); err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var best dbus.ObjectPath
	for path, ifaces := range objs {
		_, gatt := ifaces[ifaceGattMgr]
		_, adv := ifaces[ifaceAdvMgr]
		if gatt && adv && (best == "" || path < best) {
			best = path
		}
	}
	if best == "" {
		return "", ErrUnavailable
	}
	return best, nil
}

// props answers org.freedesktop.DBus.Properties for one exported object.
type props struct {
	iface  string
	values map[string]dbus.Variant
}

func (o *props) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	if v, ok := o.values[name]; ok && iface == o.iface {
		return v, nil
	}
	return dbus.Variant{}, dbus.MakeFailedError(fmt.Errorf("no property %s.%s", iface, name))
}

func (o *props) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	if iface != o.iface {
		return map[string]dbus.Variant{}, nil
	}
	return o.values, nil
}

func (o *props) Set(string, string, dbus.Variant) *dbus.Error {
	return dbus.NewError(errNotSupport, nil)
}

type char struct {
	def  Characteristic
	path dbus.ObjectPath
}

// ReadValue honours `offset`, which BlueZ passes for each ATT Read Blob of a value longer than
// the MTU: the whole value is read again and sliced, so a callback must return the same bytes.
func (c *char) ReadValue(opts map[string]dbus.Variant) ([]byte, *dbus.Error) {
	if c.def.Read == nil {
		return nil, dbus.NewError(errNotSupport, nil)
	}
	v, err := c.def.Read()
	if err != nil {
		return nil, dbus.NewError(errFailed, []any{err.Error()})
	}
	off := offset(opts)
	if off > len(v) {
		return nil, dbus.NewError(errInvalidOffs, nil)
	}
	return v[off:], nil
}

// WriteValue takes a value in one piece only: the client is expected to raise the MTU first
// (Android asks for 517), because BlueZ hands prepared writes over chunk by chunk with no end.
func (c *char) WriteValue(value []byte, opts map[string]dbus.Variant) *dbus.Error {
	if c.def.Write == nil {
		return dbus.NewError(errNotSupport, nil)
	}
	if offset(opts) != 0 {
		return dbus.NewError(errInvalidOffs, nil)
	}
	if err := c.def.Write(value); err != nil {
		return dbus.NewError(errFailed, []any{err.Error()})
	}
	return nil
}

func (c *char) StartNotify() *dbus.Error { return nil }
func (c *char) StopNotify() *dbus.Error  { return nil }

type adv struct{}

func (adv) Release() *dbus.Error { return nil }

type objectManager struct {
	objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
}

func (m *objectManager) GetManagedObjects() (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, *dbus.Error) {
	return m.objects, nil
}

func offset(opts map[string]dbus.Variant) int {
	if v, ok := opts["offset"]; ok {
		if n, ok := v.Value().(uint16); ok {
			return int(n)
		}
	}
	return 0
}

type exported struct {
	v     any
	path  dbus.ObjectPath
	iface string
}

func (p *Peripheral) export(svc Service) error {
	svcPath := p.app + "/service0"
	objects := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{}
	svcProps := map[string]dbus.Variant{
		"UUID": dbus.MakeVariant(strings.ToLower(svc.UUID)), "Primary": dbus.MakeVariant(true)}
	objects[svcPath] = map[string]map[string]dbus.Variant{ifaceService: svcProps}
	exports := []exported{{&props{ifaceService, svcProps}, svcPath, ifaceProps}}

	for i, def := range svc.Chars {
		var flags []string
		if def.Read != nil {
			flags = append(flags, "read")
		}
		if def.Write != nil {
			flags = append(flags, "write")
		}
		if def.Notify {
			flags = append(flags, "notify")
		}
		c := &char{def: def, path: dbus.ObjectPath(fmt.Sprintf("%s/char%d", svcPath, i))}
		p.chars[strings.ToLower(def.UUID)] = c
		cp := map[string]dbus.Variant{
			"UUID":    dbus.MakeVariant(strings.ToLower(def.UUID)),
			"Service": dbus.MakeVariant(svcPath),
			"Flags":   dbus.MakeVariant(flags),
		}
		objects[c.path] = map[string]map[string]dbus.Variant{ifaceChar: cp}
		exports = append(exports, exported{c, c.path, ifaceChar}, exported{&props{ifaceChar, cp}, c.path, ifaceProps})
	}

	advProps := map[string]dbus.Variant{
		"Type":         dbus.MakeVariant("peripheral"),
		"ServiceUUIDs": dbus.MakeVariant([]string{strings.ToLower(svc.UUID)}),
	}
	exports = append(exports,
		exported{adv{}, p.adv, ifaceAdv},
		exported{&props{ifaceAdv, advProps}, p.adv, ifaceProps},
		exported{&objectManager{objects}, p.app, ifaceObjMgr})
	for _, e := range exports {
		if err := p.conn.Export(e.v, e.path, e.iface); err != nil {
			return err
		}
	}
	return nil
}
