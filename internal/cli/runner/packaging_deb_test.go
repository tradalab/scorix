package runner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	bc := &BuildContext{
		Root: root, TempDir: tempDir, BinaryName: "MyApp",
		ProductName: "My App", Version: "1.2.3", Arch: "amd64",
		Manufacturer: "TradaLab", Description: "Demo app", Identifier: "com.tradalab.myapp",
	}
	out, err := buildDeb(bc)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(out) != "my-app_1.2.3_amd64.deb" {
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
	for _, want := range []string{"Package: my-app", "Version: 1.2.3", "Architecture: amd64", "Maintainer: TradaLab", "Description: Demo app"} {
		if !strings.Contains(string(control), want) {
			t.Fatalf("control missing %q:\n%s", want, control)
		}
	}

	data := readTarGz(t, members[2].body)
	if string(data["./usr/bin/my-app"]) != "ELFELFELF" {
		t.Fatal("binary payload missing or wrong")
	}
	desktop := string(data["./usr/share/applications/my-app.desktop"])
	if !strings.Contains(desktop, "Exec=/usr/bin/my-app %u") || !strings.Contains(desktop, "Name=My App") {
		t.Fatalf("desktop entry:\n%s", desktop)
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
