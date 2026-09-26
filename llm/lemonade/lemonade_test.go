package lemonade

import (
	"bytes"
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
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tradalab/scorix/llm"
	"github.com/tradalab/scorix/llm/internal/install/installtest"
	"github.com/tradalab/scorix/llm/model"
	"github.com/tradalab/scorix/llm/probe"
)

// The test binary stands in for lemond: it writes down how it was started and
// answers health on the port it was given.
func TestMain(m *testing.M) {
	if out := os.Getenv("FAKE_LEMOND_OUT"); out != "" {
		args := os.Args[1:]
		rec, _ := json.Marshal(map[string]any{"args": args, "hf": os.Getenv("HF_HUB_CACHE")})
		_ = os.WriteFile(out, rec, 0o600)
		port := args[slices.Index(args, "--port")+1]
		// Every start, not only the last, so a test can read the command line
		// of a retry as well as of the first try.
		if log := os.Getenv("FAKE_LEMOND_LOG"); log != "" {
			if f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
				_, _ = f.Write(append(rec, "\n"...))
				_ = f.Close()
			}
		}
		// Stands in for the port being taken between the pick and the bind,
		// once, in lemond's own wording - which is what proc.PortTaken reads.
		if once := os.Getenv("FAKE_LEMOND_BIND_FAILS_ONCE"); once != "" {
			if _, err := os.Stat(once); err != nil {
				_ = os.WriteFile(once, nil, 0o600)
				fmt.Fprintf(os.Stderr, "error: couldn't bind to 127.0.0.1:%s (address already in use)\n", port)
				os.Exit(1)
			}
		}
		http.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"status":"ok"}`) })
		_ = http.ListenAndServe("127.0.0.1:"+port, nil)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestStartKeepsLemondInsideItsDir(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "started.json")
	t.Setenv("FAKE_LEMOND_OUT", out)
	dir := t.TempDir()
	s, err := Start(context.Background(), self, ServerOptions{Dir: dir, Args: []string{"--log-level", "debug"}, ReadyTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop(context.Background())
	var rec struct {
		Args []string `json:"args"`
		HF   string   `json:"hf"`
	}
	b, _ := os.ReadFile(out)
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	port := rec.Args[slices.Index(rec.Args, "--port")+1]
	want := []string{"--host", "127.0.0.1", "--port", port, "--no-broadcast", "--log-file", "disabled", "--log-level", "debug", filepath.Join(dir, "cache"), filepath.Join(dir, "config")}
	if !slices.Equal(rec.Args, want) || rec.HF != filepath.Join(dir, "huggingface") {
		t.Errorf("started with %q, HF_HUB_CACHE %q", rec.Args, rec.HF)
	}
	if s.BaseURL() != "http://127.0.0.1:"+port+"/api/v1" {
		t.Errorf("base %s", s.BaseURL())
	}
	p, err := s.Provider("lemonade")
	if err != nil || !llm.IsLocal(p) || !llm.Has(p, llm.Transcribe) || !llm.Has(p, llm.Chat) || !llm.Has(p, llm.Embed) {
		t.Errorf("provider %v caps %v", err, p.Capabilities())
	}
	if only, _ := s.Provider("dictation", llm.Transcribe); llm.Has(only, llm.Chat) || !llm.Has(only, llm.Transcribe) {
		t.Errorf("asked for transcribe only, got %v", only.Capabilities())
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Error("still running after Stop")
	}
}

// The port a picked listener proved free can be taken by the time the child
// binds it. The retry has to build a whole command line again rather than patch
// the last one: the two directories lemond takes are positional, and rewriting
// "the element after --port" in a copy overwrote the first of them.
func TestARetakenPortIsRetriedWithAWholeCommandLine(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tmp, dir := t.TempDir(), t.TempDir()
	log := filepath.Join(tmp, "starts.jsonl")
	t.Setenv("FAKE_LEMOND_OUT", filepath.Join(tmp, "started.json"))
	t.Setenv("FAKE_LEMOND_LOG", log)
	t.Setenv("FAKE_LEMOND_BIND_FAILS_ONCE", filepath.Join(tmp, "once"))
	s, err := Start(context.Background(), self, ServerOptions{Dir: dir, Args: []string{"--log-level", "debug"}, ReadyTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop(context.Background())

	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var starts [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var rec struct {
			Args []string `json:"args"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		starts = append(starts, rec.Args)
	}
	if len(starts) != 2 {
		t.Fatalf("started %d times: %q", len(starts), starts)
	}
	var ports []string
	for i, args := range starts {
		port := args[slices.Index(args, "--port")+1]
		ports = append(ports, port)
		want := []string{"--host", "127.0.0.1", "--port", port, "--no-broadcast", "--log-file", "disabled", "--log-level", "debug", filepath.Join(dir, "cache"), filepath.Join(dir, "config")}
		if !slices.Equal(args, want) {
			t.Errorf("start %d: %q\nwant %q", i+1, args, want)
		}
	}
	if ports[0] == ports[1] {
		t.Errorf("both tries used port %s", ports[0])
	}
	if s.BaseURL() != "http://127.0.0.1:"+ports[1]+"/api/v1" {
		t.Errorf("base %s, second try took port %s", s.BaseURL(), ports[1])
	}
}

