package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tradalab/scorix/fetch"
)

func files(paths ...string) []File {
	out := make([]File, len(paths))
	for i, p := range paths {
		out[i] = File{Path: p, Size: int64(100 * (i + 1))}
	}
	return out
}

// File names from real repos: ggml-org/SmolVLM-256M-Instruct-GGUF and the split
// tinyllamas in ggml-org/models-moved, plus the naming other publishers use.
func TestVariantsGroupWhatMakesOneModel(t *testing.T) {
	snap := &Snapshot{Files: files(
		".gitattributes",
		"README.md",
		"SmolVLM-256M-Instruct-Q8_0.gguf",
		"SmolVLM-256M-Instruct-f16.gguf",
		"mmproj-SmolVLM-256M-Instruct-Q8_0.gguf",
		"mmproj-SmolVLM-256M-Instruct-f16.gguf",
		"tinyllamas/split/stories15M-q8_0-00002-of-00003.gguf",
		"tinyllamas/split/stories15M-q8_0-00001-of-00003.gguf",
		"tinyllamas/split/stories15M-q8_0-00003-of-00003.gguf",
		"tinyllamas/split/stories15M-00001-of-00003.gguf",
		"tinyllamas/split/stories15M-00003-of-00003.gguf",
		"Llama-3.2-1B-Instruct-IQ4_XS.gguf",
		"gemma-3-4b-it-UD-Q4_K_XL.gguf",
		"gpt-oss-20b-mxfp4.gguf",
	)}
	got := map[string]Variant{}
	for _, v := range Variants(snap) {
		if _, dup := got[v.Key]; dup {
			t.Errorf("key %s is not unique", v.Key)
		}
		got[v.Key] = v
	}
	names := func(fs []File) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Path)
		}
		return out
	}
	smol := got["SmolVLM-256M-Instruct-Q8_0"]
	if smol.Name != "Q8_0" || !smol.Complete || len(smol.Files) != 1 {
		t.Errorf("SmolVLM Q8_0 = %+v", smol)
	}
	if smol.Projector == nil || smol.Projector.Path != "mmproj-SmolVLM-256M-Instruct-f16.gguf" {
		t.Errorf("projector = %+v", smol.Projector)
	}
	if got["SmolVLM-256M-Instruct-f16"].Name != "F16" {
		t.Errorf("f16 named %q", got["SmolVLM-256M-Instruct-f16"].Name)
	}

	// Shards in order, whatever order the hub listed them in, and under the same
	// quant name as another model's single file without the two merging.
	split := got["tinyllamas/split/stories15M-q8_0"]
	want := []string{"tinyllamas/split/stories15M-q8_0-00001-of-00003.gguf", "tinyllamas/split/stories15M-q8_0-00002-of-00003.gguf", "tinyllamas/split/stories15M-q8_0-00003-of-00003.gguf"}
	if !split.Complete || split.Name != "Q8_0" || !reflect.DeepEqual(names(split.Files), want) || split.Size != 800+700+900 {
		t.Errorf("split q8_0 = %+v", split)
	}
	// SmolVLM's projector would load next to a text model and garble it.
	if split.Projector != nil {
		t.Errorf("another model's projector was attached: %s", split.Projector.Path)
	}
	if v := got["tinyllamas/split/stories15M"]; v.Complete || len(v.Files) != 2 {
		t.Errorf("a split missing shard 2 = %+v", v)
	}
	for key, name := range map[string]string{"Llama-3.2-1B-Instruct-IQ4_XS": "IQ4_XS", "gemma-3-4b-it-UD-Q4_K_XL": "Q4_K_XL", "gpt-oss-20b-mxfp4": "gpt-oss-20b-mxfp4"} {
		if got[key].Name != name {
			t.Errorf("%s named %q, want %q", key, got[key].Name, name)
		}
	}
	for _, v := range got {
		if strings.Contains(v.Key, "mmproj") || strings.HasSuffix(v.Key, ".md") {
			t.Errorf("%s became a variant", v.Key)
		}
	}
	if all := smol.All(); len(all) != 2 || all[1].Path != smol.Projector.Path {
		t.Errorf("All = %v", names(all))
	}
}

