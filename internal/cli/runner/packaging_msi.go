package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tradalab/scorix/internal/cli/template"
)

// windowsPackager builds a Windows MSI with the WiX Toolset (v6).
type windowsPackager struct{}

func (windowsPackager) Package(ctx context.Context, bc *BuildContext) (string, error) {
	if bc.Format != "" && bc.Format != "msi" {
		return "", fmt.Errorf("windows: unsupported format %q (only \"msi\")", bc.Format)
	}

	wixPath, err := exec.LookPath("wix")
	if err != nil {
		return "", fmt.Errorf("wix CLI not found in PATH — install WiX Toolset v6 (`dotnet tool install --global wix`), then verify with `scorix doctor`")
	}

	// WiX sources reference the icon beside the binary, so stage a copy named <ProductName>.ico.
	if bc.IconPath != "" {
		if _, err := os.Stat(bc.IconPath); err == nil {
			dst := filepath.Join(bc.TempDir, bc.ProductName+".ico")
			if err := copyFile(bc.IconPath, dst); err != nil {
				return "", fmt.Errorf("stage icon: %w", err)
			}
		}
	}

	// Sign the executable before it is embedded into the MSI.
	if err := signWindows(ctx, bc, filepath.Join(bc.TempDir, bc.BinaryName)); err != nil {
		return "", err
	}

	wxs := bc.windowsWxsFiles()
	if missing := firstMissing(wxs); missing != "" {
		fmt.Printf("==> WiX sources not found (%s) — scaffolding installer/windows/\n", missing)
		if err := scaffoldWindowsInstaller(bc); err != nil {
			return "", fmt.Errorf("scaffold installer: %w", err)
		}
	}

	ensureWixExtensions(ctx, wixPath)

	if err := os.MkdirAll(bc.ArtifactDir, 0o755); err != nil {
		return "", err
	}
	artifact := filepath.Join(bc.ArtifactDir, fmt.Sprintf("%s-%s-windows-%s.msi", bc.ProductName, bc.Version, bc.Arch))

	args := append([]string{"build"}, wxs...)
	args = append(args,
		"-ext", "WixToolset.UI.wixext",
		"-ext", "WixToolset.Util.wixext",
		"-d", "BinPath="+bc.TempDir,
		"-d", "Manufacturer="+bc.Manufacturer,
		"-d", "ProductName="+bc.ProductName,
		"-d", "ProductDesc="+bc.Description,
		"-d", "ProductVersion="+wixVersion(bc.Version),
		"-d", "UpgradeCode="+bc.upgradeCode(),
		"-o", artifact,
	)

	cmd := exec.CommandContext(ctx, wixPath, args...)
	cmd.Dir = bc.Root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	fmt.Printf("==> wix %s\n", strings.Join(args, " "))
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("wix build: %w", err)
	}

	// WiX leaves a .wixpdb next to the MSI.
	os.Remove(strings.TrimSuffix(artifact, ".msi") + ".wixpdb")

	if err := signWindows(ctx, bc, artifact); err != nil {
		return "", err
	}
	return artifact, nil
}

func (bc *BuildContext) windowsWxsFiles() []string {
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(bc.Root, p)
	}
	if bc.pkg.Windows != nil && len(bc.pkg.Windows.Wxs) > 0 {
		out := make([]string, 0, len(bc.pkg.Windows.Wxs))
		for _, w := range bc.pkg.Windows.Wxs {
			out = append(out, abs(w))
		}
		return out
	}
	return []string{
		filepath.Join(bc.Root, "installer", "windows", "product.wxs"),
		filepath.Join(bc.Root, "installer", "windows", "ui.wxs"),
	}
}

func scaffoldWindowsInstaller(bc *BuildContext) error {
	dest := filepath.Join(bc.Root, "installer", "windows")
	return writeTemplateFS(template.InstallerWindows, dest, scaffoldData(bc))
}

func ensureWixExtensions(ctx context.Context, wixPath string) {
	for _, ext := range []string{"WixToolset.UI.wixext", "WixToolset.Util.wixext"} {
		// Best-effort: if already present or offline, the later `wix build`
		// surfaces a clear error.
		cmd := exec.CommandContext(ctx, wixPath, "extension", "add", "-g", ext)
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	}
}

// wixVersion normalizes a semver-ish string to the numeric x.y.z MSI expects.
func wixVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return "0.0.0"
	}
	return v
}

func firstMissing(paths []string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			return p
		}
	}
	return ""
}