// One port, not two opinions about it: the one here decides the base URL that
// health and every later request use.
func TestAPortInArgsIsRefused(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), self, ServerOptions{Dir: t.TempDir(), Args: []string{"--port", "1234"}}); err == nil {
		t.Error("a second --port was accepted")
	}
}

func TestStartNeedsADir(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_LEMOND_OUT", filepath.Join(t.TempDir(), "started.json"))
	if s, err := Start(context.Background(), self, ServerOptions{ReadyTimeout: 20 * time.Second}); err == nil {
		s.Stop(context.Background())
		t.Error("started without a directory, so into the user's own")
	}
}

func fakeServer(t *testing.T, h http.HandlerFunc) *Provider {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := New(Config{ID: "lemonade", BaseURL: srv.URL + "/api/v1/"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func sse(w http.ResponseWriter, events ...string) {
	for _, e := range events {
		_, _ = io.WriteString(w, e+"\n\n")
		w.(http.Flusher).Flush()
	}
}

func progress(file string, idx, n int, done, total int64, pct int) string {
	return fmt.Sprintf("event: progress\ndata: {\"file\":%q,\"file_index\":%d,\"total_files\":%d,\"bytes_downloaded\":%d,\"bytes_total\":%d,\"percent\":%d}", file, idx, n, done, total, pct)
}

// Shaped like what Lemonade 11.9.0 sent for Whisper-Tiny: a first event with
// the size, repeats of it with none, then one per few hundred bytes.
func TestPullReportsEachPercentOnce(t *testing.T) {
	var body map[string]any
	p := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/pull" || r.Header.Get("Content-Type") != "application/json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sse(w,
			progress("ggml-tiny.bin", 1, 2, 0, 200, 0),
			progress("ggml-tiny.bin", 1, 2, 0, 0, 0),
			progress("ggml-tiny.bin", 1, 2, 1, 200, 0),
			progress("ggml-tiny.bin", 1, 2, 100, 200, 50),
			progress("ggml-tiny.bin", 1, 2, 101, 200, 50),
			progress("ggml-tiny.bin", 1, 2, 200, 200, 100),
			// A second file opens without its size, like the first did.
			progress("vocab.bin", 2, 2, 0, 0, 0),
			progress("vocab.bin", 2, 2, 5, 100, 0),
			`event: complete`+"\n"+`data: {"file":"","file_index":2,"percent":100}`)
	})
	var got []llm.PullProgress
	if err := p.Pull(context.Background(), "Whisper-Tiny", func(pp llm.PullProgress) { got = append(got, pp) }); err != nil {
		t.Fatal(err)
	}
	if body["model_name"] != "Whisper-Tiny" || body["stream"] != true {
		t.Errorf("sent %v", body)
	}
	want := []llm.PullProgress{
		{Status: "downloading ggml-tiny.bin (1/2)", Done: 0, Total: 200},
		{Status: "downloading ggml-tiny.bin (1/2)", Done: 100, Total: 200},
		{Status: "downloading ggml-tiny.bin (1/2)", Done: 200, Total: 200},
		{Status: "downloading vocab.bin (2/2)", Done: 5, Total: 100},
	}
	if !slices.Equal(got, want) {
		t.Errorf("progress %+v", got)
	}
}

func TestPullReportsAFailureSentInsideTheStream(t *testing.T) {
	p := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, `event: error`+"\n"+`data: {"code":"unknown_model","error":"When registering a new model, the model name must include the user namespace"}`)
	})
	err := p.Pull(context.Background(), "No-Such-Model", nil)
	if !errors.Is(err, llm.ErrModelNotFound) || !strings.Contains(err.Error(), "user namespace") {
		t.Errorf("err = %v", err)
	}
}