// unsloth keeps each quant in its own folder and one unnamed projector at the
// top, which fits all of them.
func TestAnUnnamedProjectorAboveFitsEveryQuant(t *testing.T) {
	snap := &Snapshot{Files: files("Q4_K_M/model-Q4_K_M-00001-of-00002.gguf", "Q4_K_M/model-Q4_K_M-00002-of-00002.gguf", "mmproj-F16.gguf", "other/mmproj-F16.gguf")}
	vs := Variants(snap)
	if len(vs) != 1 || vs[0].Projector == nil || vs[0].Projector.Path != "mmproj-F16.gguf" {
		t.Errorf("variants = %+v", vs)
	}
	// One in a sibling directory belongs to whatever lives there.
	sibling := &Snapshot{Files: files("Q4_K_M/model-Q4_K_M.gguf", "other/mmproj-F16.gguf")}
	if p := Variants(sibling)[0].Projector; p != nil {
		t.Errorf("a sibling directory's projector was attached: %s", p.Path)
	}
}

func TestSplitName(t *testing.T) {
	stem, i, n, ok := SplitName("dir/stories15M-q8_0-00002-of-00003.gguf")
	if !ok || stem != "stories15M-q8_0" || i != 2 || n != 3 {
		t.Errorf("= %q %d %d %v", stem, i, n, ok)
	}
	// An app hands it a path on disk, which on Windows is backslashed.
	if stem, _, _, ok := SplitName(`C:\models\m-00001-of-00002.gguf`); !ok || stem != "m" {
		t.Errorf("a Windows path: stem %q", stem)
	}
	for _, not := range []string{"model-Q4_K_M.gguf", "m-1-of-3.gguf", "m-00001-of-00003.bin"} {
		if _, _, _, ok := SplitName(not); ok {
			t.Errorf("%s read as a shard", not)
		}
	}
}

// ggml-org/gemma-3-4b-it-GGUF as listed on 2026-09-19: "mmproj-model-f16"
// names no model, so it belongs to whichever one is asked for.
func TestAProjectorNamingNoModelIsTheRepos(t *testing.T) {
	snap := &Snapshot{Files: files("gemma-3-4b-it-Q4_K_M.gguf", "gemma-3-4b-it-Q8_0.gguf", "gemma-3-4b-it-f16.gguf", "mmproj-model-f16.gguf")}
	for _, v := range Variants(snap) {
		if v.Projector == nil || v.Projector.Path != "mmproj-model-f16.gguf" {
			t.Errorf("%s: projector = %+v", v.Key, v.Projector)
		}
	}
}

// The bf16 name is the shorter one, so the length tie-break cannot hide a
// ranking that treats the two as equal.
func TestProjectorPrefersF16OverBF16(t *testing.T) {
	snap := &Snapshot{Files: files("model-Q4_K_M.gguf", "mmproj-BF16.gguf", "mmproj-model-F16.gguf", "mmproj-F32.gguf")}
	if p := Variants(snap)[0].Projector; p == nil || p.Path != "mmproj-model-F16.gguf" {
		t.Errorf("projector = %+v", p)
	}
}

// The names come from someone else's server.
func TestTheStoreKeepsRemoteNamesInsideIt(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	good := &Snapshot{Source: "huggingface", Repo: "owner/repo", Revision: "abc"}
	if p, err := s.Path(good, File{Path: "dir/model.gguf"}); err != nil || !strings.HasPrefix(p, s.Root) {
		t.Errorf("a normal path: %q %v", p, err)
	}
	for _, tc := range []struct {
		snap Snapshot
		file string
	}{
		{*good, "../../outside.gguf"},
		{*good, "/etc/passwd"},
		// Taken on Linux as a directory named "C:".
		{*good, "C:/Windows/evil.dll"},
		{*good, "c:evil.gguf"},
		{*good, `dir\..\..\evil.gguf`},
		{Snapshot{Source: "huggingface", Repo: "owner/repo", Revision: "sha256:abc"}, "m.gguf"},
		{Snapshot{Source: "huggingface", Repo: "../..", Revision: "abc"}, "m.gguf"},
		{Snapshot{Source: "huggingface", Repo: "owner/repo", Revision: ""}, "m.gguf"},
		{Snapshot{Source: "huggingface", Repo: "owner/repo", Revision: "../x"}, "m.gguf"},
	} {
		if p, err := s.Path(&tc.snap, File{Path: tc.file}); err == nil {
			t.Errorf("repo %q rev %q file %q -> %s", tc.snap.Repo, tc.snap.Revision, tc.file, p)
		}
	}
}

func digestOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type fakeHub struct {
	*httptest.Server
	mu      sync.Mutex
	gets    map[string]int
	auth    map[string]string
	content map[string][]byte
}

