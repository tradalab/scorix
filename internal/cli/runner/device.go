package runner

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rsc.io/qr"
)

type adbFunc func(ctx context.Context, args ...string) (string, error)

// A var so tests can stand in for the phone.
var systemADB = func() (adbFunc, error) {
	path, err := findADB()
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, path, args...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return string(out), toolchainError(err, stderr.String())
		}
		return string(out), nil
	}, nil
}

// devicesPath is where paired devices are remembered, per user rather than per project: a phone
// serves every app on the machine.
var devicesPath = func() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "scorix", "devices.json"), nil
}

// DeviceRecord is a device scorix has used. ID is ro.serialno, the one name that survives the
// switch between USB and Wi-Fi; the adb serial of a Wi-Fi device changes with every reconnect.
type DeviceRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	ABI     string `json:"abi"`
	Android string `json:"android"`
	Serial  string `json:"serial"`
}

type deviceRegistry struct {
	Default string         `json:"default,omitempty"`
	Devices []DeviceRecord `json:"devices"`
}

func loadDevices() (*deviceRegistry, error) {
	path, err := devicesPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &deviceRegistry{}, nil
	}
	if err != nil {
		return nil, err
	}
	var r deviceRegistry
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &r, nil
}

func (r *deviceRegistry) save() error {
	path, err := devicesPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// find matches what a user types: the ID, the model name or the current adb serial.
func (r *deviceRegistry) find(key string) *DeviceRecord {
	if key == "" {
		return nil
	}
	for i, d := range r.Devices {
		if d.ID == key || d.Serial == key || strings.EqualFold(d.Name, key) {
			return &r.Devices[i]
		}
	}
	return nil
}

// Only `device use` and `device pair` move an existing default: `-s` picks for one run.
func (r *deviceRegistry) remember(d DeviceRecord) {
	if old := r.find(d.ID); old != nil {
		*old = d
	} else {
		r.Devices = append(r.Devices, d)
	}
	if r.find(r.Default) == nil {
		r.Default = d.ID
	}
}

type adbDevice struct {
	Serial, State string
}

// parseADBDevices reads `adb devices`; a state can be several words ("no permissions (...)").
func parseADBDevices(out string) []adbDevice {
	var list []adbDevice
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || strings.HasPrefix(line, "List of") || strings.HasPrefix(line, "*") {
			continue
		}
		state := f[1]
		if state == "no" {
			state = "no permissions"
		}
		list = append(list, adbDevice{Serial: f[0], State: state})
	}
	return list
}

type mdnsService struct {
	Instance, Type, Addr string
}

const (
	mdnsPairing = "_adb-tls-pairing._tcp"
	mdnsConnect = "_adb-tls-connect._tcp"
)

func parseMDNS(out string) []mdnsService {
	var list []mdnsService
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || !strings.HasPrefix(f[1], "_adb") {
			continue
		}
		list = append(list, mdnsService{Instance: f[0], Type: strings.TrimSuffix(f[1], "."), Addr: f[2]})
	}
	return list
}

func host(addr string) string {
	if i := strings.LastIndexByte(addr, ':'); i > 0 {
		return addr[:i]
	}
	return addr
}

func identify(ctx context.Context, adb adbFunc, serial string) (DeviceRecord, error) {
	out, err := adb(ctx, "-s", serial, "shell",
		"getprop ro.serialno; getprop ro.product.model; getprop ro.product.cpu.abi; getprop ro.build.version.release")
	if err != nil {
		return DeviceRecord{}, err
	}
	f := strings.Split(strings.ReplaceAll(out, "\r", ""), "\n")
	if len(f) < 4 || f[0] == "" {
		return DeviceRecord{}, fmt.Errorf("%s did not report its serial number", serial)
	}
	return DeviceRecord{ID: f[0], Name: f[1], ABI: f[2], Android: f[3], Serial: serial}, nil
}