func TestAPullCutShortIsNotAPull(t *testing.T) {
	p := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, progress("m.gguf", 1, 1, 10, 200, 5))
	})
	if err := p.Pull(context.Background(), "m", nil); !errors.Is(err, llm.ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
}

func TestDescribeTakesCapabilitiesFromTheLabels(t *testing.T) {
	p := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models/Gemma-3-4b-it-GGUF":
			_, _ = io.WriteString(w, `{"id":"Gemma-3-4b-it-GGUF","labels":["chat","vision","hot"],"context_length":8192}`)
		case "/api/v1/models/Whisper-Tiny":
			_, _ = io.WriteString(w, `{"id":"Whisper-Tiny","labels":["transcription","realtime-transcription"]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"model_not_found","message":"Model 'No-Such' was not found."}}`)
		}
	})
	d, err := p.Describe(context.Background(), "Gemma-3-4b-it-GGUF")
	if err != nil || d.ID != "Gemma-3-4b-it-GGUF" || d.ContextLength != 8192 || !slices.Equal(d.Capabilities, []llm.Capability{llm.Chat, llm.Vision}) {
		t.Errorf("%+v %v", d, err)
	}
	d, err = p.Describe(context.Background(), "Whisper-Tiny")
	if err != nil || !slices.Equal(d.Capabilities, []llm.Capability{llm.Transcribe}) {
		t.Errorf("%+v %v", d, err)
	}
	if _, err := p.Describe(context.Background(), "No-Such"); !errors.Is(err, llm.ErrModelNotFound) || !strings.Contains(err.Error(), "was not found") {
		t.Errorf("err = %v", err)
	}
	for label, c := range map[string]llm.Capability{"embeddings": llm.Embed, "tool-calling": llm.Tools} {
		if labelCaps[label] != c {
			t.Errorf("%s -> %v", label, labelCaps[label])
		}
	}
}

