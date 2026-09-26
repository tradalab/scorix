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
	"time"
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

func cutAfter(n int, payload []byte, cuts *int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if *cuts > 0 {
			*cuts--
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload[:n])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		http.ServeContent(w, r, "app.msi", time.Time{}, bytes.NewReader(payload))
	}
}

// A dropped connection 90 MB into an installer used to throw the 90 MB away.
func TestABrokenDownloadResumesInsteadOfFailing(t *testing.T) {
	payload := bytes.Repeat([]byte("installer"), 200_000)
	cuts := 1
	srv := httptest.NewServer(cutAfter(700_000, payload, &cuts))
	defer srv.Close()
	// .exe, not .msi: .msi is what a lost extension falls back to on Windows,
	// so it could not show the extension being kept.
	path, err := (&UpdaterModule{}).Download(context.Background(), defaultClient(), srv.URL+"/app.exe")
	if err != nil {
		t.Fatalf("one dropped connection failed the update: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(path)) })
	if got, _ := os.ReadFile(path); !bytes.Equal(got, payload) {
		t.Errorf("got %d bytes, want the %d-byte installer", len(got), len(payload))
	}
	if filepath.Ext(path) != ".exe" {
		t.Errorf("installer extension lost: %s", path)
	}
}

func TestAFailedDownloadLeavesNoTempDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	t.Setenv("TMPDIR", tmp)
	cuts := 100
	srv := httptest.NewServer(cutAfter(1000, bytes.Repeat([]byte("x"), 5000), &cuts))
	defer srv.Close()
	if _, err := (&UpdaterModule{}).Download(context.Background(), defaultClient(), srv.URL+"/app.msi"); err == nil {
		t.Fatal("a download that never completed succeeded")
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "scorix-update-*")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
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
