package runner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestBuildDeb(t *testing.T) {
	root := t.TempDir()
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "MyApp"), []byte("ELFELFELF"), 0o755); err != nil {
		t.Fatal(err)
	}
	var icon bytes.Buffer
	if err := png.Encode(&icon, image.NewRGBA(image.Rect(0, 0, 256, 256))); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "installer", "linux"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "installer", "linux", "app.png"), icon.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	bc := &BuildContext{
		Root: root, TempDir: tempDir, BinaryName: "MyApp", ArtifactDir: filepath.Join(root, "artifacts"),
		ProductName: "MyApp", Version: "1.2.3", Arch: "amd64", Protocols: []string{"myapp"},
		Manufacturer: "TradaLab", Description: "Demo app", Identifier: "com.tradalab.myapp",
	}
	out, err := buildDeb(bc)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(out) != "myapp_1.2.3_amd64.deb" {
		t.Fatalf("artifact name = %s", out)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	members := parseAr(t, raw)
	if len(members) != 3 || members[0].name != "debian-binary" ||
		members[1].name != "control.tar.gz" || members[2].name != "data.tar.gz" {
		t.Fatalf("ar members = %+v", members)
	}
	if string(members[0].body) != "2.0\n" {
		t.Fatalf("debian-binary = %q", members[0].body)
	}

	control := readTarGz(t, members[1].body)["./control"]
	for _, want := range []string{"Package: myapp", "Version: 1.2.3", "Architecture: amd64", "Maintainer: TradaLab", "Description: Demo app", "Depends: libgtk-3-0, libwebkit2gtk-4.1-0 | libwebkit2gtk-4.0-37"} {
		if !strings.Contains(string(control), want) {
			t.Fatalf("control missing %q:\n%s", want, control)
		}
	}

	data := readTarGz(t, members[2].body)
	if string(data["./usr/bin/myapp"]) != "ELFELFELF" {
		t.Fatal("binary payload missing or wrong")
	}
	if !bytes.Equal(data["./usr/share/icons/hicolor/256x256/apps/com.tradalab.myapp.png"], icon.Bytes()) {
		t.Fatal("icon missing from the hicolor size it has")
	}
	desktop := string(data["./usr/share/applications/com.tradalab.myapp.desktop"])
	for _, want := range []string{"Exec=/usr/bin/myapp %u", "Icon=com.tradalab.myapp", "StartupWMClass=com.tradalab.myapp", "MimeType=x-scheme-handler/myapp;"} {
		if !strings.Contains(desktop, want) {
			t.Fatalf("desktop entry missing %q:\n%s", want, desktop)
		}
	}
}

type arMember struct {
	name string
	body []byte
}

func parseAr(t *testing.T, raw []byte) []arMember {
	t.Helper()
	if !bytes.HasPrefix(raw, []byte("!<arch>\n")) {
		t.Fatal("not an ar archive")
	}
	rest := raw[8:]
	var out []arMember
	for len(rest) >= 60 {
		hdr := rest[:60]
		if hdr[58] != '`' || hdr[59] != '\n' {
			t.Fatalf("bad ar header terminator: %q", hdr)
		}
		name := strings.TrimSpace(string(hdr[0:16]))
		size, err := strconv.Atoi(strings.TrimSpace(string(hdr[48:58])))
		if err != nil {
			t.Fatalf("bad size in header %q", hdr)
		}
		body := rest[60 : 60+size]
		out = append(out, arMember{name: name, body: body})
		adv := 60 + size
		if size%2 == 1 {
			adv++
		}
		rest = rest[adv:]
	}
	return out
}

func readTarGz(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		b, _ := io.ReadAll(tr)
		out[hdr.Name] = b
	}
	return out
}