// Speaks the three calls Resolve and Open make, in Hugging Face's shapes.
func newFakeHub(t *testing.T, repo string) *fakeHub {
	h := &fakeHub{gets: map[string]int{}, auth: map[string]string{}, content: map[string][]byte{
		"model-Q4_K_M.gguf": bytes.Repeat([]byte("q"), 300_000),
		"README.md":         []byte("hello\n"),
	}}
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		h.mu.Lock()
		h.gets["cdn/"+name]++
		h.auth["cdn/"+name] = r.Header.Get("Authorization")
		h.mu.Unlock()
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(h.content[name]))
	}))
	t.Cleanup(cdn.Close)
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.auth[r.URL.Path] = r.Header.Get("Authorization")
		h.mu.Unlock()
		switch {
		case r.URL.Path == "/api/models/gated/repo/revision/main":
			w.Header().Set("X-Error-Code", "GatedRepo")
			http.Error(w, "Access restricted", http.StatusUnauthorized)
		case r.URL.Path == "/api/models/"+repo+"/revision/main":
			_, _ = fmt.Fprint(w, `{"sha":"c0ffee"}`)
		case r.URL.Path == "/api/models/"+repo+"/tree/c0ffee" && r.URL.Query().Get("cursor") == "":
			w.Header().Set("Link", fmt.Sprintf(`<%s/api/models/%s/tree/c0ffee?recursive=true&cursor=p2>; rel="next"`, h.URL, repo))
			_, _ = fmt.Fprintf(w, `[{"type":"directory","path":"sub"},{"type":"file","path":"model-Q4_K_M.gguf","size":300000,"oid":"x","lfs":{"oid":%q}}]`, digestOf(h.content["model-Q4_K_M.gguf"]))
		case r.URL.Path == "/api/models/"+repo+"/tree/c0ffee":
			_, _ = fmt.Fprint(w, `[{"type":"file","path":"README.md","size":6,"oid":"ce013625030ba8dba906f756967f9e9ca394464a"}]`)
		case strings.HasPrefix(r.URL.Path, "/"+repo+"/resolve/c0ffee/"):
			name := strings.TrimPrefix(r.URL.Path, "/"+repo+"/resolve/c0ffee/")
			if name == "README.md" {
				// Small files stay on the hub's own host, token and all.
				http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(h.content[name]))
				return
			}
			http.Redirect(w, r, cdn.URL+"/"+name, http.StatusFound)
		default:
			w.Header().Set("X-Error-Code", "RepoNotFound")
			http.Error(w, "Repository not found", http.StatusUnauthorized)
		}
	}))
	t.Cleanup(h.Server.Close)
	return h
}

func TestHuggingFacePinsListsAndDownloads(t *testing.T) {
	hub := newFakeHub(t, "owner/repo")
	hf := &HuggingFace{Endpoint: hub.URL, Token: func(context.Context) (string, error) { return "hf_secret", nil }}
	snap, err := hf.Resolve(context.Background(), "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Revision != "c0ffee" || snap.Ref != "main" || len(snap.Files) != 2 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if d := snap.Files[0].Digest; d == nil || d.Algo != fetch.SHA256 {
		t.Errorf("an LFS file got %+v", d)
	}
	if d := snap.Files[1].Digest; d == nil || d.Algo != fetch.GitBlobSHA1 {
		t.Errorf("a small file got %+v", d)
	}

	store := &Store{Root: t.TempDir()}
	var progressed bool
	paths, err := store.Get(context.Background(), hf, snap, snap.Files, func(Progress) { progressed = true })
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range paths {
		got, _ := os.ReadFile(p)
		if !bytes.Equal(got, hub.content[snap.Files[i].Path]) {
			t.Errorf("%s differs", p)
		}
		if !strings.Contains(filepath.ToSlash(p), "/huggingface/owner/repo/c0ffee/") {
			t.Errorf("stored outside its revision: %s", p)
		}
	}
	if !progressed || !store.Has(snap, snap.Files) {
		t.Errorf("progress %v, has %v", progressed, store.Has(snap, snap.Files))
	}
	if a := hub.auth["cdn/model-Q4_K_M.gguf"]; a != "" {
		t.Errorf("the token followed the redirect to the CDN: %q", a)
	}
	if a := hub.auth["/api/models/owner/repo/revision/main"]; a != "Bearer hf_secret" {
		t.Errorf("the hub's own API got %q", a)
	}
	if a := hub.auth["/owner/repo/resolve/c0ffee/README.md"]; a != "Bearer hf_secret" {
		t.Errorf("a file on the hub's own host got %q", a)
	}

	// Stored once, never fetched again.
	if _, err := store.Get(context.Background(), hf, snap, snap.Files, nil); err != nil {
		t.Fatal(err)
	}
	if n := hub.gets["cdn/model-Q4_K_M.gguf"]; n != 1 {
		t.Errorf("fetched %d times", n)
	}
}

