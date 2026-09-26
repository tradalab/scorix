package llamacpp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tradalab/scorix/llm/internal/install/installtest"
	"github.com/tradalab/scorix/llm/model"
	"github.com/tradalab/scorix/llm/probe"
)

// Serves a release as GitHub does: the listing with digests, then the assets.
func fakeReleases(t *testing.T, assets map[string][]byte) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/ggml-org/llama.cpp/releases/tags/b9934", "/repos/ggml-org/llama.cpp/releases/latest":
			var list []map[string]any
			for name, b := range assets {
				s := sha256.Sum256(b)
				list = append(list, map[string]any{"name": name, "size": len(b), "digest": "sha256:" + hex.EncodeToString(s[:]), "browser_download_url": srv.URL + "/dl/" + name})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "b9934", "assets": list})
		default:
			name := strings.TrimPrefix(r.URL.Path, "/dl/")
			b, ok := assets[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
		}
	}))
	return srv
}

// The asset this OS would get with an Intel GPU, holding a stand-in server.
func thisOSBuild(t *testing.T, withServer bool) (probe.Machine, string, []byte) {
	m := probe.Machine{OS: runtime.GOOS, Arch: runtime.GOARCH, GPUs: []probe.GPU{{Vendor: "intel"}}}
	osName, arch, ok := platform(m)
	if !ok {
		t.Skip("no llama.cpp build for this platform")
	}
	body := installtest.Entry{Name: "llama-b9934/" + serverName(), Body: "server", Mode: 0o755}
	if !withServer {
		body.Name = "llama-b9934/README.md"
	}
	switch osName {
	case "win":
		body.Name = strings.TrimPrefix(body.Name, "llama-b9934/")
		return m, "llama-b9934-bin-win-vulkan-" + arch + ".zip", installtest.Zip(t, body, installtest.Entry{Name: "ggml.dll", Body: "dll"})
	case "ubuntu":
		return m, "llama-b9934-bin-ubuntu-vulkan-" + arch + ".tar.gz", installtest.Tgz(t, body)
	default:
		return m, "llama-b9934-bin-macos-" + arch + ".tar.gz", installtest.Tgz(t, body)
	}
}

func TestEnsureInstallsOnceAndFindsItOffline(t *testing.T) {
	m, asset, data := thisOSBuild(t, true)
	srv := fakeReleases(t, map[string][]byte{asset: data, "llama-b9934-bin-win-cpu-x64.zip": installtest.Zip(t, installtest.Entry{Name: "x", Body: "x"})})
	store := &model.Store{Root: t.TempDir()}
	in := &Installer{Store: store, Releases: &model.GitHubReleases{Endpoint: srv.URL}, Tag: "b9934"}
	exe, err := in.Ensure(context.Background(), m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "server" || !strings.Contains(filepath.ToSlash(exe), "/llama.cpp/b9934/vulkan/") {
		t.Errorf("exe = %s", exe)
	}
	if left, _ := filepath.Glob(filepath.Join(store.Root, "github", "*", "*", "*", asset)); len(left) != 0 {
		t.Errorf("the archive was kept after unpacking: %v", left)
	}

	// The next start has no network; the install on disk has to be enough.
	srv.Close()
	again, err := in.Ensure(context.Background(), m, nil)
	if err != nil || again != exe {
		t.Errorf("offline: %s, %v", again, err)
	}
	// Following latest cannot be known to be installed without asking.
	if _, ok := (&Installer{Store: store}).Installed(m); ok {
		t.Error("an unpinned installer claimed an install without asking which release is latest")
	}
}

func TestABuildWithoutTheServerLeavesNoInstall(t *testing.T) {
	m, asset, data := thisOSBuild(t, false)
	srv := fakeReleases(t, map[string][]byte{asset: data})
	defer srv.Close()
	store := &model.Store{Root: t.TempDir()}
	in := &Installer{Store: store, Releases: &model.GitHubReleases{Endpoint: srv.URL}, Tag: "b9934"}
	if _, err := in.Ensure(context.Background(), m, nil); err == nil {
		t.Fatal("a build with no llama-server installed")
	}
	if dirs, _ := filepath.Glob(filepath.Join(store.Root, "llama.cpp", "b9934", "*")); len(dirs) != 0 {
		t.Errorf("left behind: %v", dirs)
	}
}

// A CUDA install is found by a glob, and the installer's own scratch names
// match it too and sort after the real one. A leftover .partial or a .old the
// installer could not remove - Windows refuses to delete a directory holding a
// file something still has open - would then be run instead of the install
// that works, every launch, and a user who reinstalled to fix a broken binary
// keeps getting the broken one.
func TestAScratchDirectoryIsNotAnInstall(t *testing.T) {
	store := &model.Store{Root: t.TempDir()}
	in := &Installer{Store: store, Tag: "b9934", Options: Options{CUDA: true}}
	m := probe.Machine{OS: "windows", Arch: "amd64", GPUs: []probe.GPU{nv("8.6", "610.78")}}
	for _, name := range []string{"cuda-12.4", "cuda-12.4.old", "cuda-12.4.partial"} {
		dir := filepath.Join(store.Root, "llama.cpp", "b9934", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, serverName()), []byte(name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	exe, ok := in.Installed(m)
	if !ok {
		t.Fatal("the install was not found at all")
	}
	if got, _ := os.ReadFile(exe); string(got) != "cuda-12.4" {
		t.Errorf("picked %s", exe)
	}
}
