package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type blob struct {
	media string
	body  string
	path  string
}

func ociDigest(s string) string {
	h := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(h[:])
}

type fakeRegistry struct {
	srv, cdn *httptest.Server
	// Answers only with a token, the way Docker Hub does.
	auth bool
	// Refuses a manifest by digest, the way Ollama's registry does.
	noDigest bool
	manifest string
	blobs    map[string]string
	// What the token service answers; Docker Hub's shape when empty.
	tokenBody string

	tokens, configs atomic.Int32
	sentAuth        atomic.Value
	cdnAuth         atomic.Value
}

// Layers become a manifest of mediaType; a config with body cfg is added when
// cfg is not empty.
func newRegistry(t *testing.T, mediaType, cfg string, layers ...blob) *fakeRegistry {
	r := &fakeRegistry{blobs: map[string]string{}}
	var descs []map[string]any
	for _, l := range layers {
		d := map[string]any{"mediaType": l.media, "digest": ociDigest(l.body), "size": len(l.body)}
		if l.path != "" {
			d["annotations"] = map[string]string{annotationFilePath: l.path}
		}
		descs = append(descs, d)
		r.blobs[ociDigest(l.body)] = l.body
	}
	m := map[string]any{"schemaVersion": 2, "mediaType": mediaType, "layers": descs}
	if cfg != "" {
		m["config"] = map[string]any{"mediaType": "application/vnd.docker.container.image.v1+json", "digest": ociDigest(cfg), "size": len(cfg)}
		r.blobs[ociDigest(cfg)] = cfg
	}
	b, _ := json.Marshal(m)
	r.manifest = string(b)
	r.cdn = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.cdnAuth.Store(req.Header.Get("Authorization"))
		body, ok := r.blobs[strings.TrimPrefix(req.URL.Path, "/")]
		if !ok {
			http.NotFound(w, req)
			return
		}
		http.ServeContent(w, req, "", time.Time{}, strings.NewReader(body))
	}))
	r.srv = httptest.NewTLSServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.srv.Close)
	t.Cleanup(r.cdn.Close)
	return r
}