func TestHuggingFaceSaysWhyItCannot(t *testing.T) {
	hub := newFakeHub(t, "owner/repo")
	hf := &HuggingFace{Endpoint: hub.URL}
	if _, err := hf.Resolve(context.Background(), "gated/repo"); !errors.Is(err, ErrGated) || !strings.Contains(err.Error(), "gated/repo") {
		t.Errorf("gated: %v", err)
	}
	if _, err := hf.Resolve(context.Background(), "nobody/nothing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if _, err := hf.Resolve(context.Background(), "just-a-name"); err == nil {
		t.Error("a ref without an owner was accepted")
	}
}

func TestAURLNeedsItsDigest(t *testing.T) {
	body := []byte("gguf bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "m.gguf", time.Time{}, bytes.NewReader(body))
	}))
	defer srv.Close()
	u := &URL{}
	for _, bad := range []string{srv.URL + "/m.gguf", srv.URL + "/m.gguf#sha256=abc", "ftp://x/m.gguf#sha256=" + digestOf(body), srv.URL + "/#sha256=" + digestOf(body)} {
		if _, err := u.Resolve(context.Background(), bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	snap, err := u.Resolve(context.Background(), srv.URL+"/dir/m.gguf#sha256="+strings.ToUpper(digestOf(body)))
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: t.TempDir()}
	paths, err := store.Get(context.Background(), u, snap, snap.Files, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(paths[0]); !bytes.Equal(got, body) || filepath.Base(paths[0]) != "m.gguf" {
		t.Errorf("stored %s", paths[0])
	}

	lying, _ := u.Resolve(context.Background(), srv.URL+"/m.gguf#sha256="+digestOf([]byte("other")))
	if _, err := store.Get(context.Background(), u, lying, lying.Files, nil); !errors.Is(err, fetch.ErrDigest) {
		t.Errorf("a wrong digest: %v", err)
	}
}

func TestGitHubReleasesPinTheTagAndTheBytes(t *testing.T) {
	body := []byte("llama-server zip")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/ggml-org/llama.cpp/releases/latest", "/repos/ggml-org/llama.cpp/releases/tags/b9934":
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "b9934", "assets": []map[string]any{
				{"name": "llama-b9934-bin-win-vulkan-x64.zip", "size": len(body), "digest": "sha256:" + digestOf(body), "browser_download_url": srv.URL + "/dl/win.zip"},
				{"name": "old.zip", "size": 3, "browser_download_url": srv.URL + "/dl/old.zip"},
			}})
		case "/dl/win.zip":
			http.ServeContent(w, r, "win.zip", time.Time{}, bytes.NewReader(body))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	g := &GitHubReleases{Endpoint: srv.URL}
	for _, ref := range []string{"ggml-org/llama.cpp", "ggml-org/llama.cpp@b9934"} {
		snap, err := g.Resolve(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		if snap.Revision != "b9934" || snap.Files[0].Digest == nil || snap.Files[1].Digest != nil {
			t.Errorf("%s -> %+v", ref, snap)
		}
	}
	snap, _ := g.Resolve(context.Background(), "ggml-org/llama.cpp@b9934")
	paths, err := (&Store{Root: t.TempDir()}).Get(context.Background(), g, snap, snap.Files[:1], nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(paths[0]); !bytes.Equal(got, body) {
		t.Error("asset differs")
	}
	if _, err := g.Resolve(context.Background(), "ggml-org/llama.cpp@nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing tag: %v", err)
	}
}

