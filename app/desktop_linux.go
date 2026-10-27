//go:build linux && !android

package app

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/tradalab/scorix/internal/desktopentry"
	"github.com/tradalab/scorix/logger"
)

// No AppImage gets a menu entry by itself; rewritten every run so a moved image stays reachable.
func installDesktopEntry(identifier string) {
	image, dir := os.Getenv("APPIMAGE"), os.Getenv("APPDIR")
	if image == "" || dir == "" || identifier == "" {
		return
	}
	data := xdgDataHome()
	if data == "" {
		return
	}
	if err := writeDesktopEntry(identifier, image, dir, data); err != nil {
		logger.Warn("app: cannot add the AppImage to the app menu", "err", err)
	}
}

func writeDesktopEntry(identifier, image, appDir, dataHome string) error {
	// One .desktop at the root, whatever its name.
	found, _ := filepath.Glob(filepath.Join(appDir, "*.desktop"))
	if len(found) == 0 {
		return nil
	}
	src, err := os.ReadFile(found[0])
	if err != nil {
		return err
	}
	keys := map[string]string{"StartupWMClass": identifier, "TryExec": image}
	if icon, err := os.ReadFile(filepath.Join(appDir, ".DirIcon")); err == nil {
		path := filepath.Join(dataHome, "icons", identifier+".png")
		if _, err := writeIfChanged(path, icon); err != nil {
			return err
		}
		keys["Icon"] = path
	}
	entry := desktopentry.Rewrite(src, desktopentry.Quote(image), keys)
	apps := filepath.Join(dataHome, "applications")
	changed, err := writeIfChanged(filepath.Join(apps, identifier+".desktop"), entry)
	if err != nil {
		return err
	}
	// Scheme handlers resolve through the cache, not the .desktop files.
	if changed && desktopentry.Get(entry, "MimeType") != "" {
		if tool, err := exec.LookPath("update-desktop-database"); err == nil {
			_ = exec.Command(tool, apps).Run()
		}
	}
	return nil
}

func writeIfChanged(path string, data []byte) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, data, 0o644)
}

func xdgDataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}
