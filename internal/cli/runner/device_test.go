package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// A reply can be a func so a test can change what the phone shows between calls.
type fakeADB struct {
	mu      sync.Mutex
	replies map[string]any
	calls   []string
}

func (f *fakeADB) run(_ context.Context, args ...string) (string, error) {
	key := strings.Join(args, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, key)
	switch r := f.replies[key].(type) {
	case string:
		return r, nil
	case func() string:
		return r(), nil
	case error:
		return "", r
	}
	return "", fmt.Errorf("unexpected adb %s", key)
}

func (f *fakeADB) called(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, key)
}

func tempDevices(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "devices.json")
	old := devicesPath
	devicesPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { devicesPath = old })
}

func props(serial, id, model, abi string) (string, string) {
	return "-s " + serial + " shell getprop ro.serialno; getprop ro.product.model; getprop ro.product.cpu.abi; getprop ro.build.version.release",
		id + "\n" + model + "\n" + abi + "\n15\n"
}

func TestParseADBDevices(t *testing.T) {
	out := "* daemon started successfully\nList of devices attached\n" +
		"R58M12345      device usb:1-1 product:o1s model:SM_G991B transport_id:1\n" +
		"emulator-5554\tunauthorized\n" +
		"0123ABC  no permissions (missing udev rules?); see [http://developer.android.com/tools/device.html]\n" +
		"192.168.1.20:41235\toffline\n\n"
	got := parseADBDevices(out)
	want := []adbDevice{{"R58M12345", "device"}, {"emulator-5554", "unauthorized"}, {"0123ABC", "no permissions"}, {"192.168.1.20:41235", "offline"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestParseMDNS(t *testing.T) {
	out := "List of discovered mdns services\n" +
		"adb-R58M12345-AbCdEf\t_adb-tls-connect._tcp.\t192.168.1.20:41235\n" +
		"scorix-abc123\t_adb-tls-pairing._tcp\t192.168.1.20:37000\n"
	got := parseMDNS(out)
	if len(got) != 2 || got[0].Type != mdnsConnect || got[1].Instance != "scorix-abc123" || host(got[1].Addr) != "192.168.1.20" {
		t.Fatalf("got %+v", got)
	}
}

func TestPlatformToolsMajor(t *testing.T) {
	if v := platformToolsMajor("Android Debug Bridge version 1.0.41\nVersion 37.0.1-15733141\nInstalled as /x/adb\n"); v != 37 {
		t.Fatalf("got %d", v)
	}
	if v := platformToolsMajor("garbage"); v != 0 {
		t.Fatalf("got %d", v)
	}
}

func TestRenderQRIsSquareAndFramed(t *testing.T) {
	art, err := renderQR("WIFI:T:ADB;S:scorix-abc123;P:secret;;")
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSuffix(art, "\n"), "\n")
	bare := func(row string) string { return strings.TrimSuffix(strings.TrimPrefix(row, "\x1b[30;47m"), "\x1b[0m") }
	width := len([]rune(bare(rows[0])))
	// Two modules per row: the rows are half the width, rounded up.
	if want := (width + 1) / 2; len(rows) != want {
		t.Fatalf("%d rows for width %d, want %d", len(rows), width, want)
	}
	if strings.TrimSpace(bare(rows[0])) != "" {
		t.Fatal("no quiet zone on top")
	}
}

func TestResolvePicksTheOnlyDeviceAndBuildsItsABI(t *testing.T) {
	tempDevices(t)
	k, v := props("R58M12345", "R58M12345", "SM-G991B", "arm64-v8a")
	adb := &fakeADB{replies: map[string]any{"devices": "List of devices attached\nR58M12345\tdevice\n", k: v}}
	rec, err := resolveDevice(context.Background(), adb.run, "")
	if err != nil || rec.Serial != "R58M12345" || rec.ABI != "arm64-v8a" {
		t.Fatalf("got %+v %v", rec, err)
	}
	reg, _ := loadDevices()
	if reg.Default != "R58M12345" || len(reg.Devices) != 1 {
		t.Fatalf("not remembered: %+v", reg)
	}
}

func TestResolveRefusesToGuessBetweenTwo(t *testing.T) {
	tempDevices(t)
	adb := &fakeADB{replies: map[string]any{"devices": "List of devices attached\nA\tdevice\nB\tdevice\n"}}
	_, err := resolveDevice(context.Background(), adb.run, "")
	if code := exitCode(err); code != ExitUsage {
		t.Fatalf("exit %d: %v", code, err)
	}
}

func TestResolveSaysWhenThePhoneWaitsForApproval(t *testing.T) {
	tempDevices(t)
	adb := &fakeADB{replies: map[string]any{"devices": "List of devices attached\nR58M12345\tunauthorized\n"}}
	_, err := resolveDevice(context.Background(), adb.run, "")
	if code := exitCode(err); code != ExitMissing || !strings.Contains(err.Error(), "accept") {
		t.Fatalf("exit %d: %v", code, err)
	}
}

// A Wi-Fi phone comes back on another port; the default is found again by its serial in mDNS.
func TestResolveReconnectsTheDefaultOverWiFi(t *testing.T) {
	tempDevices(t)
	reg := &deviceRegistry{}
	reg.remember(DeviceRecord{ID: "R58M12345", Name: "SM-G991B", ABI: "arm64-v8a", Serial: "192.168.1.20:41235"})
	if err := reg.save(); err != nil {
		t.Fatal(err)
	}
	k, v := props("192.168.1.20:44001", "R58M12345", "SM-G991B", "arm64-v8a")
	adb := &fakeADB{replies: map[string]any{
		"devices":                    "List of devices attached\n",
		"mdns services":              "adb-R58M12345-AbCdEf\t_adb-tls-connect._tcp\t192.168.1.20:44001\n",
		"connect 192.168.1.20:44001": "connected to 192.168.1.20:44001\n",
		k:                            v,
	}}
	rec, err := resolveDevice(context.Background(), adb.run, "")
	if err != nil || rec.Serial != "192.168.1.20:44001" {
		t.Fatalf("got %+v %v", rec, err)
	}
	if reg, _ := loadDevices(); reg.Devices[0].Serial != "192.168.1.20:44001" {
		t.Fatalf("new port not remembered: %+v", reg.Devices)
	}
}

func TestResolveWithNothingConnected(t *testing.T) {
	tempDevices(t)
	adb := &fakeADB{replies: map[string]any{"devices": "List of devices attached\n"}}
	_, err := resolveDevice(context.Background(), adb.run, "")
	if code := exitCode(err); code != ExitMissing {
		t.Fatalf("exit %d: %v", code, err)
	}
}

// The phone on its "pairing code" screen advertises itself; the user only types the code.
func TestPairWithACodeFoundOverMDNS(t *testing.T) {
	tempDevices(t)
	k, v := props("192.168.1.20:44001", "R58M12345", "SM-G991B", "arm64-v8a")
	adb := &fakeADB{replies: map[string]any{
		"mdns services": "adb-pair-xyz\t_adb-tls-pairing._tcp\t192.168.1.20:37000\n" +
			"adb-R58M12345-AbCdEf\t_adb-tls-connect._tcp\t192.168.1.20:44001\n",
		"pair 192.168.1.20:37000 482913": "Successfully paired to 192.168.1.20:37000 [guid=adb-R58M12345]\n",
		"connect 192.168.1.20:44001":     "connected to 192.168.1.20:44001\n",
		k:                                v,
	}}
	res := &DeviceResult{}
	err := devicePair(context.Background(), adb.run, DeviceOptions{In: strings.NewReader("482913\n")}, res, 0)
	if err != nil || res.Paired == nil || res.Paired.ID != "R58M12345" {
		t.Fatalf("got %+v %v", res.Paired, err)
	}
	if reg, _ := loadDevices(); reg.Default != "R58M12345" {
		t.Fatal("the paired phone is not the default")
	}
}

// The QR path: the phone's pairing service carries the QR's name and pairs with its password.
func TestWaitForPairingTakesTheQRService(t *testing.T) {
	adb := &fakeADB{replies: map[string]any{
		"mdns services": "other-phone\t_adb-tls-pairing._tcp\t10.0.0.9:1000\nscorix-abc123\t_adb-tls-pairing._tcp\t10.0.0.5:37000\n",
	}}
	addr, code, err := waitForPairing(context.Background(), adb.run, "scorix-abc123", "pw", nil, 0)
	if err != nil || addr != "10.0.0.5:37000" || code != "pw" {
		t.Fatalf("got %s %s %v", addr, code, err)
	}
}

func TestPairRefusesAWrongCode(t *testing.T) {
	tempDevices(t)
	adb := &fakeADB{replies: map[string]any{"pair 10.0.0.5:37000 000000": "Failed: Wrong password or connection was dropped.\n"}}
	err := devicePair(context.Background(), adb.run, DeviceOptions{Address: "10.0.0.5:37000", Code: "000000"}, &DeviceResult{}, 0)
	if err == nil || !strings.Contains(err.Error(), "Wrong password") {
		t.Fatalf("err %v", err)
	}
}

func TestUseAndForget(t *testing.T) {
	tempDevices(t)
	reg := &deviceRegistry{}
	reg.remember(DeviceRecord{ID: "A1", Name: "Pixel 8", Serial: "10.0.0.5:44001"})
	reg.remember(DeviceRecord{ID: "B2", Name: "SM-G991B", Serial: "B2"})
	if err := reg.save(); err != nil {
		t.Fatal(err)
	}
	adb := &fakeADB{replies: map[string]any{"disconnect 10.0.0.5:44001": "disconnected\n"}}
	old := systemADB
	systemADB = func() (adbFunc, error) { return adb.run, nil }
	t.Cleanup(func() { systemADB = old })

	if err := DeviceUse(context.Background(), DeviceOptions{Device: "pixel 8"}); err != nil {
		t.Fatal(err)
	}
	if reg, _ := loadDevices(); reg.Default != "A1" {
		t.Fatalf("default %q", reg.Default)
	}
	if err := DeviceForget(context.Background(), DeviceOptions{Device: "A1"}); err != nil {
		t.Fatal(err)
	}
	reg, _ = loadDevices()
	if reg.Default != "" || len(reg.Devices) != 1 || !adb.called("disconnect 10.0.0.5:44001") {
		t.Fatalf("after forget: %+v, calls %v", reg, adb.calls)
	}
	if err := DeviceUse(context.Background(), DeviceOptions{Device: "nope"}); exitCode(err) != ExitUsage {
		t.Fatalf("use of an unknown device: %v", err)
	}
}

func TestNewestAPK(t *testing.T) {
	dir := t.TempDir()
	if _, err := newestAPK(dir); exitCode(err) != ExitUsage {
		t.Fatalf("empty dir: %v", err)
	}
	old, newer := filepath.Join(dir, "app-release.apk"), filepath.Join(dir, "app-debug.apk")
	for i, p := range []string{old, newer} {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(time.Duration(i-2) * time.Minute)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := newestAPK(dir); err != nil || got != newer {
		t.Fatalf("got %s %v", got, err)
	}
}

// Outside a project there is no app id to start: a usage error, before any device is touched.
func TestRunNeedsTheProject(t *testing.T) {
	tempDevices(t)
	adb := &fakeADB{replies: map[string]any{}}
	old := systemADB
	systemADB = func() (adbFunc, error) { return adb.run, nil }
	t.Cleanup(func() { systemADB = old })
	dir := t.TempDir()
	apk := filepath.Join(dir, "app.apk")
	if err := os.WriteFile(apk, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	err := DeviceRun(context.Background(), DeviceOptions{APK: apk, Dir: dir})
	if exitCode(err) != ExitUsage || len(adb.calls) != 0 {
		t.Fatalf("err %v, adb calls %v", err, adb.calls)
	}
}

// An agent reads `devices` from list even when there are none, and finds no such key elsewhere.
func TestDevicesKeyOnlyOnList(t *testing.T) {
	b, _ := json.Marshal(DeviceResult{Devices: []DeviceInfo{}})
	if !strings.Contains(string(b), `"devices":[]`) {
		t.Fatalf("list: %s", b)
	}
	b, _ = json.Marshal(DeviceResult{APK: "a.apk"})
	if strings.Contains(string(b), "devices") {
		t.Fatalf("run: %s", b)
	}
}

// With stdin closed nobody can type a code, so a phone on the code screen must not end the wait
// for the QR.
func TestWaitForPairingKeepsWaitingForTheQRWhenStdinIsClosed(t *testing.T) {
	n := 0
	adb := &fakeADB{replies: map[string]any{
		"mdns services": func() string {
			n++
			if n == 1 {
				return "other-phone\t_adb-tls-pairing._tcp\t10.0.0.9:1000\n"
			}
			return "other-phone\t_adb-tls-pairing._tcp\t10.0.0.9:1000\nscorix-abc123\t_adb-tls-pairing._tcp\t10.0.0.5:37000\n"
		},
	}}
	addr, code, err := waitForPairing(context.Background(), adb.run, "scorix-abc123", "pw", strings.NewReader(""), 0)
	if err != nil || addr != "10.0.0.5:37000" || code != "pw" {
		t.Fatalf("got %s %s %v", addr, code, err)
	}
}

// A device with no model name must not stand in for an unset default.
func TestResolveDoesNotTakeANamelessDeviceAsTheDefault(t *testing.T) {
	tempDevices(t)
	reg := &deviceRegistry{Devices: []DeviceRecord{{ID: "C3", Serial: "C3"}}}
	if err := reg.save(); err != nil {
		t.Fatal(err)
	}
	adb := &fakeADB{replies: map[string]any{"devices": "List of devices attached\nA1\tdevice\nB2\tdevice\n"}}
	if _, err := resolveDevice(context.Background(), adb.run, ""); exitCode(err) != ExitUsage {
		t.Fatalf("err %v", err)
	}
}

func TestForgetDisconnectsAnMDNSConnectedDevice(t *testing.T) {
	tempDevices(t)
	serial := "adb-A1-AbCdEf._adb-tls-connect._tcp"
	reg := &deviceRegistry{Devices: []DeviceRecord{{ID: "A1", Name: "Pixel 8", Serial: serial}}}
	if err := reg.save(); err != nil {
		t.Fatal(err)
	}
	adb := &fakeADB{replies: map[string]any{"disconnect " + serial: "disconnected\n"}}
	old := systemADB
	systemADB = func() (adbFunc, error) { return adb.run, nil }
	t.Cleanup(func() { systemADB = old })
	if err := DeviceForget(context.Background(), DeviceOptions{Device: "A1"}); err != nil || !adb.called("disconnect "+serial) {
		t.Fatalf("err %v, calls %v", err, adb.calls)
	}
}
