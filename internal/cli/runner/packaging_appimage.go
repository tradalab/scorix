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
	if bc.Format == "deb" {
		return buildDeb(bc)
	}
	if bc.Format != "" && bc.Format != "appimage" {
		return "", fmt.Errorf("linux: unsupported format %q (appimage or deb)", bc.Format)
	}
	tool, err := exec.LookPath("linuxdeploy")
	if err != nil {
		return "", fmt.Errorf("linuxdeploy not found in PATH - install it from https://github.com/linuxdeploy/linuxdeploy, then verify with `scorix doctor`")
	}

	binary := filepath.Join(bc.TempDir, bc.BinaryName) // built by the caller

	src, err := linuxDesktopSource(bc)
	if err != nil {
		return "", err
	}
	srcIcon, err := linuxIcon(bc)
	if err != nil {
		return "", err
	}
	// linuxdeploy requires the icon named as Icon=.
	id := desktopID(bc)
	desktop := filepath.Join(bc.TempDir, id+".desktop")
	if err := os.WriteFile(desktop, linuxDesktopEntry(bc, id, src, ""), 0o644); err != nil {
		return "", err
	}
	icon := filepath.Join(bc.TempDir, id+".png")
	if err := copyFile(srcIcon, icon); err != nil {
		return "", err
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

	// Clear stale *.AppImage so we can reliably identify linuxdeploy's output.
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
