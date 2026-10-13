package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func installRig(t *testing.T) (*cliRig, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the installer owns PATH on Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(t.TempDir(), "Probe.AppImage")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APPIMAGE", target)
	return newCLIRig(t), filepath.Join(home, ".local", "bin", "probe"), target
}

func TestCLIInstallLinksAndUninstallRemoves(t *testing.T) {
	r, link, target := installRig(t)
	t.Setenv("PATH", filepath.Dir(link))
	if code := r.run(false, "", "cli", "install"); code != ExitOK {
		t.Fatalf("install: exit %d: %s", code, r.errs.String())
	}
	if dest, err := os.Readlink(link); err != nil || dest != target {
		t.Fatalf("link = %q, %v; want %s", dest, err, target)
	}
	r.run(false, "", "cli", "status", "--json")
	if !strings.Contains(r.out.String(), `"installed":true`) || !strings.Contains(r.out.String(), `"on_path":true`) {
		t.Errorf("status: %s", r.out.String())
	}
	if code := r.run(false, "", "cli", "uninstall"); code != ExitOK {
		t.Fatalf("uninstall: exit %d", code)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("link still there: %v", err)
	}
}

func TestCLIInstallLeavesOtherPeoplesFilesAlone(t *testing.T) {
	r, link, _ := installRig(t)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code := r.run(false, "", "cli", "install"); code == ExitOK {
		t.Error("replaced a real file of the same name")
	}
	if b, _ := os.ReadFile(link); string(b) != "mine" {
		t.Error("the user's file changed")
	}

	os.Remove(link)
	other := filepath.Join(t.TempDir(), "Other.AppImage")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if code := r.run(false, "", "cli", "uninstall"); code == ExitOK {
		t.Error("removed a link that belongs to another install")
	}
	if dest, _ := os.Readlink(link); dest != other {
		t.Error("the other install's link changed")
	}
}

func TestCLIStatusSaysWhenTheDirIsNotOnPath(t *testing.T) {
	r, _, _ := installRig(t)
	t.Setenv("PATH", "/usr/bin")
	r.run(false, "", "cli", "install")
	r.run(false, "", "cli", "status")
	if !strings.Contains(r.out.String(), "export PATH=") {
		t.Errorf("no PATH hint:\n%s", r.out.String())
	}
}

// What a .deb leaves.
func TestCLIStatusCountsAPackagedBinary(t *testing.T) {
	r, link, _ := installRig(t)
	bin := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APPIMAGE", bin)
	t.Setenv("PATH", filepath.Dir(bin))
	r.run(false, "", "cli", "status", "--json")
	if !strings.Contains(r.out.String(), `"installed":true`) {
		t.Errorf("a packaged binary on PATH reads as not installed: %s", r.out.String())
	}
	if code := r.run(false, "", "cli", "install"); code != ExitOK {
		t.Fatalf("install: exit %d", code)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Error("install added a second command next to the packaged one")
	}
}

func TestCLIInstallLeavesAnotherToolsLinkAlone(t *testing.T) {
	r, link, _ := installRig(t)
	other := filepath.Join(t.TempDir(), "code")
	if err := os.WriteFile(other, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if code := r.run(false, "", "cli", "install"); code == ExitOK {
		t.Error("replaced a link to another program")
	}
	if dest, _ := os.Readlink(link); dest != other {
		t.Errorf("link now points at %s", dest)
	}
}

func TestCLIUninstallNeverRemovesAPackagedBinary(t *testing.T) {
	r, _, _ := installRig(t)
	bin := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APPIMAGE", bin)
	t.Setenv("PATH", filepath.Dir(bin))
	if code := r.run(false, "", "cli", "uninstall"); code == ExitOK {
		t.Error("uninstall reported success on the package's binary")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("the packaged binary was removed: %v", err)
	}
}
