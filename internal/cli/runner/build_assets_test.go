package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDist(t *testing.T, names ...string) string {
	t.Helper()
	dist := t.TempDir()
	for _, n := range names {
		p := filepath.Join(dist, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dist
}

// An unpinned extension is served by sniffing, which is deterministic but reads
// the file rather than a declaration. Naming it at build time is what keeps the
// pin table growing on evidence instead of on somebody noticing a blank panel.
func TestUnpinnedAssetsNamesWhatTheTableDoesNotKnow(t *testing.T) {
	dist := writeDist(t,
		"index.html", "_next/app.js", "app.css", "index.txt",
		"photo.avif", "clip.mp4", "other.avif",
	)
	got := strings.Join(unpinnedAssets(dist), ", ")
	if got != ".avif (2), .mp4 (1)" {
		t.Errorf("unpinnedAssets = %q, want %q", got, ".avif (2), .mp4 (1)")
	}
}

// A shell built only from what scorix pins must stay quiet, or the warning is
// noise on every build and stops being read.
func TestUnpinnedAssetsQuietOnAPinnedShell(t *testing.T) {
	dist := writeDist(t, "index.html", "_next/app.js", "app.css", "index.txt", "f.woff2")
	if got := unpinnedAssets(dist); got != nil {
		t.Errorf("warned about a fully pinned shell: %v", got)
	}
}

// ensureDist drops a .keep placeholder when there is no frontend, and an
// extensionless file can never be pinned by an extension table: warning about
// either is noise the author cannot act on.
func TestUnpinnedAssetsIgnoresWhatCannotBePinned(t *testing.T) {
	dist := writeDist(t, ".keep", "LICENSE", "sub/.gitignore")
	if got := unpinnedAssets(dist); got != nil {
		t.Errorf("warned about unpinnable entries: %v", got)
	}
}
