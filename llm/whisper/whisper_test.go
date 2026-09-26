package whisper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// The test binary stands in for whisper-cli and behaves the ways whisper-cli
// v1.9.1 was seen to on 2026-09-19.
func TestMain(m *testing.M) {
	if mode := os.Getenv("FAKE_WHISPER"); mode != "" {
		os.Exit(fakeWhisper(mode))
	}
	os.Exit(m.Run())
}

func fakeWhisper(mode string) int {
	args := os.Args[1:]
	arg := func(flag string) string {
		if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
			return args[i+1]
		}
		return ""
	}
	_ = os.WriteFile(os.Getenv("FAKE_WHISPER_SEEN"), []byte(strings.Join(args, "\x00")), 0o600)
	audio, _ := os.ReadFile(arg("-f"))
	switch mode {
	case "ok":
		fmt.Fprint(os.Stderr, "whisper_full_with_state: auto-detected language: en (p = 0.998150)\n")
		fmt.Printf("\n %s\n", audio)
		return 0
	case "junk":
		fmt.Fprint(os.Stderr, "read_audio_data: failed to read audio data\nerror: failed to read audio file 'x.wav'\n")
		return 0
	case "nomodel":
		fmt.Fprint(os.Stderr, "error: failed to initialize whisper context\n")
		return 3
	}
	return 1
}

func fake(t *testing.T, mode string) (*Transcriber, func() []string) {
	seen := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_WHISPER", mode)
	t.Setenv("FAKE_WHISPER_SEEN", seen)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return New("whisper", exe), func() []string {
		b, _ := os.ReadFile(seen)
		return strings.Split(string(b), "\x00")
	}
}