func (r *fakeRegistry) serve(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/token" {
		r.tokens.Add(1)
		if q := req.URL.Query(); q.Get("service") != "reg" || q.Get("scope") != "repository:library/m:pull" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body := r.tokenBody
		if body == "" {
			body = `{"token":"t1","expires_in":300}`
		}
		_, _ = io.WriteString(w, body)
		return
	}
	r.sentAuth.Store(req.Header.Get("Authorization"))
	name, rest, _ := strings.Cut(strings.TrimPrefix(req.URL.Path, "/v2/"), "/")
	ns, rest, _ := strings.Cut(rest, "/")
	name += "/" + ns
	if r.auth && req.Header.Get("Authorization") != "Bearer t1" {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="reg",scope="repository:%s:pull"`, r.srv.URL, name))
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`)
		return
	}
	kind, ref, _ := strings.Cut(rest, "/")
	switch {
	case kind == "manifests" && !strings.Contains(req.Header.Get("Accept"), mediaOCIManifest):
		w.WriteHeader(http.StatusNotAcceptable)
	case kind == "manifests" && name == "library/m" && (ref == "latest" || ref == "v1" || (!r.noDigest && ref == ociDigest(r.manifest))):
		_, _ = io.WriteString(w, r.manifest)
	case kind == "blobs" && r.blobs[ref] != "":
		if !strings.HasPrefix(r.blobs[ref], "GGUF") {
			r.configs.Add(1)
			_, _ = io.WriteString(w, r.blobs[ref])
			return
		}
		http.Redirect(w, req, r.cdn.URL+"/"+ref, http.StatusTemporaryRedirect)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}`)
	}
}

func (r *fakeRegistry) ref(suffix string) string {
	return strings.TrimPrefix(r.srv.URL, "https://") + "/library/m" + suffix
}

func (r *fakeRegistry) source() *OCI { return &OCI{Client: r.srv.Client()} }

func paths(s *Snapshot) []string {
	var out []string
	for _, f := range s.Files {
		out = append(out, f.Path)
	}
	return out
}

func TestOllamaLayersBecomeFilesAModelCanLoad(t *testing.T) {
	r := newRegistry(t, "application/vnd.docker.distribution.manifest.v2+json", `{"model_format":"gguf","file_type":"Q4_K_M"}`,
		blob{media: "application/vnd.ollama.image.model", body: "GGUF model"},
		blob{media: "application/vnd.ollama.image.projector", body: "GGUF projector"},
		blob{media: "application/vnd.ollama.image.license", body: "license"},
		blob{media: "application/vnd.ollama.image.template", body: "{{ .Prompt }}"},
		blob{media: "application/vnd.ollama.image.params", body: `{"stop":["x"]}`},
		blob{media: "application/vnd.ollama.image.system", body: "be brief"},
		blob{media: "application/vnd.ollama.image.adapter", body: "lora"},
	)
	snap, err := r.source().Resolve(context.Background(), r.ref(""))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"m-Q4_K_M.gguf", "mmproj.gguf", "LICENSE", "template", "params.json", "system.txt", "blobs/sha256-" + strings.TrimPrefix(ociDigest("lora"), "sha256:")}
	if strings.Join(paths(snap), ",") != strings.Join(want, ",") {
		t.Errorf("files %v", paths(snap))
	}
	if snap.Revision != strings.TrimPrefix(ociDigest(r.manifest), "sha256:") || snap.Ref != "latest" || snap.Source != "oci" {
		t.Errorf("snapshot %+v", snap)
	}
	if f := snap.Files[0]; f.Size != int64(len("GGUF model")) || f.Digest.Hex != strings.TrimPrefix(ociDigest("GGUF model"), "sha256:") {
		t.Errorf("model file %+v", f)
	}
	vs := Variants(snap)
	if len(vs) != 1 || vs[0].Name != "Q4_K_M" || vs[0].Projector == nil || vs[0].Projector.Path != "mmproj.gguf" || !vs[0].Complete {
		t.Errorf("variants %+v", vs)
	}
}

// Docker's older packaging: several unnamed weights are the shards of one
// model, and two license layers must not overwrite each other.
func TestDockerShardsKeepTheirOrder(t *testing.T) {
	r := newRegistry(t, "application/vnd.oci.image.manifest.v1+json", `{"config":{"format":"gguf","quantization":"MOSTLY_MXFP4"}}`,
		blob{media: "application/vnd.docker.ai.gguf.v3", body: "GGUF 1"},
		blob{media: "application/vnd.docker.ai.gguf.v3", body: "GGUF 2"},
		blob{media: "application/vnd.docker.ai.gguf.v3", body: "GGUF 3"},
		blob{media: "application/vnd.docker.ai.license", body: "a"},
		blob{media: "application/vnd.docker.ai.license", body: "b"},
	)
	snap, err := r.source().Resolve(context.Background(), r.ref(":v1"))
	if err != nil {
		t.Fatal(err)
	}
	want := "m-MXFP4-00001-of-00003.gguf,m-MXFP4-00002-of-00003.gguf,m-MXFP4-00003-of-00003.gguf,LICENSE,LICENSE-2"
	if got := strings.Join(paths(snap), ","); got != want {
		t.Errorf("files %s", got)
	}
	if snap.Files[1].Digest.Hex != strings.TrimPrefix(ociDigest("GGUF 2"), "sha256:") {
		t.Error("shard 2 is not the second layer")
	}
	if vs := Variants(snap); len(vs) != 1 || len(vs[0].Files) != 3 || !vs[0].Complete {
		t.Errorf("variants %+v", vs)
	}
}

// The newer packaging names every file, so the config is not read at all.
func TestAnnotatedLayersKeepTheirNames(t *testing.T) {
	r := newRegistry(t, "application/vnd.oci.image.manifest.v1+json", `{"config":{"quantization":"Q8_0"}}`,
		blob{media: "application/vnd.cncf.model.weight.v1.raw", body: "GGUF w", path: "gemma-3-12b-it-Q4_K_M.gguf"},
		blob{media: "application/vnd.cncf.model.weight.v1.raw", body: "GGUF p", path: "mmproj-F16.gguf"},
	)
	snap, err := r.source().Resolve(context.Background(), r.ref(""))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(paths(snap), ","); got != "gemma-3-12b-it-Q4_K_M.gguf,mmproj-F16.gguf" || r.configs.Load() != 0 {
		t.Errorf("files %s, config read %d times", got, r.configs.Load())
	}
}

// Docker Hub's flow: a 401 naming a token service, an anonymous token, and a
// blob that redirects to a CDN on another host.
func TestTheTokenIsFetchedOnceAndStaysOffTheCDN(t *testing.T) {
	r := newRegistry(t, "application/vnd.oci.image.manifest.v1+json", `{"config":{"quantization":"Q4_0"}}`,
		blob{media: "application/vnd.docker.ai.gguf.v3", body: "GGUF weights"})
	r.auth = true
	src := r.source()
	snap, err := src.Resolve(context.Background(), r.ref(""))
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: t.TempDir()}
	got, err := store.Get(context.Background(), src, snap, snap.Files, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got[0]); string(b) != "GGUF weights" {
		t.Errorf("stored %q", b)
	}
	if r.tokens.Load() != 1 || r.sentAuth.Load() != "Bearer t1" || r.cdnAuth.Load() != "" {
		t.Errorf("tokens %d, registry saw %q, CDN saw %q", r.tokens.Load(), r.sentAuth.Load(), r.cdnAuth.Load())
	}

	// The OAuth2 name for the same thing, which some token services use.
	r.tokenBody = `{"access_token":"t1"}`
	if _, err := r.source().Resolve(context.Background(), r.ref("")); err != nil {
		t.Errorf("access_token: %v", err)
	}
}

func TestOpenResumesAtAnOffset(t *testing.T) {
	r := newRegistry(t, "application/vnd.oci.image.manifest.v1+json", "", blob{media: "application/vnd.docker.ai.gguf.v3", body: "GGUF weights"})
	src := r.source()
	snap, err := src.Resolve(context.Background(), r.ref(""))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := src.Open(context.Background(), snap, snap.Files[0], 5)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "weights" {
		t.Errorf("from offset 5: %q", b)
	}
	gone := snap.Files[0]
	gone.URL = strings.Replace(gone.URL, gone.Digest.Hex, strings.Repeat("0", 64), 1)
	if _, err := src.Open(context.Background(), snap, gone, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("a blob the registry does not have: %v", err)
	}
}

func TestLayerDigestsMustBeSHA256(t *testing.T) {
	r := newRegistry(t, "application/vnd.oci.image.manifest.v1+json", "", blob{media: "application/vnd.docker.ai.gguf.v3", body: "GGUF"})
	r.manifest = strings.Replace(r.manifest, `"digest":"sha256:`, `"digest":"sha512:`, 1)
	if _, err := r.source().Resolve(context.Background(), r.ref("")); err == nil || !strings.Contains(err.Error(), "sha512") {
		t.Errorf("err = %v", err)
	}
}

// No usable quantization: no config, one too big to read, or a value that is
// not safe in a file name.
func TestWithoutAQuantizationTheModelIsNamedAfterItsRepo(t *testing.T) {
	weights := blob{media: "application/vnd.docker.ai.gguf.v3", body: "GGUF"}
	big := `{"config":{"quantization":"Q8_0"}, "pad":"x"}`
	for name, cfg := range map[string]string{"none": "", "big": big, "unsafe": `{"file_type":"../../x"}`} {
		r := newRegistry(t, "application/vnd.oci.image.manifest.v1+json", cfg, weights)
		if name == "big" {
			r.manifest = strings.Replace(r.manifest, fmt.Sprintf(`"size":%d}`, len(big)), `"size":2097152}`, 1)
		}
		snap, err := r.source().Resolve(context.Background(), r.ref(""))
		if err != nil || snap.Files[0].Path != "m.gguf" {
			t.Errorf("%s: %v %v", name, paths(snap), err)
		}
		if name == "big" && r.configs.Load() != 0 {
			t.Error("a config declared at 2 MB was read")
		}
	}
}

// Two layers with one name must not overwrite each other, and a projector
// must stay a .gguf for anything to recognise it.
func TestRepeatedNamesKeepTheirExtension(t *testing.T) {
	r := newRegistry(t, "application/vnd.oci.image.manifest.v1+json", "",
		blob{media: "application/vnd.docker.ai.mmproj", body: "GGUF p1"},
		blob{media: "application/vnd.docker.ai.mmproj", body: "GGUF p2"})
	snap, err := r.source().Resolve(context.Background(), r.ref(""))
	if err != nil || strings.Join(paths(snap), ",") != "mmproj.gguf,mmproj-2.gguf" {
		t.Errorf("%v %v", paths(snap), err)
	}
}

func TestAPinHoldsWhereTheRegistryCannotLookItUp(t *testing.T) {
	for _, noDigest := range []bool{false, true} {
		r := newRegistry(t, "application/vnd.oci.image.manifest.v1+json", "", blob{media: "application/vnd.docker.ai.license", body: "l"})
		r.noDigest = noDigest
		want := strings.TrimPrefix(ociDigest(r.manifest), "sha256:")
		snap, err := r.source().Resolve(context.Background(), r.ref(":latest@sha256:"+want))
		if err != nil || snap.Revision != want {
			t.Errorf("noDigest %v: %v %v", noDigest, snap, err)
		}
		other := strings.Repeat("0", 64)
		if _, err := r.source().Resolve(context.Background(), r.ref(":latest@sha256:"+other)); !errors.Is(err, ErrNotFound) {
			t.Errorf("noDigest %v: a moved tag passed as the pinned manifest: %v", noDigest, err)
		}
	}
}

func TestMissingAndPrivateReadAsNotFound(t *testing.T) {
	r := newRegistry(t, "application/vnd.oci.image.manifest.v1+json", "", blob{media: "application/vnd.docker.ai.license", body: "l"})
	host := strings.TrimPrefix(r.srv.URL, "https://")
	_, err := r.source().Resolve(context.Background(), host+"/library/other")
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "MANIFEST_UNKNOWN: manifest unknown") || strings.Contains(err.Error(), "private") {
		t.Errorf("missing: %v", err)
	}
	r.auth = true
	r.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/token" {
			_, _ = io.WriteString(w, `{"token":"other"}`)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+r.srv.URL+`/token"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	if _, err := r.source().Resolve(context.Background(), r.ref("")); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "private") {
		t.Errorf("private: %v", err)
	}
}