type DeviceOptions struct {
	// Pair: an address and code typed from the phone's "Pair with pairing code" screen; empty
	// shows a QR code instead. Use and Forget: the device.
	Address string
	Code    string
	Device  string
	// Run: the APK, empty for the newest in the project's artifacts/, and the project root.
	APK     string
	Dir     string
	In      io.Reader
	JSONOut io.Writer
}

type DeviceInfo struct {
	DeviceRecord
	State   string `json:"state"`
	Default bool   `json:"default,omitempty"`
}

type DeviceResult struct {
	Devices []DeviceInfo `json:"devices,omitzero"`
	Paired  *DeviceInfo  `json:"paired,omitempty"`
	APK     string       `json:"apk,omitempty"`
	AppID   string       `json:"appId,omitempty"`
}

// deviceRun reports usage before looking for adb: a wrong call is wrong whether or not adb exists.
func deviceRun(ctx context.Context, opt DeviceOptions, command string, usage error, run func(context.Context, adbFunc, *DeviceResult) error) error {
	res := &DeviceResult{}
	err := usage
	var adb adbFunc
	if err == nil {
		adb, err = systemADB()
	}
	if err == nil {
		err = run(ctx, adb, res)
	}
	if opt.JSONOut != nil {
		return EmitJSON(opt.JSONOut, command, res, err)
	}
	return err
}

func DeviceList(ctx context.Context, opt DeviceOptions) error {
	return deviceRun(ctx, opt, "device list", nil, func(ctx context.Context, adb adbFunc, res *DeviceResult) error {
		return deviceList(ctx, adb, res)
	})
}

// deviceList shows what adb sees now and what was used before, so an offline Wi-Fi phone is still
// on the list under its name.
func deviceList(ctx context.Context, adb adbFunc, res *DeviceResult) error {
	reg, err := loadDevices()
	if err != nil {
		return err
	}
	out, err := adb(ctx, "devices")
	if err != nil {
		return err
	}
	res.Devices = []DeviceInfo{}
	seen := map[string]bool{}
	for _, d := range parseADBDevices(out) {
		info := DeviceInfo{DeviceRecord: DeviceRecord{Serial: d.Serial}, State: d.State}
		if d.State == "device" {
			if rec, err := identify(ctx, adb, d.Serial); err == nil {
				info.DeviceRecord = rec
			}
		}
		if rec := reg.find(d.Serial); rec != nil && info.ID == "" {
			info.ID, info.Name, info.ABI, info.Android = rec.ID, rec.Name, rec.ABI, rec.Android
		}
		info.Default = info.ID != "" && info.ID == reg.Default
		seen[info.ID] = true
		res.Devices = append(res.Devices, info)
	}
	for _, rec := range reg.Devices {
		if !seen[rec.ID] {
			res.Devices = append(res.Devices, DeviceInfo{DeviceRecord: rec, State: "offline", Default: rec.ID == reg.Default})
		}
	}
	if len(res.Devices) == 0 {
		fmt.Println("No device. Plug one in with USB debugging on, or `scorix device pair` over Wi-Fi.")
	}
	for _, d := range res.Devices {
		mark := " "
		if d.Default {
			mark = "*"
		}
		fmt.Printf("%s %-24s %-10s %-12s Android %-5s %s\n", mark, d.Name, d.State, d.ABI, d.Android, d.Serial)
		if d.State == "unauthorized" {
			fmt.Println("    accept the USB debugging prompt on the phone")
		}
	}
	return nil
}

func DevicePair(ctx context.Context, opt DeviceOptions) error {
	return deviceRun(ctx, opt, "device pair", pairUsage(opt), func(ctx context.Context, adb adbFunc, res *DeviceResult) error {
		return devicePair(ctx, adb, opt, res, time.Second)
	})
}

func pairUsage(opt DeviceOptions) error {
	if (opt.Address == "") != (opt.Code == "") {
		return UsageError(errors.New("give both the address and the code the phone shows, or neither for a QR code"))
	}
	return nil
}

// pairWait is how long the QR stays up: long enough to find the setting on the phone.
const pairWait = 3 * time.Minute