func TestTranscribeReadsTextAndTheDetectedLanguage(t *testing.T) {
	tr, args := fake(t, "ok")
	res, err := tr.Transcribe(context.Background(), llm.TranscribeRequest{Model: "ggml-tiny.bin", Audio: llm.AudioClip{MIME: "audio/wav", Data: []byte("the quick brown fox")}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "the quick brown fox" || res.Language != "en" {
		t.Errorf("result = %+v", res)
	}
	got := args()
	if slices.Contains(got, "-np") || got[slices.Index(got, "-l")+1] != "auto" || !strings.HasSuffix(got[slices.Index(got, "-f")+1], ".wav") {
		t.Errorf("args = %q", got)
	}
	if _, err := os.Stat(got[slices.Index(got, "-f")+1]); !os.IsNotExist(err) {
		t.Error("the clip's temp file was left behind")
	}

	// A language given is passed on and reported as given.
	res, err = tr.Transcribe(context.Background(), llm.TranscribeRequest{Model: "m", Language: "vi", Audio: llm.AudioClip{MIME: "audio/mpeg", Data: []byte("xin chao")}})
	if err != nil || res.Language != "vi" || args()[slices.Index(args(), "-l")+1] != "vi" || !strings.HasSuffix(args()[slices.Index(args(), "-f")+1], ".mp3") {
		t.Errorf("given language: %+v %v %q", res, err, args())
	}
}

// whisper-cli exits 0 with an empty transcript for audio it cannot decode,
// which reads exactly like a recording of silence.
func TestAudioItCannotReadIsAnErrorNotSilence(t *testing.T) {
	tr, _ := fake(t, "junk")
	_, err := tr.Transcribe(context.Background(), llm.TranscribeRequest{Model: "m", Audio: llm.AudioClip{MIME: "audio/wav", Data: []byte{1, 2, 3}}})
	if err == nil || !strings.Contains(err.Error(), "failed to read audio file") {
		t.Errorf("err = %v", err)
	}
}

func TestAModelItCannotLoadSaysSo(t *testing.T) {
	tr, _ := fake(t, "nomodel")
	_, err := tr.Transcribe(context.Background(), llm.TranscribeRequest{Model: "missing.bin", Audio: llm.AudioClip{MIME: "audio/wav", Data: []byte("x")}})
	if !errors.Is(err, llm.ErrModelNotFound) {
		t.Errorf("err = %v", err)
	}
}

// Checked before running: a whisper-cli that happened to cope would otherwise
// make a missing model a transcript.
func TestNoModelIsRefusedBeforeRunning(t *testing.T) {
	tr, args := fake(t, "ok")
	if _, err := tr.Transcribe(context.Background(), llm.TranscribeRequest{Audio: llm.AudioClip{MIME: "audio/wav", Data: []byte("x")}}); !errors.Is(err, llm.ErrModelNotFound) {
		t.Errorf("no model: %v", err)
	}
	if len(args()) > 1 {
		t.Error("whisper-cli ran without a model")
	}
}

func TestAFormatItCannotDecodeIsRefusedBeforeRunning(t *testing.T) {
	tr, args := fake(t, "ok")
	if _, err := tr.Transcribe(context.Background(), llm.TranscribeRequest{Model: "m", Audio: llm.AudioClip{MIME: "audio/webm"}}); !errors.Is(err, llm.ErrUnsupported) {
		t.Errorf("err = %v", err)
	}
	if len(args()) > 1 {
		t.Error("whisper-cli ran for a format it cannot read")
	}
}

func TestATranscriberIsASlotLikeAnyOther(t *testing.T) {
	tr, _ := fake(t, "ok")
	r := llm.NewRegistry()
	if err := r.Register(tr); err != nil {
		t.Fatal(err)
	}
	if err := r.Bind("dictation", llm.Binding{Provider: "whisper", Model: "ggml-tiny.bin", LocalOnly: true}); err != nil {
		t.Fatal(err)
	}
	res, err := r.Transcribe(context.Background(), "dictation", llm.TranscribeRequest{Audio: llm.AudioClip{MIME: "audio/wav", Data: []byte("hello")}})
	if err != nil || res.Text != "hello" {
		t.Errorf("%+v, %v", res, err)
	}
	if _, err := r.Chat(context.Background(), "dictation", llm.ChatRequest{}, nil); !errors.Is(err, llm.ErrUnsupported) {
		t.Errorf("chat on a transcriber: %v", err)
	}
}

func TestAssetForEachPlatform(t *testing.T) {
	for _, tc := range []struct {
		os, arch, want string
	}{{"windows", "amd64", "whisper-bin-x64.zip"}, {"linux", "arm64", "whisper-bin-ubuntu-arm64.tar.gz"}} {
		if got, err := asset(probe.Machine{OS: tc.os, Arch: tc.arch}); err != nil || got != tc.want {
			t.Errorf("%s/%s: %s %v", tc.os, tc.arch, got, err)
		}
	}
	if _, err := asset(probe.Machine{OS: "darwin", Arch: "arm64"}); !errors.Is(err, errNoBuild) {
		t.Errorf("macOS: %v", err)
	}
}

// whisper.cpp's Windows zip keeps the tool under Release/.
func TestEnsureInstallsTheCLIAndFindsItOffline(t *testing.T) {
	m := probe.Machine{OS: runtime.GOOS, Arch: runtime.GOARCH}
	name, err := asset(m)
	if err != nil {
		t.Skip("no whisper.cpp build for this platform")
	}
	body := installtest.Entry{Name: "Release/" + cliName(), Body: "cli", Mode: 0o755}
	data := installtest.Tgz(t, body)
	if strings.HasSuffix(name, ".zip") {
		data = installtest.Zip(t, body)
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dl" {
			http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
			return
		}
		s := sha256.Sum256(data)
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.9.1", "assets": []map[string]any{
			{"name": name, "size": len(data), "digest": "sha256:" + hex.EncodeToString(s[:]), "browser_download_url": srv.URL + "/dl"}}})
	}))
	in := &Installer{Store: &model.Store{Root: t.TempDir()}, Releases: &model.GitHubReleases{Endpoint: srv.URL}, Tag: "v1.9.1"}
	exe, err := in.Ensure(context.Background(), m, nil)
	if err != nil || filepath.Base(exe) != cliName() {
		t.Fatalf("%s, %v", exe, err)
	}
	// Antivirus quarantines the binary, or someone deletes it: the install
	// directory is still there, and a rename onto a directory that exists is
	// refused on Windows even when it is empty. Every launch would try and fail
	// the same way, saying only "Access is denied".
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	if again, err := in.Ensure(context.Background(), m, nil); err != nil || again != exe {
		t.Fatalf("after the binary went missing: %s, %v", again, err)
	}
	srv.Close()
	if again, err := in.Ensure(context.Background(), m, nil); err != nil || again != exe {
		t.Errorf("offline: %s, %v", again, err)
	}
}