func TestAnIndexIsNotAModel(t *testing.T) {
	r := newRegistry(t, "application/vnd.oci.image.index.v1+json", "")
	if _, err := r.source().Resolve(context.Background(), r.ref("")); err == nil || !strings.Contains(err.Error(), "index") {
		t.Errorf("err = %v", err)
	}
}

func TestOCIRefs(t *testing.T) {
	for ref, want := range map[string]string{
		"docker.io/ai/smollm2":                               "docker.io ai/smollm2 latest ",
		"registry.ollama.ai/library/qwen3:0.6b":              "registry.ollama.ai library/qwen3 0.6b ",
		"localhost:5000/a/b/c:t":                             "localhost:5000 a/b/c t ",
		"docker.io/ai/smollm2@sha256:" + zeros():             "docker.io ai/smollm2  " + zeros(),
		"docker.io/ai/smollm2:x@" + strings.ToUpper(zeros()): "docker.io ai/smollm2 x " + zeros(),
	} {
		host, name, tag, pin, err := parseOCIRef(ref)
		if got := strings.Join([]string{host, name, tag, pin}, " "); err != nil || got != want {
			t.Errorf("%s: %q %v", ref, got, err)
		}
	}
	for _, ref := range []string{"qwen3", "ai/smollm2", "ai/smollm2/gguf", "registry.ollama.ai/qwen3", "docker.io/AI/Smollm2", "docker.io/ai/smollm2@sha256:abc", "docker.io/ai/smollm2@sha256:abcd"} {
		if _, _, _, _, err := parseOCIRef(ref); err == nil {
			t.Errorf("%s was accepted", ref)
		}
	}
	if ociAPIHost("docker.io") != "registry-1.docker.io" || ociAPIHost("registry.ollama.ai") != "registry.ollama.ai" {
		t.Error("api host")
	}
}

func zeros() string { return strings.Repeat("ab", 32) }

func TestChallenge(t *testing.T) {
	got := parseChallenge(`realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:ai/smollm2:pull"`)
	if got["realm"] != "https://auth.docker.io/token" || got["service"] != "registry.docker.io" || got["scope"] != "repository:ai/smollm2:pull" {
		t.Errorf("%v", got)
	}
}