func TestAFolderIsUsedWhereItIs(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "sub", "m-Q4_0.gguf"), []byte("x"), 0o644)
	snap, err := Folder{}.Resolve(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Files) != 1 || snap.Files[0].Path != "sub/m-Q4_0.gguf" || Variants(snap)[0].Name != "Q4_0" {
		t.Fatalf("snapshot = %+v", snap)
	}
	store := &Store{Root: t.TempDir()}
	paths, err := store.Get(context.Background(), Folder{}, snap, snap.Files, nil)
	if err != nil || paths[0] != filepath.Join(dir, "sub", "m-Q4_0.gguf") {
		t.Errorf("paths = %v, %v", paths, err)
	}
	if entries, _ := os.ReadDir(store.Root); len(entries) != 0 {
		t.Error("a folder's model was copied into the store")
	}
	if _, err := (Folder{}).Resolve(context.Background(), filepath.Join(dir, "missing")); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing folder: %v", err)
	}
}

// The next page is a URL the server chose, so it never passes the repo again.
func TestTheTokenDoesNotFollowAListingOffTheHub(t *testing.T) {
	var elsewhere string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere = r.Header.Get("Authorization")
		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer other.Close()
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/revision/main"):
			_, _ = fmt.Fprint(w, `{"sha":"c0ffee"}`)
		default:
			if r.URL.Query().Get("cursor") == "" {
				w.Header().Set("Link", fmt.Sprintf(`<%s/api/models/owner/repo/tree/c0ffee?cursor=p2>; rel="next"`, other.URL))
			}
			_, _ = fmt.Fprint(w, `[]`)
		}
	}))
	defer hub.Close()
	hf := &HuggingFace{Endpoint: hub.URL, Token: func(context.Context) (string, error) { return "hf_secret", nil }}
	if _, err := hf.Resolve(context.Background(), "owner/repo"); err != nil {
		t.Fatal(err)
	}
	if elsewhere != "" {
		t.Errorf("the token reached another host: %q", elsewhere)
	}
}

// A hub that names itself in the next-page URL would otherwise get the
// token sent to whatever host it chose.
func TestAListingThatNeverEndsIsGivenUpOn(t *testing.T) {
	var pages int
	var hub *httptest.Server
	hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/revision/main") {
			_, _ = fmt.Fprint(w, `{"sha":"c0ffee"}`)
			return
		}
		pages++
		// Absolute, the way a hub writes it: a relative one would fail on the
		// next request and the loop would stop for the wrong reason.
		w.Header().Set("Link", `<`+hub.URL+r.URL.RequestURI()+`>; rel="next"`)
		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer hub.Close()
	hf := &HuggingFace{Endpoint: hub.URL}
	if _, err := hf.Resolve(context.Background(), "owner/repo"); err == nil {
		t.Fatal("a listing that never ends was followed to the end")
	}
	if pages != maxPages {
		t.Errorf("asked for %d pages, the cap is %d", pages, maxPages)
	}
}

// Accepting the terms in a browser is what to do about a gated file, and
// retrying is not - so it must not look like a failure that resumes.
func TestAGatedFileIsNotTriedAgain(t *testing.T) {
	var tries int
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/revision/main"):
			_, _ = fmt.Fprint(w, `{"sha":"c0ffee"}`)
		case strings.Contains(r.URL.Path, "/tree/"):
			_, _ = fmt.Fprint(w, `[{"type":"file","path":"model.gguf","size":4}]`)
		default:
			tries++
			w.Header().Set("X-Error-Code", "GatedRepo")
			http.Error(w, "Access restricted", http.StatusUnauthorized)
		}
	}))
	defer hub.Close()
	hf := &HuggingFace{Endpoint: hub.URL}
	snap, err := hf.Resolve(context.Background(), "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: t.TempDir()}
	_, err = store.Get(context.Background(), hf, snap, snap.Files, nil)
	if !errors.Is(err, ErrGated) {
		t.Fatalf("err = %v", err)
	}
	if tries != 1 {
		t.Errorf("asked %d times: %v", tries, err)
	}
	if strings.Contains(err.Error(), "gave up after") {
		t.Errorf("a gated repo reads as a flaky network: %v", err)
	}
}

// A download that will never work leaves nothing for the next one to
// resume from.
func TestAFailedDownloadLeavesNoPart(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/revision/main"):
			_, _ = fmt.Fprint(w, `{"sha":"c0ffee"}`)
		case strings.Contains(r.URL.Path, "/tree/"):
			_, _ = fmt.Fprint(w, `[{"type":"file","path":"model.gguf","size":4}]`)
		default:
			w.Header().Set("X-Error-Code", "EntryNotFound")
			http.Error(w, "not there", http.StatusNotFound)
		}
	}))
	defer hub.Close()
	hf := &HuggingFace{Endpoint: hub.URL}
	snap, err := hf.Resolve(context.Background(), "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	store := &Store{Root: root}
	if _, err := store.Get(context.Background(), hf, snap, snap.Files, nil); err == nil {
		t.Fatal("a missing file downloaded")
	}
	var parts []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".part") {
			parts = append(parts, p)
		}
		return nil
	})
	if len(parts) != 0 {
		t.Errorf("left behind %v", parts)
	}
}

