package install

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/tradalab/scorix/llm/internal/install/installtest"
)

type entry = installtest.Entry

func unpackBytes(t *testing.T, name string, data []byte) (string, error) {
	dir := t.TempDir()
	a := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(a, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, Unpack(a, filepath.Join(dir, "install"))
}

// A release asset is someone else's input: a name or link that reaches out of
// the install directory would let the archive write anywhere.
func TestNothingInAnArchiveLeavesTheInstall(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"slip.zip", installtest.Zip(t, entry{Name: "../evil.dll", Body: "x"})},
		{"slip.tar.gz", installtest.Tgz(t, entry{Name: "llama/../../evil", Body: "x", Mode: 0o644})},
		{"abs.tar.gz", installtest.Tgz(t, entry{Name: "/etc/evil", Body: "x", Mode: 0o644})},
		{"link.tar.gz", installtest.Tgz(t, entry{Name: "llama/lib.so", Link: "../../outside"})},
		{"link.zip", installtest.Zip(t, entry{Name: "llama/lib.so", Link: "../../outside"})},
		// Not absolute to filepath on Windows, yet a link to it lands on the
		// current drive.
		{"abslink.tar.gz", installtest.Tgz(t, entry{Name: "llama/lib.so", Link: "/etc/passwd"})},
	} {
		dir, err := unpackBytes(t, tc.name, tc.data)
		if err == nil {
			t.Errorf("%s was unpacked", tc.name)
		}
		for _, p := range []string{filepath.Join(dir, "evil.dll"), filepath.Join(dir, "evil"), filepath.Join(dir, "outside")} {
			if _, err := os.Lstat(p); err == nil {
				t.Errorf("%s: %s was written outside the install", tc.name, p)
			}
		}
	}
}

// Both shapes, because a link that lands as a plain file holding the target
// text leaves a .so nothing can load, and unzip grew its link branch after
// untar had one.
func TestAnArchiveKeepsItsExecutableAndLinks(t *testing.T) {
	// Not "skip on Windows": a Windows box with Developer Mode on can make a
	// symlink, and skipping there left the branch covered nowhere at all.
	if !canSymlink(t) {
		t.Skip("symlinks need a privilege this account does not have")
	}
	es := []entry{
		{Name: "llama-b1/", Dir: true},
		{Name: "llama-b1/llama-server", Body: "#!", Mode: 0o755},
		{Name: "llama-b1/libx.so.0.1", Body: "so", Mode: 0o644},
		{Name: "llama-b1/libx.so", Link: "libx.so.0.1"},
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"b.tar.gz", installtest.Tgz(t, es...)},
		{"b.zip", installtest.Zip(t, es...)},
	} {
		t.Run(tc.name, func(t *testing.T) { keepsLinks(t, tc.name, tc.data) })
	}
}

func keepsLinks(t *testing.T, name string, data []byte) {
	t.Helper()
	dir, err := unpackBytes(t, name, data)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no executable bit to keep; the link half is what both shapes
	// share, and it is the half that was covered nowhere.
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, "install", "llama-b1", "llama-server"))
		if err != nil || fi.Mode().Perm()&0o100 == 0 {
			t.Errorf("llama-server lost its executable bit: %v %v", fi, err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(dir, "install", "llama-b1", "libx.so")); err != nil || string(got) != "so" {
		t.Errorf("the library link does not resolve: %q %v", got, err)
	}
}

// A release asset from an older tag arrives with no sha256 of its own - the
// hub only started attaching them in mid-2025 - so the zip's own CRC is the
// only thing standing between a flipped bit on the wire and a binary that
// crashes at run time with nothing pointing at the download. archive/zip
// checks it when the entry's reader reaches the end, which a copy that stops
// at the declared size never does, and a stored entry is never compressed so
// nothing else notices either.
func TestAZipEntryThatDoesNotMatchItsChecksumIsRefused(t *testing.T) {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	h := &zip.FileHeader{Name: "llama-b1/" + exeName, Method: zip.Store}
	h.SetMode(0o755)
	f, err := w.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw := b.Bytes()
	i := bytes.Index(raw, []byte(payload))
	if i < 0 {
		t.Fatal("the stored bytes are not in the archive")
	}
	raw[i] ^= 0xff

	dir, err := unpackBytes(t, "flipped.zip", raw)
	if err == nil {
		t.Error("an entry that does not match its checksum was unpacked")
	}
	if _, err := os.Stat(filepath.Join(dir, "install", "llama-b1", exeName)); err == nil {
		t.Error("the damaged file was left in the install")
	}
}

const (
	exeName = "llama-server"
	payload = "PAYLOAD-BYTES-0123456789"
)

func canSymlink(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	return os.Symlink("target", filepath.Join(dir, "link")) == nil
}
