package runner

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheInfoPlistCarriesTheAppsIdentityVersionSchemesAndTheUsageItsModulesNeed(t *testing.T) {
	bc := &BuildContext{Identifier: "com.example.demo", ProductName: "Demo & Co", Version: "1.5.2", Protocols: []string{"demo"}}
	p := iosPlistFor(bc, []string{"scanner", "biometric", "widget", "clipboard", "scanner"}, false)
	if p.Build != "1005002" {
		t.Errorf("build = %s, want the versionCode Android uses", p.Build)
	}
	if p.AppGroup != "group.com.example.demo" {
		t.Errorf("app group = %q, want one when the widget module is on", p.AppGroup)
	}
	out := renderIOSPlist(p)
	if err := xml.Unmarshal([]byte(out), new(struct{})); err != nil {
		t.Fatalf("plist is not well-formed XML: %v\n%s", err, out)
	}
	for _, want := range []string{
		"<key>CFBundleIdentifier</key><string>com.example.demo</string>",
		"<key>CFBundleName</key><string>Demo &amp; Co</string>",
		"<key>CFBundleShortVersionString</key><string>1.5.2</string>",
		"<key>CFBundleVersion</key><string>1005002</string>",
		"<string>iPhoneOS</string>",
		"<key>CFBundleURLSchemes</key><array><string>demo</string></array>",
		"<key>NSCameraUsageDescription</key>",
		"<key>NSPhotoLibraryUsageDescription</key>",
		"<key>NSFaceIDUsageDescription</key>",
		"<key>ScorixAppGroup</key><string>group.com.example.demo</string>",
		"<key>ITSAppUsesNonExemptEncryption</key><false/>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Info.plist lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "NSCameraUsageDescription") != 1 {
		t.Error("a usage string written twice")
	}
	ent := renderIOSEntitlements(p)
	if !strings.Contains(ent, "<string>group.com.example.demo</string>") {
		t.Errorf("entitlements lack the app group:\n%s", ent)
	}
}

func TestASimulatorBuildDeclaresTheSimulatorAndAnAppWithoutWidgetsHasNoGroup(t *testing.T) {
	p := iosPlistFor(&BuildContext{Identifier: "com.example.demo", AppName: "demo", Version: "0.1.0"}, nil, true)
	out := renderIOSPlist(p)
	if !strings.Contains(out, "<string>iPhoneSimulator</string>") || !strings.Contains(out, "<key>CFBundleName</key><string>demo</string>") {
		t.Errorf("simulator plist:\n%s", out)
	}
	if strings.Contains(out, "ScorixAppGroup") || strings.Contains(renderIOSEntitlements(p), "application-groups") {
		t.Error("an app group without the widget module")
	}
	for _, key := range []string{"NSBluetoothAlwaysUsageDescription", "NSCameraUsageDescription", "NSFaceIDUsageDescription", "NSLocationWhenInUseUsageDescription", "NSPhotoLibraryUsageDescription"} {
		if !strings.Contains(out, "<key>"+key+"</key>") {
			t.Errorf("%s missing: App Store refuses a binary that links the API without it, even when no module asks", key)
		}
	}
}

func TestInitIOSWritesTheAppDelegateAndAPluginListThatCompilesWithNoNativeService(t *testing.T) {
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"go.mod":      "module example.com/demo\n\ngo 1.26\n",
		"scorix.yaml": "name: example.com/demo\napp:\n  name: demo\n  identifier: com.example.demo\n  version: 0.1.0\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := &MobileResult{}
	if err := iosInit(context.Background(), MobileOptions{Dir: dir, Platform: "ios"}, res); err != nil {
		t.Fatal(err)
	}
	app := readGenerated(t, dir, "ios/App/AppDelegate.swift")
	if !strings.HasPrefix(app, scaffoldHeader+"\n\n") || !strings.Contains(app, "Scorix.shared.start(plugins: ScorixPlugins.create())") {
		t.Errorf("AppDelegate.swift:\n%s", app)
	}
	plugins := readGenerated(t, dir, "ios/App/Scorix/ScorixPlugins.swift")
	if !strings.Contains(plugins, "enum ScorixPlugins") || !strings.Contains(plugins, "static func create() -> [ScorixPlugin]") {
		t.Errorf("ScorixPlugins.swift:\n%s", plugins)
	}
	if ig := readGenerated(t, dir, "ios/.gitignore"); !strings.Contains(ig, ".scorix/") {
		t.Errorf(".gitignore = %q", ig)
	}
	if err := iosInit(context.Background(), MobileOptions{Dir: dir, Platform: "ios"}, &MobileResult{}); err == nil {
		t.Fatal("a second init overwrote a project that is the app's now")
	}
}
