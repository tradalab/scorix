package runner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"image/png"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func buildDeb(bc *BuildContext) (string, error) {
	binSrc := filepath.Join(bc.TempDir, bc.BinaryName)
	binData, err := os.ReadFile(binSrc)
	if err != nil {
		return "", fmt.Errorf("deb: read built binary: %w", err)
	}

	pkgName := debPackageName(bc.ProductName)
	arch := map[string]string{"amd64": "amd64", "arm64": "arm64", "386": "i386"}[bc.Arch]
	if arch == "" {
		arch = bc.Arch
	}
	version := orDefault(bc.Version, "0.0.0")

	src, err := linuxDesktopSource(bc)
	if err != nil {
		return "", err
	}
	iconPath, err := linuxIcon(bc)
	if err != nil {
		return "", err
	}
	iconData, err := os.ReadFile(iconPath)
	if err != nil {
		return "", err
	}
	iconDir, err := debIconDir(iconData)
	if err != nil {
		return "", fmt.Errorf("deb: %s: %w", iconPath, err)
	}
	id := desktopID(bc)
	desktop := linuxDesktopEntry(bc, id, src, "/usr/bin/"+pkgName)

	var data bytes.Buffer
	dataGz := gzip.NewWriter(&data)
	dataTar := tar.NewWriter(dataGz)
	now := time.Now()
	files := []struct {
		name string
		mode int64
		body []byte
	}{
		{"./usr/bin/" + pkgName, 0o755, binData},
		{"./usr/share/applications/" + id + ".desktop", 0o644, desktop},
		{"./" + iconDir + id + ".png", 0o644, iconData},
	}
	dirs := map[string]bool{}
	for _, f := range files {
		for d := path.Dir(f.name); d != "."; d = path.Dir(d) {
			dirs["./"+d+"/"] = true
		}
	}
	for _, dir := range slices.Sorted(maps.Keys(dirs)) {
		if err := dataTar.WriteHeader(&tar.Header{Name: dir, Mode: 0o755, Typeflag: tar.TypeDir, ModTime: now}); err != nil {
			return "", err
		}
	}
	for _, f := range files {
		if err := writeTarFile(dataTar, f.name, f.mode, now, f.body); err != nil {
			return "", err
		}
	}
	if err := dataTar.Close(); err != nil {
		return "", err
	}
	if err := dataGz.Close(); err != nil {
		return "", err
	}

	installedKB := (len(binData) + len(desktop) + len(iconData) + 1023) / 1024
	control := fmt.Sprintf(`Package: %s
Version: %s
Section: utils
Priority: optional
Architecture: %s
Maintainer: %s
Installed-Size: %d
Depends: %s
Description: %s
`, pkgName, version, arch, orDefault(bc.Manufacturer, "unknown"), installedKB, debDepends, orDefault(bc.Description, bc.ProductName))

	var ctrl bytes.Buffer
	ctrlGz := gzip.NewWriter(&ctrl)
	ctrlTar := tar.NewWriter(ctrlGz)
	if err := writeTarFile(ctrlTar, "./control", 0o644, now, []byte(control)); err != nil {
		return "", err
	}
	if err := ctrlTar.Close(); err != nil {
		return "", err
	}
	if err := ctrlGz.Close(); err != nil {
		return "", err
	}

	if err := os.MkdirAll(bc.ArtifactDir, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(bc.ArtifactDir, fmt.Sprintf("%s_%s_%s.deb", pkgName, version, arch))
	f, err := os.Create(out)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := f.WriteString("!<arch>\n"); err != nil {
		return "", err
	}
	for _, m := range []struct {
		name string
		body []byte
	}{
		{"debian-binary", []byte("2.0\n")},
		{"control.tar.gz", ctrl.Bytes()},
		{"data.tar.gz", data.Bytes()},
	} {
		if err := writeArMember(f, m.name, m.body, now); err != nil {
			return "", err
		}
	}
	return out, nil
}

// The webview is dlopen'd, so dpkg cannot infer these.
const debDepends = "libgtk-3-0, libwebkit2gtk-4.1-0 | libwebkit2gtk-4.0-37"

func debIconDir(icon []byte) (string, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(icon))
	if err != nil {
		return "", err
	}
	if cfg.Width == cfg.Height && slices.Contains([]int{16, 22, 24, 32, 48, 64, 128, 256, 512}, cfg.Width) {
		return fmt.Sprintf("usr/share/icons/hicolor/%dx%d/apps/", cfg.Width, cfg.Height), nil
	}
	return "usr/share/pixmaps/", nil
}

func writeTarFile(tw *tar.Writer, name string, mode int64, mod time.Time, body []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), ModTime: mod}); err != nil {
		return err
	}
	_, err := tw.Write(body)
	return err
}

func writeArMember(f *os.File, name string, body []byte, mod time.Time) error {
	hdr := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8s%-10d`\n",
		name, mod.Unix(), 0, 0, "100644", len(body))
	if _, err := f.WriteString(hdr); err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		return err
	}
	if len(body)%2 == 1 {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	return nil
}

func debPackageName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.', r == '+':
			b.WriteRune(r)
		case r == ' ', r == '_':
			b.WriteRune('-')
		}
	}
	if b.Len() < 2 {
		return "scorix-app"
	}
	return b.String()
}
