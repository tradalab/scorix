package runner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
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

	exe := "/usr/bin/" + pkgName
	desktop := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=%s
Comment=%s
Exec=%s %%u
Terminal=false
Categories=Utility;
`, bc.ProductName, orDefault(bc.Description, bc.ProductName), exe)

	var data bytes.Buffer
	dataGz := gzip.NewWriter(&data)
	dataTar := tar.NewWriter(dataGz)
	now := time.Now()
	writeTar := func(name string, mode int64, body []byte) error {
		return writeTarFile(dataTar, name, mode, now, body)
	}
	for _, dir := range []string{"./usr/", "./usr/bin/", "./usr/share/", "./usr/share/applications/"} {
		if err := dataTar.WriteHeader(&tar.Header{Name: dir, Mode: 0o755, Typeflag: tar.TypeDir, ModTime: now}); err != nil {
			return "", err
		}
	}
	if err := writeTar("./usr/bin/"+pkgName, 0o755, binData); err != nil {
		return "", err
	}
	if err := writeTar("./usr/share/applications/"+pkgName+".desktop", 0o644, []byte(desktop)); err != nil {
		return "", err
	}
	if err := dataTar.Close(); err != nil {
		return "", err
	}
	if err := dataGz.Close(); err != nil {
		return "", err
	}

	installedKB := (len(binData) + 1023) / 1024
	control := fmt.Sprintf(`Package: %s
Version: %s
Section: utils
Priority: optional
Architecture: %s
Maintainer: %s
Installed-Size: %d
Description: %s
`, pkgName, version, arch, orDefault(bc.Manufacturer, "unknown"), installedKB, orDefault(bc.Description, bc.ProductName))

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

	outDir := filepath.Join(bc.Root, "dist")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(outDir, fmt.Sprintf("%s_%s_%s.deb", pkgName, version, arch))
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
