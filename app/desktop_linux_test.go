//go:build linux && !android

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppImageEntryPointsAtTheImage(t *testing.T) {
	appDir, data := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(appDir, "S3Hub.desktop"), []byte("[Desktop Entry]\nName=S3Hub\nExec=S3Hub %u\nIcon=S3Hub\nMimeType=x-scheme-handler/s3hub;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, ".DirIcon"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	image := "/home/u/My Apps/S3Hub.AppImage"
	if err := writeDesktopEntry("com.tradalab.s3hub", image, appDir, data); err != nil {
		t.Fatal(err)
	}
	entry, err := os.ReadFile(filepath.Join(data, "applications", "com.tradalab.s3hub.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	icon := filepath.Join(data, "icons", "com.tradalab.s3hub.png")
	for _, want := range []string{`Exec="` + image + `" %u`, "TryExec=" + image, "Icon=" + icon, "StartupWMClass=com.tradalab.s3hub"} {
		if !strings.Contains(string(entry), want) {
			t.Fatalf("entry missing %q:\n%s", want, entry)
		}
	}
	if b, _ := os.ReadFile(icon); string(b) != "png" {
		t.Fatal("icon not copied out of the image")
	}
}

func TestAnAppStartedFromInsideAnotherAppImageAddsNoMenuEntryForIt(t *testing.T) {
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "other.desktop"), []byte("[Desktop Entry]\nName=Other IDE\nExec=other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("APPIMAGE", "/opt/OtherIDE.AppImage")
	t.Setenv("APPDIR", other)
	installDesktopEntry("com.tradalab.s3hub")
	if entries, _ := os.ReadDir(filepath.Join(data, "applications")); len(entries) != 0 {
		t.Fatalf("wrote %d menu entries pointing at an AppImage this process does not run from", len(entries))
	}
}

func TestTheMCPCommandIsThisProcessNotAnInheritedAppImage(t *testing.T) {
	t.Setenv("APPIMAGE", "/opt/OtherIDE.AppImage")
	t.Setenv("APPDIR", t.TempDir())
	got, err := mcpSelfCommand()
	exe, _ := os.Executable()
	if err != nil || got != exe {
		t.Fatalf("mcp command = %q, %v; want this executable %q", got, err, exe)
	}
}
