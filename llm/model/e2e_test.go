package model

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Cuts the first response from each CDN host after n bytes, the way a flaky
// connection does, so the resume path runs against the real CDN. The hubs'
// own API hosts answer listings and tokens, which are not resumed.
type cutOnce struct {
	next http.RoundTripper
	n    int64
	mu   sync.Mutex
	cut  map[string]bool
	// Bodies that really were cut: a file smaller than n never is.
	hits atomic.Int32
}

func (c *cutOnce) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(r)
	if err != nil || apiHosts[r.URL.Host] || resp.StatusCode/100 != 2 {
		return resp, err
	}
	c.mu.Lock()
	first := !c.cut[r.URL.Path]
	c.cut[r.URL.Path] = true
	c.mu.Unlock()
	if first {
		resp.Body = &cutBody{r: resp.Body, left: c.n, hits: &c.hits}
	}
	return resp, nil
}

var apiHosts = map[string]bool{"huggingface.co": true, "registry.ollama.ai": true, "registry-1.docker.io": true, "auth.docker.io": true}

type cutBody struct {
	r    io.ReadCloser
	left int64
	hits *atomic.Int32
}

func (b *cutBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		b.hits.Add(1)
		return 0, io.ErrUnexpectedEOF
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.r.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *cutBody) Close() error { return b.r.Close() }

// Against huggingface.co, gated on SCORIX_MODEL_E2E=1: about 26 MB. With
// SCORIX_MODEL_E2E_LLAMA=<llama-server> it also loads what it downloaded.
func TestAgainstHuggingFace(t *testing.T) {
	if os.Getenv("SCORIX_MODEL_E2E") != "1" {
		t.Skip("set SCORIX_MODEL_E2E=1 to download from huggingface.co")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cutter := &cutOnce{next: http.DefaultTransport, n: 2 << 20, cut: map[string]bool{}}
	hf := &HuggingFace{Client: &http.Client{Transport: cutter}}

	// Renamed to ggml-org/models-moved; the old name still answers, by redirect.
	snap, err := hf.Resolve(ctx, "ggml-org/models")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s@%s -> %s, %d files", snap.Repo, snap.Ref, snap.Revision, len(snap.Files))
	var split *Variant
	for _, v := range Variants(snap) {
		if v.Key == "tinyllamas/split/stories15M-q8_0" {
			split = &v
		}
	}
	if split == nil || !split.Complete || len(split.Files) != 3 {
		t.Fatalf("the split q8_0 model = %+v", split)
	}
	for _, f := range split.Files {
		if f.Digest == nil || f.Digest.Algo != "sha256" {
			t.Errorf("%s has no sha256: %+v", f.Path, f.Digest)
		}
	}

	store := &Store{Root: t.TempDir()}
	start := time.Now()
	paths, err := store.Get(ctx, hf, snap, split.All(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("3 shards, %d bytes, verified, in %s; cut after 2 MiB: %d", split.Size, time.Since(start).Round(time.Millisecond), cutter.hits.Load())
	if cutter.hits.Load() == 0 {
		t.Error("no CDN response was cut, so resume was not exercised")
	}

	// llama.cpp loads shard 1 and finds the rest beside it by name.
	if llama := os.Getenv("SCORIX_MODEL_E2E_LLAMA"); llama != "" && !loads(ctx, t, llama, paths[0]) {
		t.Error("llama-server did not become healthy on the downloaded shards")
	}
}

func loads(ctx context.Context, t *testing.T, llama, model string, args ...string) bool {
	port := freePort(t)
	cmd := exec.CommandContext(ctx, llama, append([]string{"-m", model, "--host", "127.0.0.1", "--port", fmt.Sprint(port), "-c", "256"}, args...)...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
	}
	return false
}

// Against the Ollama library and Docker Hub, gated on SCORIX_OCI_E2E=1: about
// 135 MB. With SCORIX_MODEL_E2E_LLAMA=<llama-server> it also loads both.
func TestAgainstOCIRegistries(t *testing.T) {
	if os.Getenv("SCORIX_OCI_E2E") != "1" {
		t.Skip("set SCORIX_OCI_E2E=1 to download from registry.ollama.ai and docker.io")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cutter := &cutOnce{next: http.DefaultTransport, n: 2 << 20, cut: map[string]bool{}}
	src := &OCI{Client: &http.Client{Transport: cutter}}
	store := &Store{Root: t.TempDir()}
	for _, c := range []struct {
		ref, file string
		args      []string
	}{
		{"registry.ollama.ai/library/all-minilm:22m", "all-minilm-F16.gguf", []string{"--embeddings"}},
		{"docker.io/ai/smollm2:135M-Q2_K", "smollm2-Q2_K.gguf", nil},
	} {
		snap, err := src.Resolve(ctx, c.ref)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s -> %s: %v", c.ref, snap.Revision, paths(snap))
		vs := Variants(snap)
		if len(vs) != 1 || vs[0].Files[0].Path != c.file {
			t.Fatalf("%s: variants %+v", c.ref, vs)
		}
		// Ollama's registry has no manifest by digest; the pin is checked
		// against what the tag gives instead.
		again, err := src.Resolve(ctx, c.ref+"@sha256:"+snap.Revision)
		if err != nil || again.Revision != snap.Revision {
			t.Errorf("%s pinned: %v %v", c.ref, again, err)
		}
		start := time.Now()
		got, err := store.Get(ctx, src, snap, snap.Files, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d files, %d bytes of weights, verified, in %s", c.ref, len(got), vs[0].Size, time.Since(start).Round(time.Millisecond))
		if llama := os.Getenv("SCORIX_MODEL_E2E_LLAMA"); llama != "" && !loads(ctx, t, llama, got[0], c.args...) {
			t.Errorf("llama-server did not load %s", c.ref)
		}
	}
	t.Logf("CDN responses cut after 2 MiB: %d", cutter.hits.Load())
	if cutter.hits.Load() < 2 {
		t.Error("the CDNs of both registries were not cut, so resume was not exercised")
	}
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
