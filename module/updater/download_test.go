package updater

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// One line per 32 KiB chunk is 3,200 lines for a 100 MB installer and ten times that
// for an AppImage, into a rotating log file.
func TestDownloadDoesNotFloodTheLog(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 4<<20)
	for _, tc := range []struct {
		name       string
		contentLen bool
		wantAtMost int
	}{
		{"content-length known", true, 60},
		{"chunked, size unknown", false, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.contentLen {
					w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
				}
				_, _ = w.Write(payload)
			}))
			defer srv.Close()

			var lines []string
			old := progressLog
			progressLog = func(msg string) { lines = append(lines, msg) }
			t.Cleanup(func() { progressLog = old })

			m := &UpdaterModule{}
			path, err := m.Download(context.Background(), defaultClient(), srv.URL+"/app.msi")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(path)) })

			t.Logf("4 MiB wrote %d progress lines", len(lines))
			if len(lines) > tc.wantAtMost {
				t.Errorf("%d progress lines for 4 MiB, want at most %d", len(lines), tc.wantAtMost)
			}
			if len(lines) == 0 || !strings.Contains(lines[len(lines)-1], "completed") {
				t.Errorf("the last line is not the completion: %v", lines)
			}
			if fi, err := os.Stat(path); err != nil || fi.Size() != int64(len(payload)) {
				t.Errorf("downloaded file = %v (%v), want %d bytes", fi, err, len(payload))
			}
		})
	}
}

// A path that names a file this call already deleted is a path the UI can show and
// nothing can open.
func TestFullUpdateLeavesNoPathToADeletedFile(t *testing.T) {
	pubB64, _ := genKey(t)
	_, wrongPriv := genKey(t)
	artifact := []byte("installer bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(artifact)
	}))
	defer srv.Close()

	m := &UpdaterModule{
		cfg:     Config{PublicKeyBase64: pubB64, CurrentVersion: "v1.0.0"},
		dataDir: t.TempDir(),
	}
	m.provider = &fixedProvider{res: &Result{
		HasUpdate:    true,
		NewVersion:   "v2.0.0",
		ArtifactURLs: []string{srv.URL + "/app.msi"},
		SigBase64:    base64.StdEncoding.EncodeToString(ed25519.Sign(wrongPriv, artifact)),
	}}

	res, err := m.FullUpdate(context.Background())
	if err == nil {
		t.Fatal("a signature from the wrong key installed")
	}
	if res.LocalPath != "" {
		if _, statErr := os.Stat(res.LocalPath); statErr != nil {
			t.Errorf("result still names %q, which this call deleted", res.LocalPath)
		}
	}
}
