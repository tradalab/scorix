//go:build windows

package updater

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func exitWith(t *testing.T, code int) error {
	t.Helper()
	return exec.Command("cmd.exe", "/c", "exit", strconv.Itoa(code)).Run()
}

// /norestart is what makes msiexec answer 3010 instead of rebooting, so this call is the
// one guaranteed to see it - and 3010 means the files are already on disk.
func TestMSIRebootCodesAreSuccess(t *testing.T) {
	for _, code := range []int{0, 3010, 1641} {
		if err := msiInstallResult(exitWith(t, code)); err != nil {
			t.Errorf("msiexec exit %d read as a failure: %v", code, err)
		}
	}
	for _, code := range []int{1602, 1603, 1618, 1620} {
		err := msiInstallResult(exitWith(t, code))
		if err == nil {
			t.Fatalf("msiexec exit %d read as success", code)
		}
		if !strings.Contains(err.Error(), strconv.Itoa(code)) {
			t.Errorf("exit %d reported as %q, which does not name the code", code, err)
		}
	}
}

// Start-Process without -PassThru exits 0 whatever it ran, so every elevated install that
// failed after the UAC prompt came back as success, with the floor already moved up.
func TestElevatedScriptCarriesTheInstallerExitCode(t *testing.T) {
	run := func(child string) error {
		return runElevatedInstall(context.Background(), t.TempDir(),
			elevatedScript("cmd.exe", "/c exit "+child, "Open"))
	}
	if err := run("1603"); err == nil || !strings.Contains(err.Error(), "1603") {
		t.Errorf("an elevated install that failed with 1603 came back as %v", err)
	}
	if err := run("3010"); err != nil {
		t.Errorf("an elevated install that succeeded pending reboot came back as %v", err)
	}
}

// A refused UAC prompt fails the launch itself, and an installer that never started must
// not come back as success. 1601 rather than PowerShell's exit 1, which names nothing.
func TestElevatedScriptFailsWhenTheInstallerCannotStart(t *testing.T) {
	script := elevatedScript(filepath.Join(t.TempDir(), "no-such.exe"), "/i x", "Open")
	err := runElevatedInstall(context.Background(), t.TempDir(), script)
	if err == nil {
		t.Fatal("an installer that never started came back as a successful install")
	}
	if !strings.Contains(err.Error(), "1601") {
		t.Errorf("an installer that never started reported %q", err)
	}
}

// The timing test below cannot pin this down: msiexec shows its modal box only sometimes,
// so a full-UI run can come back on its own and look fine. Assert on the UI level instead.
func TestInstallerAsksForNoModalUI(t *testing.T) {
	args := msiexecArgs(filepath.Join(t.TempDir(), "app.msi"))
	if !slices.Contains(args, "/passive") {
		t.Errorf("msiexec args %v can stop on a dialog nothing here can dismiss", args)
	}
}

// The half no fake can reach: a package that really installs. Gated because it changes the
// machine - SCORIX_MSI_PKG is a per-user .msi, SCORIX_MSI_PRODUCT its ProductCode. The
// uninstall is the assertion: msiexec answers 1605 for a product that was never installed.
func TestRealMsiexecInstallsAPackage(t *testing.T) {
	pkg, code := os.Getenv("SCORIX_MSI_PKG"), os.Getenv("SCORIX_MSI_PRODUCT")
	if pkg == "" || code == "" {
		t.Skip("set SCORIX_MSI_PKG and SCORIX_MSI_PRODUCT to run")
	}
	t.Cleanup(func() { _ = exec.Command("msiexec.exe", "/x", code, "/qn", "/norestart").Run() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := runInstallerWindows(ctx, pkg, false); err != nil {
		t.Fatalf("installing %s: %v", pkg, err)
	}
	if err := exec.Command("msiexec.exe", "/x", code, "/qn", "/norestart").Run(); err != nil {
		t.Fatalf("uninstall says %s was not installed: %v", code, err)
	}
	t.Logf("installed and removed %s through the production path", code)
}

// Real msiexec through the production path. It has to come back on its own: an error box
// here is modal, and FullUpdate would wait for a window nobody told the user to look for.
func TestRealMsiexecRefusesACorruptPackageWithoutBlocking(t *testing.T) {
	pkg := filepath.Join(t.TempDir(), "app.msi")
	if err := os.WriteFile(pkg, []byte("not an msi"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	err := runInstallerWindows(ctx, pkg, false)
	took := time.Since(start)

	t.Logf("real msiexec on a corrupt package: %v (after %s)", err, took.Round(time.Millisecond))
	if err == nil {
		t.Fatal("msiexec accepted a file that is not a package")
	}
	if ctx.Err() != nil {
		t.Fatal("msiexec had to be killed by the context: it was waiting on a dialog")
	}
	if !strings.Contains(err.Error(), "1620") {
		t.Errorf("error %q does not carry msiexec's 1620", err)
	}
}
