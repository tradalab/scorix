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

// linuxPackager builds a Linux AppImage with linuxdeploy.
type linuxPackager struct{}

func (linuxPackager) Package(ctx context.Context, bc *BuildContext) (string, error) {
	if bc.Format != "" && bc.Format != "appimage" {
		return "", fmt.Errorf("linux: unsupported format %q (only \"appimage\")", bc.Format)
	}
	tool, err := exec.LookPath("linuxdeploy")
	if err != nil {
		return "", fmt.Errorf("linuxdeploy not found in PATH — install it from https://github.com/linuxdeploy/linuxdeploy, then verify with `scorix doctor`")
	}

	binary := filepath.Join(bc.TempDir, bc.BinaryName) // built by the caller

	desktop := firstExisting(
		filepath.Join(bc.Root, "installer", "linux", bc.ProductName+".desktop"),
		filepath.Join(bc.Root, "installer", "linux", "app.desktop"),
	)
	if desktop == "" {
		fmt.Println("==> Desktop entry not found — scaffolding installer/linux/")
		if err := scaffoldLinuxInstaller(bc); err != nil {
			return "", fmt.Errorf("scaffold installer: %w", err)
		}
		desktop = filepath.Join(bc.Root, "installer", "linux", "app.desktop")
	}

	icon := firstExisting(
		filepath.Join(bc.Root, "installer", "linux", bc.ProductName+".png"),
		filepath.Join(bc.Root, "installer", "linux", "app.png"),
	)
	if icon == "" && bc.IconPath != "" && strings.EqualFold(filepath.Ext(bc.IconPath), ".png") {
		icon = bc.IconPath
	}
	if icon == "" {
		return "", fmt.Errorf("linux icon not found — provide installer/linux/%s.png (PNG; .ico is not accepted by AppImage)", bc.ProductName)
	}

	appDir := filepath.Join(bc.Root, ".scorix", "AppDir")
	if err := os.RemoveAll(appDir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(bc.ArtifactDir, 0o755); err != nil {
		return "", err
	}

	// linuxdeploy writes <Name>-<arch>.AppImage into the working dir. Clear any
	// stale ones so we can reliably identify the output.
	for _, s := range globAppImages(bc.Root) {
		_ = os.Remove(s)
	}

	args := []string{
		"--appimage-extract-and-run",
		"--appdir", appDir,
		"--executable", binary,
		"--desktop-file", desktop,
		"--icon-file", icon,
		"--output", "appimage",
	}
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Dir = bc.Root
	cmd.Env = append(os.Environ(), "VERSION="+bc.Version)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	fmt.Printf("==> linuxdeploy %s\n", strings.Join(args, " "))
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("linuxdeploy: %w", err)
	}

	produced := globAppImages(bc.Root)
	if len(produced) == 0 {
		return "", fmt.Errorf("linuxdeploy completed but produced no .AppImage")
	}
	artifact := filepath.Join(bc.ArtifactDir, fmt.Sprintf("%s-%s-linux-%s.AppImage", bc.ProductName, bc.Version, bc.Arch))
	if err := os.Rename(produced[0], artifact); err != nil {
		return "", err
	}

	// Optional GPG detached signature (<artifact>.sig).
	if err := signLinux(ctx, bc, artifact); err != nil {
		return "", err
	}
	return artifact, nil
}

func globAppImages(dir string) []string {
	m, _ := filepath.Glob(filepath.Join(dir, "*.AppImage"))
	return m
}

func scaffoldLinuxInstaller(bc *BuildContext) error {
	dest := filepath.Join(bc.Root, "installer", "linux")
	return writeTemplateFS(template.InstallerLinux, dest, scaffoldData(bc))
}
