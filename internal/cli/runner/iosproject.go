package runner

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/tradalab/scorix/internal/cli/template"
)

const (
	iosMinVersion = "16.0"
	iosLibName    = "libscorixapp.a"
)

var iosUsage = [][2]string{
	{"NSBluetoothAlwaysUsageDescription", "Connect to your Bluetooth device."},
	{"NSCameraUsageDescription", "Scan QR codes with the camera."},
	{"NSFaceIDUsageDescription", "Unlock with Face ID."},
	{"NSLocationWhenInUseUsageDescription", "Use your location while the app is open."},
	{"NSPhotoLibraryUsageDescription", "Read a QR code from a picture you choose."},
}

type iosPlist struct {
	BundleID   string
	Name       string
	Version    string
	Build      string
	Schemes    []string
	Usage      [][2]string
	AppGroup   string
	Simulator  bool
	MinVersion string
}

func iosProject(opt MobileOptions) (*BuildContext, string, error) {
	bc, err := resolveBuildContext(opt.Dir, "ios", "arm64")
	if err != nil {
		return nil, "", err
	}
	return bc, filepath.Join(bc.Root, "ios"), nil
}

func iosInit(ctx context.Context, opt MobileOptions, res *MobileResult) error {
	bc, dir, err := iosProject(opt)
	if err != nil {
		return err
	}
	res.Project, res.AppID = dir, bc.Identifier
	if _, err := os.Stat(dir); err == nil {
		return exitErrorf(ExitFailed, "exists", "%s already exists and belongs to the app now; delete it to start over", dir)
	}
	fmt.Printf("==> Creating iOS project in %s\n", dir)
	if err := writeTemplateFS("static/mobile/ios", dir, map[string]any{"Name": bc.AppName}); err != nil {
		return err
	}
	return writeIOSPlugins(bc.Root)
}

func writeIOSPlugins(root string) error {
	path := filepath.Join(root, "ios", "App", "Scorix", "ScorixPlugins.swift")
	if fileExists(path) {
		return nil
	}
	s, err := renderGeneratedFile(generatedFile{Path: path, Template: mustRead(template.IOSPlugins), Data: swiftFile{}, Force: true})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, s.Content, 0o644)
}

func iosSync(ctx context.Context, opt MobileOptions, res *MobileResult) (*BuildContext, error) {
	bc, dir, err := iosProject(opt)
	if err != nil {
		return nil, err
	}
	res.Project = dir
	if !fileExists(filepath.Join(dir, "App")) {
		return nil, exitErrorf(ExitFailed, "not_initialized", "no iOS project in %s - run `scorix mobile init ios` first", dir)
	}
	modDir, err := scorixModuleDir(ctx, bc.Root)
	if err != nil {
		return nil, err
	}
	host := filepath.Join(modDir, "mobile", "ios", "ScorixHost")
	if !fileExists(filepath.Join(host, "Package.swift")) {
		return nil, fmt.Errorf("the scorix in go.mod (%s) has no iOS host library - it needs a version with iOS support", modDir)
	}
	gen := filepath.Join(dir, ".scorix")
	hostDst := filepath.Join(gen, "host")
	fmt.Printf("==> Copying host library %s -> %s\n", host, hostDst)
	if err := os.RemoveAll(hostDst); err != nil {
		return nil, err
	}
	if err := copySourceTree(host, hostDst); err != nil {
		return nil, err
	}
	if err := writeIOSPlugins(bc.Root); err != nil {
		return nil, err
	}
	mods, err := registeredModules(bc.Root)
	if err != nil {
		return nil, err
	}
	p := iosPlistFor(bc, mods, opt.Simulator)
	for name, body := range map[string]string{
		"Info.plist":            renderIOSPlist(p),
		"PrivacyInfo.xcprivacy": iosPrivacyManifest,
		"App.entitlements":      renderIOSEntitlements(p),
	} {
		if err := os.WriteFile(filepath.Join(gen, name), []byte(body), 0o644); err != nil {
			return nil, err
		}
	}
	if opt.SkipFrontend {
		fmt.Println("==> Skipping frontend build (using existing .scorix/dist)")
	} else if err := buildFrontend(ctx, bc); err != nil {
		return nil, err
	}
	if err := ensureDist(bc); err != nil {
		return nil, err
	}
	if runtime.GOOS != "darwin" {
		fmt.Println("==> Skipping the Go library: Go for iOS links only against the iOS SDK, which ships with Xcode on macOS")
		return bc, nil
	}
	out := filepath.Join(gen, iosLibName)
	if err := goBuildIOS(ctx, bc, opt.Simulator, out); err != nil {
		return nil, err
	}
	res.Libraries = append(res.Libraries, out)
	return bc, nil
}