func TestDeleteAndItsRefusal(t *testing.T) {
	var deleted []any
	p := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/delete" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		deleted = append(deleted, body["model_name"])
		if body["model_name"] != "Whisper-Tiny" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"code":"unknown_model","error":"cannot remove the file"}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"success"}`)
	})
	if err := p.Delete(context.Background(), "Whisper-Tiny"); err != nil {
		t.Error(err)
	}
	if err := p.Delete(context.Background(), "x"); !errors.Is(err, llm.ErrModelNotFound) || !strings.HasSuffix(err.Error(), "cannot remove the file") {
		t.Errorf("err = %v", err)
	}
	if len(deleted) != 2 {
		t.Errorf("deleted %v", deleted)
	}
}

// Lemonade asks for a key once LEMONADE_API_KEY is set.
func TestAnswersWithoutWordsStillSayWhatWentWrong(t *testing.T) {
	p := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/delete":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "loading")
		}
	})
	if err := p.Delete(context.Background(), "m"); !errors.Is(err, llm.ErrUnauthorized) {
		t.Errorf("401: %v", err)
	}
	if _, err := p.Describe(context.Background(), "m"); !errors.Is(err, llm.ErrUnavailable) || !strings.Contains(err.Error(), "loading") {
		t.Errorf("503: %v", err)
	}
}

func TestAssetForEachPlatform(t *testing.T) {
	for _, c := range []struct{ os, arch, want string }{
		{"windows", "amd64", "-windows-x64.zip"},
		{"linux", "amd64", "-ubuntu-x64.tar.gz"},
		{"linux", "arm64", "-ubuntu-arm64.tar.gz"},
		{"darwin", "arm64", "-macos-arm64.tar.gz"},
	} {
		if got, err := assetSuffix(probe.Machine{OS: c.os, Arch: c.arch}); err != nil || got != c.want {
			t.Errorf("%s/%s: %q %v", c.os, c.arch, got, err)
		}
	}
	if _, err := assetSuffix(probe.Machine{OS: "darwin", Arch: "amd64"}); !errors.Is(err, errNoBuild) {
		t.Errorf("Intel mac: %v", err)
	}
}

func fakeReleases(t *testing.T, assets map[string][]byte) *model.GitHubReleases {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/lemonade-sdk/lemonade/releases/tags/v11.9.0" {
			var list []map[string]any
			for name, b := range assets {
				s := sha256.Sum256(b)
				list = append(list, map[string]any{"name": name, "size": len(b), "digest": "sha256:" + hex.EncodeToString(s[:]), "browser_download_url": srv.URL + "/dl/" + name})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v11.9.0", "assets": list})
			return
		}
		b, ok := assets[strings.TrimPrefix(r.URL.Path, "/dl/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
	}))
	t.Cleanup(srv.Close)
	return &model.GitHubReleases{Endpoint: srv.URL}
}

func thisMachine(t *testing.T) (probe.Machine, string) {
	m := probe.Machine{OS: runtime.GOOS, Arch: runtime.GOARCH}
	suffix, err := assetSuffix(m)
	if err != nil {
		t.Skip("no embeddable build for this platform")
	}
	return m, suffix
}

// The release also carries the MSI and distro packages, which install a
// system service rather than something an app can run.
func TestEnsureInstallsTheEmbeddableBuildAndFindsItOffline(t *testing.T) {
	m, suffix := thisMachine(t)
	top := "lemonade-embeddable-11.9.0" + strings.TrimSuffix(strings.TrimSuffix(suffix, ".zip"), ".tar.gz")
	entries := []installtest.Entry{{Name: top + "/" + serverName(), Body: "lemond", Mode: 0o755}, {Name: top + "/resources/defaults.json", Body: "{}", Mode: 0o644}}
	archive := installtest.Tgz(t, entries...)
	if strings.HasSuffix(suffix, ".zip") {
		archive = installtest.Zip(t, entries...)
	}
	gh := fakeReleases(t, map[string][]byte{
		"lemonade-embeddable-11.9.0" + suffix: archive, "lemonade.msi": []byte("msi"),
		// Same platform suffix, not the embeddable build.
		"lemonade-server-11.9.0" + suffix: []byte("not this"),
	})
	in := &Installer{Store: &model.Store{Root: t.TempDir()}, Releases: gh, Tag: "v11.9.0"}
	exe, err := in.Ensure(context.Background(), m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "lemond" || !strings.Contains(filepath.ToSlash(exe), "/lemonade/v11.9.0/") {
		t.Errorf("exe %s", exe)
	}
	in.Releases = &model.GitHubReleases{Endpoint: "http://127.0.0.1:1"}
	if again, err := in.Ensure(context.Background(), m, nil); err != nil || again != exe {
		t.Errorf("offline: %s %v", again, err)
	}
	if _, ok := (&Installer{Store: in.Store}).Installed(); ok {
		t.Error("an unpinned installer claimed an install")
	}
}

func TestEnsureSaysWhenTheReleaseHasNoBuildForThisMachine(t *testing.T) {
	m, _ := thisMachine(t)
	in := &Installer{Store: &model.Store{Root: t.TempDir()}, Releases: fakeReleases(t, map[string][]byte{"lemonade.msi": []byte("msi")}), Tag: "v11.9.0"}
	if _, err := in.Ensure(context.Background(), m, nil); !errors.Is(err, errNoBuild) {
		t.Errorf("err = %v", err)
	}
}

// A server that dies mid-download closes the body cleanly, so the half-written
// line arrives with io.EOF rather than an error. Still a broken connection.
func TestAPullCutAtACleanCloseIsABrokenConnection(t *testing.T) {
	p := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: progress\ndata: {\"file\":\"a\"")
		w.(http.Flusher).Flush()
	})
	err := p.Pull(context.Background(), "m", nil)
	if !errors.Is(err, llm.ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
}

// A download the network cut short is a broken connection, not a server that
// sent something unreadable: only the first is worth retrying.
func TestAPullCutMidLineIsABrokenConnection(t *testing.T) {
	p := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: progress\ndata: {\"file\":\"a\"")
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	})
	err := p.Pull(context.Background(), "m", nil)
	if !errors.Is(err, llm.ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
}