// devicePair uses Android Studio's QR format: the phone advertises a pairing service named after the
// QR and pairs with its password. A phone on the code screen advertises too, so only the code is typed.
func devicePair(ctx context.Context, adb adbFunc, opt DeviceOptions, res *DeviceResult, poll time.Duration) error {
	addr, code := opt.Address, opt.Code
	if addr == "" {
		name, err := randomToken(6)
		if err != nil {
			return err
		}
		name = "scorix-" + name
		password, err := randomToken(12)
		if err != nil {
			return err
		}
		art, err := renderQR("WIFI:T:ADB;S:" + name + ";P:" + password + ";;")
		if err != nil {
			return err
		}
		fmt.Print(art)
		fmt.Println("On the phone: Developer options > Wireless debugging > Pair device with QR code.")
		fmt.Println("Or choose Pair device with pairing code; the code is then asked for here.")
		if addr, code, err = waitForPairing(ctx, adb, name, password, opt.In, poll); err != nil {
			return err
		}
	}
	out, err := adb(ctx, "pair", addr, code)
	if err != nil {
		return err
	}
	if !strings.Contains(out, "Successfully paired") {
		return fmt.Errorf("adb pair: %s", strings.TrimSpace(out))
	}
	fmt.Println("Paired. Connecting...")
	serial, err := connectAfterPairing(ctx, adb, host(addr), poll)
	if err != nil {
		return err
	}
	rec, err := identify(ctx, adb, serial)
	if err != nil {
		return err
	}
	reg, err := loadDevices()
	if err != nil {
		return err
	}
	reg.remember(rec)
	reg.Default = rec.ID
	if err := reg.save(); err != nil {
		return err
	}
	res.Paired = &DeviceInfo{DeviceRecord: rec, State: "device", Default: true}
	fmt.Printf("Connected to %s (%s); `scorix mobile dev` now uses it.\n", rec.Name, serial)
	return nil
}