func iosPlistFor(bc *BuildContext, modules []string, simulator bool) iosPlist {
	p := iosPlist{
		BundleID:   bc.Identifier,
		Name:       bc.ProductName,
		Version:    bc.Version,
		Build:      strconv.Itoa(androidVersionCode(bc.Version)),
		Schemes:    append([]string(nil), bc.Protocols...),
		Simulator:  simulator,
		MinVersion: iosMinVersion,
	}
	if p.Name == "" {
		p.Name = bc.AppName
	}
	p.Usage = append(p.Usage, iosUsage...)
	for _, m := range modules {
		if m == "widget" {
			p.AppGroup = "group." + bc.Identifier
		}
	}
	return p
}

func plistEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func renderIOSPlist(p iosPlist) string {
	var b strings.Builder
	kv := func(k, v string) { fmt.Fprintf(&b, "  <key>%s</key><string>%s</string>\n", k, plistEscape(v)) }
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	kv("CFBundleIdentifier", p.BundleID)
	kv("CFBundleExecutable", "App")
	kv("CFBundleName", p.Name)
	kv("CFBundleDisplayName", p.Name)
	kv("CFBundlePackageType", "APPL")
	kv("CFBundleShortVersionString", p.Version)
	kv("CFBundleVersion", p.Build)
	kv("MinimumOSVersion", p.MinVersion)
	platform := "iPhoneOS"
	if p.Simulator {
		platform = "iPhoneSimulator"
	}
	fmt.Fprintf(&b, "  <key>CFBundleSupportedPlatforms</key><array><string>%s</string></array>\n", platform)
	b.WriteString("  <key>LSRequiresIPhoneOS</key><true/>\n")
	b.WriteString("  <key>UIDeviceFamily</key><array><integer>1</integer></array>\n")
	b.WriteString("  <key>UILaunchScreen</key><dict/>\n")
	b.WriteString("  <key>ITSAppUsesNonExemptEncryption</key><false/>\n")
	if len(p.Schemes) > 0 {
		b.WriteString("  <key>CFBundleURLTypes</key><array><dict><key>CFBundleURLSchemes</key><array>")
		for _, s := range p.Schemes {
			fmt.Fprintf(&b, "<string>%s</string>", plistEscape(s))
		}
		b.WriteString("</array></dict></array>\n")
	}
	for _, u := range p.Usage {
		kv(u[0], u[1])
	}
	if p.AppGroup != "" {
		kv("ScorixAppGroup", p.AppGroup)
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func renderIOSEntitlements(p iosPlist) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	if p.AppGroup != "" {
		fmt.Fprintf(&b, "  <key>com.apple.security.application-groups</key><array><string>%s</string></array>\n", plistEscape(p.AppGroup))
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

const iosPrivacyManifest = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>NSPrivacyTracking</key><false/>
  <key>NSPrivacyTrackingDomains</key><array/>
  <key>NSPrivacyCollectedDataTypes</key><array/>
  <key>NSPrivacyAccessedAPITypes</key>
  <array>
    <dict>
      <key>NSPrivacyAccessedAPIType</key><string>NSPrivacyAccessedAPICategoryFileTimestamp</string>
      <key>NSPrivacyAccessedAPITypeReasons</key><array><string>C617.1</string></array>
    </dict>
    <dict>
      <key>NSPrivacyAccessedAPIType</key><string>NSPrivacyAccessedAPICategoryUserDefaults</string>
      <key>NSPrivacyAccessedAPITypeReasons</key><array><string>CA92.1</string></array>
    </dict>
  </array>
</dict>
</plist>
`

func iosSDK(simulator bool) string {
	if simulator {
		return "iphonesimulator"
	}
	return "iphoneos"
}

func iosTarget(simulator bool) string {
	if simulator {
		return "arm64-apple-ios" + iosMinVersion + "-simulator"
	}
	return "arm64-apple-ios" + iosMinVersion
}

func xcrun(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "xcrun", args...).Output()
	if err != nil {
		return "", fmt.Errorf("xcrun %s: %w (install Xcode and run xcode-select)", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func goBuildIOS(ctx context.Context, bc *BuildContext, simulator bool, out string) error {
	sdk, err := xcrun(ctx, "--sdk", iosSDK(simulator), "--show-sdk-path")
	if err != nil {
		return err
	}
	cc, err := xcrun(ctx, "--sdk", iosSDK(simulator), "-f", "clang")
	if err != nil {
		return err
	}
	flags := "-isysroot " + sdk + " -target " + iosTarget(simulator)
	ld := append([]string{"-s", "-w"}, bc.Ldflags...)
	args := []string{"build", "-buildmode=c-archive", "-trimpath", "-ldflags", strings.Join(ld, " ")}
	if len(bc.Tags) > 0 {
		args = append(args, "-tags", strings.Join(bc.Tags, ","))
	}
	args = append(args, "-o", out, ".")
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = bc.Root
	cmd.Env = append(os.Environ(), "GOOS=ios", "GOARCH=arm64", "CGO_ENABLED=1", "CC="+cc, "CGO_CFLAGS="+flags, "CGO_LDFLAGS="+flags)
	cmd.Stdout = os.Stdout
	var stderr strings.Builder
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	fmt.Printf("==> GOOS=ios GOARCH=arm64 go %s\n", strings.Join(args, " "))
	if err := cmd.Run(); err != nil {
		return toolchainError(err, stderr.String())
	}
	return os.Remove(strings.TrimSuffix(out, ".a") + ".h")
}

func iosBuild(ctx context.Context, opt MobileOptions, res *MobileResult) error {
	if runtime.GOOS != "darwin" {
		return exitErrorf(ExitFailed, "needs_macos", "building for iOS needs macOS with Xcode; run it in CI on macos-latest")
	}
	bc, err := iosSync(ctx, opt, res)
	if err != nil {
		return err
	}
	dir := res.Project
	gen := filepath.Join(dir, ".scorix")
	work := filepath.Join(dir, "build")
	if err := os.RemoveAll(work); err != nil {
		return err
	}
	appDir := filepath.Join(work, "App.app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return err
	}
	sdk, err := xcrun(ctx, "--sdk", iosSDK(opt.Simulator), "--show-sdk-path")
	if err != nil {
		return err
	}
	target := iosTarget(opt.Simulator)
	host := filepath.Join(gen, "host", "Sources")
	goInclude := filepath.Join(host, "ScorixGo", "include")
	hostSwift, err := filepath.Glob(filepath.Join(host, "ScorixHost", "*.swift"))
	if err != nil {
		return err
	}
	swiftc := func(args ...string) error {
		full := append([]string{"--sdk", iosSDK(opt.Simulator), "swiftc", "-target", target, "-sdk", sdk, "-O"}, args...)
		cmd := exec.CommandContext(ctx, "xcrun", full...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		fmt.Printf("==> swiftc %s\n", strings.Join(args[:min(len(args), 4)], " "))
		return cmd.Run()
	}
	if err := swiftc(append([]string{"-parse-as-library", "-emit-library", "-static", "-emit-module", "-module-name", "ScorixHost",
		"-I", goInclude, "-emit-module-path", filepath.Join(work, "ScorixHost.swiftmodule"), "-o", filepath.Join(work, "libScorixHost.a")}, hostSwift...)...); err != nil {
		return fmt.Errorf("ScorixHost: %w", err)
	}
	var appSwift []string
	err = filepath.WalkDir(filepath.Join(dir, "App"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".swift") {
			appSwift = append(appSwift, p)
		}
		return err
	})
	if err != nil {
		return err
	}
	link := []string{"-I", work, "-I", goInclude, "-L", work, "-lScorixHost", filepath.Join(gen, iosLibName),
		"-framework", "UIKit", "-framework", "WebKit", "-framework", "Security", "-framework", "CoreFoundation", "-lresolv",
		"-o", filepath.Join(appDir, "App")}
	if err := swiftc(append(append([]string{"-parse-as-library"}, link...), appSwift...)...); err != nil {
		return fmt.Errorf("app: %w", err)
	}
	for _, f := range []string{"Info.plist", "PrivacyInfo.xcprivacy"} {
		if err := copyFile(filepath.Join(gen, f), filepath.Join(appDir, f)); err != nil {
			return err
		}
	}
	if err := iosSign(ctx, appDir, filepath.Join(gen, "App.entitlements"), opt.Simulator); err != nil {
		return err
	}
	name := fmt.Sprintf("%s-%s-ios", bc.AppName, bc.Version)
	if opt.Simulator {
		dst := filepath.Join(bc.ArtifactDir, name+"-simulator.app")
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
		if err := copySourceTree(appDir, dst); err != nil {
			return err
		}
		res.Output = dst
		fmt.Printf("==> Built %s\n", dst)
		return nil
	}
	ipa := filepath.Join(bc.ArtifactDir, name+".ipa")
	if err := zipIPA(ctx, work, ipa); err != nil {
		return err
	}
	res.Output = ipa
	fmt.Printf("==> Built %s\n", ipa)
	return nil
}

func iosSign(ctx context.Context, appDir, entitlements string, simulator bool) error {
	identity := os.Getenv("SCORIX_IOS_SIGN_IDENTITY")
	profile := os.Getenv("SCORIX_IOS_PROFILE")
	signed := false
	if simulator || identity == "" {
		identity = "-"
	} else {
		signed = true
		if profile == "" {
			return exitErrorf(ExitFailed, "no_profile", "SCORIX_IOS_SIGN_IDENTITY is set but SCORIX_IOS_PROFILE (the .mobileprovision) is not")
		}
		if err := copyFile(profile, filepath.Join(appDir, "embedded.mobileprovision")); err != nil {
			return err
		}
	}
	args := []string{"--force", "--sign", identity}
	if signed {
		args = append(args, "--entitlements", entitlements)
	}
	cmd := exec.CommandContext(ctx, "codesign", append(args, appDir)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("codesign: %w", err)
	}
	if !signed && !simulator {
		fmt.Println("warning: the app is signed ad hoc and installs on no iPhone - set SCORIX_IOS_SIGN_IDENTITY and SCORIX_IOS_PROFILE to sign it")
	}
	return nil
}

func zipIPA(ctx context.Context, work, ipa string) error {
	payload := filepath.Join(work, "Payload")
	if err := os.RemoveAll(payload); err != nil {
		return err
	}
	if err := os.MkdirAll(payload, 0o755); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(work, "App.app"), filepath.Join(payload, "App.app")); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(ipa), 0o755); err != nil {
		return err
	}
	if err := os.Remove(ipa); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	cmd := exec.CommandContext(ctx, "ditto", "-c", "-k", "--sequesterRsrc", "--keepParent", payload, ipa)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