// A part nobody comes back for is the store growing by every model the user
// failed to download.
func TestAPartNobodyCameBackForIsSwept(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "huggingface", "owner", "repo", "c0ffee", "gone.gguf.part")
	fresh := filepath.Join(root, "huggingface", "owner", "repo", "c0ffee", "resuming.gguf.part")
	keep := filepath.Join(root, "huggingface", "owner", "repo", "c0ffee", "model.gguf")
	for _, p := range []string{old, fresh, keep} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	long := time.Now().Add(-partKeptFor - time.Hour)
	if err := os.Chtimes(old, long, long); err != nil {
		t.Fatal(err)
	}

	hub := newFakeHub(t, "owner/repo")
	hf := &HuggingFace{Endpoint: hub.URL}
	snap, err := hf.Resolve(context.Background(), "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	if _, err := store.Get(context.Background(), hf, snap, snap.Files, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the abandoned part is still there: %v", err)
	}
	for _, p := range []string{fresh, keep} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed: %v", filepath.Base(p), err)
		}
	}
}

// A part being written is not abandoned however old it looks. No writer
// here on purpose: Windows refuses to remove an open file, so a test that
// opened it would pass whatever the sweep decided.
func TestTheSweepLeavesAPartThatIsBeingWritten(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "huggingface", "owner", "repo", "c0ffee", "resuming.gguf"+fetch.PartSuffix)
	dead := filepath.Join(root, "huggingface", "owner", "repo", "c0ffee", "gone.gguf"+fetch.PartSuffix)
	long := time.Now().Add(-partKeptFor - time.Hour)
	for _, p := range []string{live, dead} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, long, long); err != nil {
			t.Fatal(err)
		}
	}

	release := hold(live)
	(&Store{Root: root}).sweepParts()
	if _, err := os.Stat(live); err != nil {
		t.Errorf("the part being written was swept: %v", err)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("the abandoned part is still there: %v", err)
	}

	// And the hold ends with the download, or one interrupted transfer would
	// keep its part for good.
	release()
	(&Store{Root: root}).sweepParts()
	if _, err := os.Stat(live); !os.IsNotExist(err) {
		t.Errorf("still held after the download ended: %v", err)
	}
}

// The other half: Get really registers the part it is about to write.
func TestGetHoldsThePartItIsWriting(t *testing.T) {
	hub := newFakeHub(t, "owner/repo")
	hf := &HuggingFace{Endpoint: hub.URL}
	snap, err := hf.Resolve(context.Background(), "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: t.TempDir()}
	var mu sync.Mutex
	held := map[string]bool{}
	_, err = store.Get(context.Background(), hf, snap, snap.Files, func(p Progress) {
		dst, err := store.Path(snap, p.File)
		if err != nil {
			t.Error(err)
			return
		}
		liveMu.Lock()
		busy := live[dst+fetch.PartSuffix] > 0
		liveMu.Unlock()
		mu.Lock()
		held[p.File.Path] = held[p.File.Path] || busy
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range snap.Files {
		if !held[f.Path] {
			t.Errorf("%s was fetched without its part being held", f.Path)
		}
	}
	// Nothing stays registered once Get is done.
	liveMu.Lock()
	n := len(live)
	liveMu.Unlock()
	if n != 0 {
		t.Errorf("%d parts still registered", n)
	}
}

// On portableName, not through Path: Windows refuses these via IsLocal anyway,
// so a test going through it would say nothing about Linux.
func TestANameIsHeldToTheStrictestRule(t *testing.T) {
	for _, p := range []string{"C:/Windows/evil.dll", "c:evil", "sha256:abc", `a\b`, `..\x`} {
		if portableName(p) {
			t.Errorf("%q passed as a portable name", p)
		}
	}
	for _, p := range []string{"model.gguf", "dir/model.gguf", "owner/repo", "abc123", "mmproj-model-f16.gguf"} {
		if !portableName(p) {
			t.Errorf("%q was refused", p)
		}
	}
}
