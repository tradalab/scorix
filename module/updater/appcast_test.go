package updater

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAdvertisedIn(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		version string
		want    bool
	}{
		{"packager filename", "https://cdn/pchub/PCHub-0.2.0-windows-amd64.msi", "0.2.0", true},
		{"query string ignored", "https://cdn/pchub/PCHub-0.2.0-windows-amd64.msi?x=1", "0.2.0", true},
		{"backslash separator", `https://cdn\pchub\PCHub-0.2.0-windows-amd64.msi`, "0.2.0", true},

		{"high version, old artifact", "https://cdn/pchub/PCHub-0.1.0-windows-amd64.msi", "99.0.0", false},
		{"version only in the path", "https://cdn/pchub/0.2.0/installer.msi", "0.2.0", false},

		{"empty version", "https://cdn/pchub/PCHub-0.2.0-windows-amd64.msi", "", false},
		{"empty url", "", "0.2.0", false},
		{"whitespace version", "https://cdn/pchub/PCHub-0.2.0-windows-amd64.msi", "   ", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := advertisedIn(c.url, c.version); got != c.want {
				t.Fatalf("advertisedIn(%q, %q) = %v, want %v", c.url, c.version, got, c.want)
			}
		})
	}
}

func serveManifest(t *testing.T, v any) *httptest.Server {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sig") {
			t.Errorf("provider fetched %s; the signature lives inside the manifest now", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAppcastNeedsNoDetachedSignature(t *testing.T) {
	srv := serveManifest(t, StaticAppcast{
		Version: "0.2.0",
		Platforms: map[string]PlatformArtifact{
			"windows-amd64": {URLs: []string{"https://cdn/pchub/PCHub-0.2.0-windows-amd64.msi"}, SignatureBase64: "sig"},
		},
	})

	res, err := NewAppcastProvider(srv.URL).CheckForUpdate(context.Background(), "0.1.0", "windows-amd64")
	if err != nil {
		t.Fatal(err)
	}
	if res.NewVersion != "0.2.0" || res.SigBase64 != "sig" || len(res.ArtifactURLs) != 1 {
		t.Fatalf("got %+v", res)
	}
}

func TestAppcastRefusesVersionArtifactMismatch(t *testing.T) {
	srv := serveManifest(t, StaticAppcast{
		Version: "99.0.0",
		Platforms: map[string]PlatformArtifact{
			"windows-amd64": {URLs: []string{"https://cdn/pchub/PCHub-0.1.0-windows-amd64.msi"}, SignatureBase64: "sig"},
		},
	})

	_, err := NewAppcastProvider(srv.URL).CheckForUpdate(context.Background(), "0.1.0", "windows-amd64")
	if err == nil {
		t.Fatal("a manifest offering 99.0.0 while pointing at the 0.1.0 artifact was accepted")
	}
	if !strings.Contains(err.Error(), "99.0.0") {
		t.Fatalf("error does not say what was offered: %v", err)
	}
}

func TestAppcastNoEndpoint(t *testing.T) {
	_, err := NewAppcastProvider("").CheckForUpdate(context.Background(), "0.1.0", "windows-amd64")
	if err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("got %v, want a missing-endpoint error", err)
	}
}

func TestAllAdvertise(t *testing.T) {
	good := []string{
		"https://cdn-03.tradalab.com/pchub/0.2.0/PCHub-0.2.0-windows-amd64.msi",
		"https://pub-abc.r2.dev/pchub/0.2.0/PCHub-0.2.0-windows-amd64.msi",
	}
	if _, ok := allAdvertise(good, "0.2.0"); !ok {
		t.Fatal("two honest mirrors were refused")
	}

	mixed := []string{good[0], "https://pub-abc.r2.dev/pchub/0.1.0/PCHub-0.1.0-windows-amd64.msi"}
	bad, ok := allAdvertise(mixed, "0.2.0")
	if ok {
		t.Fatal("a mirror pointing at the old artifact was accepted")
	}
	if bad != mixed[1] {
		t.Fatalf("named %q as the offender, want %q", bad, mixed[1])
	}

	if _, ok := allAdvertise(nil, "0.2.0"); ok {
		t.Fatal("a manifest naming no host was accepted")
	}
}

func TestAppcastCarriesEveryHost(t *testing.T) {
	urls := []string{
		"https://cdn-03.tradalab.com/pchub/0.2.0/PCHub-0.2.0-windows-amd64.msi",
		"https://pub-abc.r2.dev/pchub/0.2.0/PCHub-0.2.0-windows-amd64.msi",
	}
	srv := serveManifest(t, StaticAppcast{
		Version:   "0.2.0",
		Platforms: map[string]PlatformArtifact{"windows-amd64": {URLs: urls, SignatureBase64: "sig"}},
	})

	res, err := NewAppcastProvider(srv.URL).CheckForUpdate(context.Background(), "0.1.0", "windows-amd64")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ArtifactURLs) != 2 || res.ArtifactURLs[1] != urls[1] {
		t.Fatalf("got %v, want both hosts in order", res.ArtifactURLs)
	}
}

func TestDownloadAnyFallsThrough(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(dead.Close)
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("installer bytes"))
	}))
	t.Cleanup(live.Close)

	m := New()
	m.dataDir = t.TempDir()

	path, err := m.downloadAny(context.Background(), []string{dead.URL + "/a.msi", live.URL + "/a.msi"})
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "installer bytes" {
		t.Fatalf("read %q err=%v", b, err)
	}
}

func TestDownloadAnyReportsEveryHost(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(dead.Close)

	m := New()
	m.dataDir = t.TempDir()

	_, err := m.downloadAny(context.Background(), []string{dead.URL + "/a.msi", dead.URL + "/b.msi"})
	if err == nil {
		t.Fatal("every host failed but the download reported success")
	}
	for _, want := range []string{"/a.msi", "/b.msi"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestDownloadAnyRefusesEmptyList(t *testing.T) {
	m := New()
	m.dataDir = t.TempDir()
	if _, err := m.downloadAny(context.Background(), nil); err == nil {
		t.Fatal("an empty url list was accepted")
	}
}