// waitForPairing watches mDNS for the phone: the QR's own service pairs with the QR's password, any
// other pairing service is a phone showing a code, which the user then types.
func waitForPairing(ctx context.Context, adb adbFunc, name, password string, in io.Reader, poll time.Duration) (addr, code string, err error) {
	deadline := time.Now().Add(pairWait)
	asked := map[string]bool{}
	lines := bufio.NewReader(in)
	for time.Now().Before(deadline) {
		out, err := adb(ctx, "mdns", "services")
		if err != nil {
			return "", "", err
		}
		for _, s := range parseMDNS(out) {
			if s.Type != mdnsPairing {
				continue
			}
			if s.Instance == name {
				return s.Addr, password, nil
			}
			if in != nil && !asked[s.Addr] {
				asked[s.Addr] = true
				fmt.Printf("A phone at %s shows a pairing code. Type it: ", s.Addr)
				line, err := lines.ReadString('\n')
				if code := strings.TrimSpace(line); code != "" {
					return s.Addr, code, nil
				}
				// Stdin is closed (a script, an agent): nobody can type a code, but the QR still works.
				if err != nil {
					fmt.Println()
					in = nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(poll):
		}
	}
	return "", "", errors.New("no phone came to pair; is it on the same Wi-Fi with Wireless debugging on?")
}

// connectAfterPairing finds the phone's debugging port, which is not the pairing port, by the host
// they share.
func connectAfterPairing(ctx context.Context, adb adbFunc, phone string, poll time.Duration) (string, error) {
	for i := 0; i < 30; i++ {
		out, err := adb(ctx, "mdns", "services")
		if err != nil {
			return "", err
		}
		for _, s := range parseMDNS(out) {
			if s.Type == mdnsConnect && host(s.Addr) == phone {
				return connect(ctx, adb, s.Addr)
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(poll):
		}
	}
	return "", errors.New("paired, but the phone's debugging port did not show up; `adb connect <ip:port>` with the port on its Wireless debugging screen")
}

func connect(ctx context.Context, adb adbFunc, addr string) (string, error) {
	out, err := adb(ctx, "connect", addr)
	if err != nil {
		return "", err
	}
	if !strings.Contains(out, "connected to") {
		return "", fmt.Errorf("adb connect %s: %s", addr, strings.TrimSpace(out))
	}
	return addr, nil
}

// DeviceRun is `mobile dev` without the build: it installs an APK built elsewhere (Docker, CI) on
// the device mobile dev would pick, starts it and follows its log. For a machine with no JDK.
func DeviceRun(ctx context.Context, opt DeviceOptions) error {
	return deviceRun(ctx, opt, "device run", nil, func(ctx context.Context, adb adbFunc, res *DeviceResult) error {
		apk := opt.APK
		if apk == "" {
			var err error
			if apk, err = newestAPK(filepath.Join(opt.Dir, "artifacts")); err != nil {
				return err
			}
		}
		// Only a debug APK the CLI built ends in -debug.apk; any other is taken for a release.
		appID, err := apkAppID(ctx, apk, opt.Dir, strings.HasSuffix(apk, "-debug.apk"))
		if err != nil {
			return UsageError(fmt.Errorf("%w - run from the project root, or pass --dir", err))
		}
		res.APK, res.AppID = apk, appID
		target, err := resolveDevice(ctx, adb, opt.Device)
		if err != nil {
			return err
		}
		fmt.Printf("==> Device %s (%s, %s)\n", target.Name, target.Serial, target.ABI)
		dev, err := deviceCmd(ctx, target.Serial)
		if err != nil {
			return err
		}
		if err := installAPK(dev, apk, appID); err != nil {
			return err
		}
		return startAndFollow(dev, appID, []string{"shell", "am", "start", "-n", launcherActivity(dev, appID)})
	})
}

// newestAPK picks the APK built last: a Docker build leaves the debug and release ones side by side.
func newestAPK(dir string) (string, error) {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.apk"))
	newest, at := "", time.Time{}
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.ModTime().After(at) {
			newest, at = m, fi.ModTime()
		}
	}
	if newest == "" {
		return "", UsageError(fmt.Errorf("no APK in %s - build one, or pass its path", dir))
	}
	return newest, nil
}

func DeviceUse(ctx context.Context, opt DeviceOptions) error {
	return deviceRun(ctx, opt, "device use", nil, func(context.Context, adbFunc, *DeviceResult) error {
		reg, err := loadDevices()
		if err != nil {
			return err
		}
		rec := reg.find(opt.Device)
		if rec == nil {
			return UsageError(fmt.Errorf("no device %q has been used; `scorix device list` shows the names", opt.Device))
		}
		reg.Default = rec.ID
		fmt.Printf("`scorix mobile dev` now uses %s.\n", rec.Name)
		return reg.save()
	})
}

// DeviceForget drops a device from the list and disconnects it. The phone keeps trusting this
// machine until "Revoke debugging authorizations" there.
func DeviceForget(ctx context.Context, opt DeviceOptions) error {
	return deviceRun(ctx, opt, "device forget", nil, func(ctx context.Context, adb adbFunc, _ *DeviceResult) error {
		reg, err := loadDevices()
		if err != nil {
			return err
		}
		rec := reg.find(opt.Device)
		if rec == nil {
			return UsageError(fmt.Errorf("no device %q has been used", opt.Device))
		}
		// Only network transports disconnect, and an mDNS-connected serial has no ":".
		if strings.Contains(rec.Serial, ":") || strings.Contains(rec.Serial, mdnsConnect) {
			_, _ = adb(ctx, "disconnect", rec.Serial)
		}
		id := rec.ID
		reg.Devices = slices.DeleteFunc(reg.Devices, func(d DeviceRecord) bool { return d.ID == id })
		if reg.Default == id {
			reg.Default = ""
		}
		fmt.Printf("Forgot %s.\n", id)
		return reg.save()
	})
}

// resolveDevice picks the target of `mobile dev`: the one asked for, the only one connected, or
// the remembered default, reconnecting a Wi-Fi phone whose port has moved.
func resolveDevice(ctx context.Context, adb adbFunc, want string) (DeviceRecord, error) {
	reg, err := loadDevices()
	if err != nil {
		return DeviceRecord{}, err
	}
	out, err := adb(ctx, "devices")
	if err != nil {
		return DeviceRecord{}, err
	}
	var online []string
	for _, d := range parseADBDevices(out) {
		switch d.State {
		case "device":
			online = append(online, d.Serial)
		case "unauthorized":
			if want == "" || want == d.Serial {
				return DeviceRecord{}, exitErrorf(ExitMissing, "missing_prerequisite", "%s is waiting: accept the USB debugging prompt on the phone", d.Serial)
			}
		}
	}

	pick := ""
	var target *DeviceRecord
	switch {
	case want != "":
		if slices.Contains(online, want) {
			pick = want
		} else if target = reg.find(want); target == nil {
			return DeviceRecord{}, UsageError(fmt.Errorf("no device %q connected or remembered; `scorix device list`", want))
		}
	case len(online) == 1:
		pick = online[0]
	default:
		if target = reg.find(reg.Default); target == nil && len(online) > 1 {
			return DeviceRecord{}, UsageError(fmt.Errorf("%d devices connected; choose one with -s <serial> or `scorix device use`", len(online)))
		}
	}
	if target != nil {
		if slices.Contains(online, target.Serial) {
			pick = target.Serial
		} else if pick, err = reconnect(ctx, adb, target.ID); err != nil {
			return DeviceRecord{}, err
		}
	}
	if pick == "" {
		return DeviceRecord{}, exitErrorf(ExitMissing, "missing_prerequisite", "no device: plug one in with USB debugging on, or `scorix device pair` over Wi-Fi")
	}
	rec, err := identify(ctx, adb, pick)
	if err != nil {
		return DeviceRecord{}, err
	}
	reg.remember(rec)
	return rec, reg.save()
}

// reconnect finds a paired phone on Wi-Fi again. Its debugging service is named after its serial
// number, which is what stays fixed while the port changes.
func reconnect(ctx context.Context, adb adbFunc, id string) (string, error) {
	out, err := adb(ctx, "mdns", "services")
	if err != nil {
		return "", err
	}
	for _, s := range parseMDNS(out) {
		if s.Type == mdnsConnect && strings.HasPrefix(s.Instance, "adb-"+id+"-") {
			fmt.Printf("==> Reconnecting %s at %s\n", id, s.Addr)
			return connect(ctx, adb, s.Addr)
		}
	}
	return "", exitErrorf(ExitMissing, "missing_prerequisite", "%s is not connected and not found on Wi-Fi; turn on Wireless debugging, or plug it in", id)
}

func randomToken(n int) (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, n)
	for i := range b {
		k, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b[i] = alphabet[k.Int64()]
	}
	return string(b), nil
}

// renderQR draws text as half-block characters, two modules per character row, with explicit
// black on white so a dark terminal does not invert it into something a camera cannot read.
func renderQR(text string) (string, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 2
	dark := func(x, y int) bool {
		x, y = x-quiet, y-quiet
		return x >= 0 && y >= 0 && x < code.Size && y < code.Size && code.Black(x, y)
	}
	var b strings.Builder
	n := code.Size + 2*quiet
	for y := 0; y < n; y += 2 {
		b.WriteString("\x1b[30;47m")
		for x := 0; x < n; x++ {
			switch top, bottom := dark(x, y), dark(x, y+1); {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\x1b[0m\n")
	}
	return b.String(), nil
}

// platformToolsMajor reads "Version 37.0.1-15733141" out of `adb version`; 0 when it is not there.
func platformToolsMajor(out string) int {
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Version "); ok {
			major, _, _ := strings.Cut(rest, ".")
			n := 0
			if _, err := fmt.Sscanf(major, "%d", &n); err == nil {
				return n
			}
		}
	}
	return 0
}
