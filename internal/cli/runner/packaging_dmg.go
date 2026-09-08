package runner

import (
	"bytes"
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

	// Linux refuses to package without an icon and Windows prints a warning;
	// macOS used to say nothing at all, which is how every .dmg in the house
	// shipped a bundle wearing the generic application icon while the Windows
	// build wore the real mark. A warning rather than an error: a bundle with
	// no icon still runs, and failing here would break pipelines that never
	// had one.
	icns := firstExisting(
		filepath.Join(bc.Root, "installer", "mac", bc.ProductName+".icns"),
		filepath.Join(bc.Root, "installer", "mac", "AppIcon.icns"),
	)
	if icns == "" {
		fmt.Printf("warning: no macOS icon - provide installer/mac/%s.icns; the bundle falls back to the generic application icon (.ico is a Windows resource and is not converted)\n", bc.ProductName)
	} else if err := copyFile(icns, filepath.Join(resDir, "AppIcon.icns")); err != nil {
		return "", err
	}

	// Use the app's Info.plist if present, else scaffold; patch version to track scorix.yaml.
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
	// Before anything is written into it: a plist macOS cannot parse produces a
	// bundle that will not launch, and every step after this one - codesign,
	// hdiutil, notarize - succeeds on it regardless.
	if err := checkPlist(plistData, plistSrc); err != nil {
		return "", err
	}

	plistData = patchPlistVersion(plistData, bc.Version)
	// An icon in Resources that no key points at is invisible: Finder reads
	// CFBundleIconFile, not the directory. A plist that predates the scaffold
	// carrying that key would otherwise ship the icon AND the generic look,
	// with nothing to say why.
	if icns != "" {
		plistData = ensurePlistString(plistData, "CFBundleIconFile", "AppIcon")
	}
	// The identity the manifest owns, enforced rather than trusted - the same
	// class of bug as an MSI carrying a sibling's UpgradeCode, on the platform
	// where nothing else would notice.
	plistData, notes := applyPlistIdentity(plistData, bc.Identifier)
	for _, n := range notes {
		fmt.Println(n)
	}
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
		content = ensurePlistString(content, key, version)
	}
	return content
}

// ensurePlistString sets key, ADDING it when the plist does not carry it.
//
// Replacing only what is already there looks harmless and is not: a plist
// hand-copied before a key existed in the scaffold never gains it, so bundles
// shipped with no CFBundleShortVersionString at all - no marketing version
// anywhere in Finder - and nothing in the build said so.
func ensurePlistString(content []byte, key, value string) []byte {
	entry := "<key>" + key + "</key>\n  <string>" + value + "</string>"

	re := regexp.MustCompile(`<key>` + regexp.QuoteMeta(key) + `</key>\s*<string>[^<]*</string>`)
	if re.Match(content) {
		return re.ReplaceAllLiteral(content, []byte(entry))
	}

	// Before the closing </dict> of the root dictionary, which is the last one.
	closing := bytes.LastIndex(content, []byte("</dict>"))
	if closing < 0 {
		return content
	}
	insert := []byte("  " + entry + "\n")
	out := make([]byte, 0, len(content)+len(insert))
	out = append(out, content[:closing]...)
	out = append(out, insert...)
	return append(out, content[closing:]...)
}
