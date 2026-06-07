package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/tradalab/scorix/internal/cli/template"
)

// darwinPackager assembles a macOS .app bundle and (by default) a .dmg.
type darwinPackager struct{}

func (darwinPackager) Package(ctx context.Context, bc *BuildContext) (string, error) {
	if bc.Format != "" && bc.Format != "dmg" && bc.Format != "app" {
		return "", fmt.Errorf("darwin: unsupported format %q (use \"dmg\" or \"app\")", bc.Format)
	}

	binary := filepath.Join(bc.TempDir, bc.BinaryName) // built by the caller (handles universal)

	if err := os.MkdirAll(bc.ArtifactDir, 0o755); err != nil {
		return "", err
	}
	appBundle := filepath.Join(bc.ArtifactDir, bc.ProductName+".app")
	if err := os.RemoveAll(appBundle); err != nil {
		return "", err
	}
	macosDir := filepath.Join(appBundle, "Contents", "MacOS")
	resDir := filepath.Join(appBundle, "Contents", "Resources")
	if err := os.MkdirAll(macosDir, 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(resDir, 0o755); err != nil {
		return "", err
	}

	exe := filepath.Join(macosDir, bc.ProductName)
	if err := copyFile(binary, exe); err != nil {
		return "", err
	}
	_ = os.Chmod(exe, 0o755)

	if icns := firstExisting(
		filepath.Join(bc.Root, "installer", "mac", bc.ProductName+".icns"),
		filepath.Join(bc.Root, "installer", "mac", "AppIcon.icns"),
	); icns != "" {
		if err := copyFile(icns, filepath.Join(resDir, "AppIcon.icns")); err != nil {
			return "", err
		}
	}

	// Info.plist: use the app's if present, else scaffold. Patch the version so
	// the bundle's version tracks etc/app.yaml (single source of truth).
	plistSrc := filepath.Join(bc.Root, "installer", "mac", "Info.plist")
	if _, err := os.Stat(plistSrc); err != nil {
		fmt.Println("==> Info.plist not found — scaffolding installer/mac/")
		if err := scaffoldDarwinInstaller(bc); err != nil {
			return "", fmt.Errorf("scaffold installer: %w", err)
		}
	}
	plistData, err := os.ReadFile(plistSrc)
	if err != nil {
		return "", err
	}
	plistData = patchPlistVersion(plistData, bc.Version)
	if err := os.WriteFile(filepath.Join(appBundle, "Contents", "Info.plist"), plistData, 0o644); err != nil {
		return "", err
	}

	// Code-sign the assembled bundle (deep) before wrapping it.
	if err := codesignMac(ctx, bc, appBundle, true); err != nil {
		return "", err
	}

	if bc.Format == "app" {
		fmt.Printf("==> Built app bundle %s\n", appBundle)
		return appBundle, nil
	}

	dmg := filepath.Join(bc.ArtifactDir, fmt.Sprintf("%s-%s-macos-%s.dmg", bc.ProductName, bc.Version, bc.Arch))
	_ = os.Remove(dmg)

	created := false
	if hdiutil, err := exec.LookPath("hdiutil"); err == nil {
		cmd := exec.CommandContext(ctx, hdiutil, "create",
			"-volname", bc.ProductName,
			"-srcfolder", appBundle,
			"-ov", "-format", "UDZO",
			dmg)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		fmt.Printf("==> hdiutil create -volname %s -srcfolder %s -ov -format UDZO %s\n", bc.ProductName, appBundle, dmg)
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("hdiutil: %w", err)
		}
		created = true
	} else if dmgbuild, err := exec.LookPath("dmgbuild"); err == nil {
		settings := filepath.Join(bc.Root, ".scorix", "dmg_settings.py")
		content := fmt.Sprintf("volume_name = %q\nfiles = [%q]\nsymlinks = {'Applications': '/Applications'}\n", bc.ProductName, appBundle)
		if err := os.WriteFile(settings, []byte(content), 0o644); err != nil {
			return "", err
		}
		cmd := exec.CommandContext(ctx, dmgbuild, "-s", settings, bc.ProductName, dmg)
		cmd.Dir = bc.Root
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		fmt.Printf("==> dmgbuild -s %s %s %s\n", settings, bc.ProductName, dmg)
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("dmgbuild: %w", err)
		}
		created = true
	}
	if !created {
		return "", fmt.Errorf("no DMG tool found (need hdiutil on macOS, or dmgbuild). The app bundle is ready at %s — re-run with --target app to keep just the bundle", appBundle)
	}

	// Sign the disk image, then notarize + staple it (no-ops if not configured).
	if err := codesignMac(ctx, bc, dmg, false); err != nil {
		return "", err
	}
	if err := notarizeMac(ctx, bc, dmg); err != nil {
		return "", err
	}
	return dmg, nil
}

func scaffoldDarwinInstaller(bc *BuildContext) error {
	dest := filepath.Join(bc.Root, "installer", "mac")
	return writeTemplateFS(template.InstallerMac, dest, scaffoldData(bc))
}

// patchPlistVersion best-effort rewrites CFBundleVersion / CFBundleShortVersionString
// to version. No-op for keys that aren't present.
func patchPlistVersion(content []byte, version string) []byte {
	for _, key := range []string{"CFBundleShortVersionString", "CFBundleVersion"} {
		content = setPlistString(content, key, version)
	}
	return content
}

func setPlistString(content []byte, key, value string) []byte {
	re := regexp.MustCompile(`<key>` + regexp.QuoteMeta(key) + `</key>\s*<string>[^<]*</string>`)
	repl := "<key>" + key + "</key>\n  <string>" + value + "</string>"
	return re.ReplaceAllLiteral(content, []byte(repl))
}
