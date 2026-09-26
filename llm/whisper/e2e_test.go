package whisper

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tradalab/scorix/llm"
	"github.com/tradalab/scorix/llm/model"
	"github.com/tradalab/scorix/llm/probe"
)

// Installs whisper-cli v1.9.1 from GitHub and transcribes a real recording,
// gated on SCORIX_WHISPER_E2E_MODEL (a ggml model) and SCORIX_WHISPER_E2E_WAV
// (speech saying "The quick brown fox jumps over the lazy dog").
func TestAgainstARealWhisper(t *testing.T) {
	modelPath, wav := os.Getenv("SCORIX_WHISPER_E2E_MODEL"), os.Getenv("SCORIX_WHISPER_E2E_WAV")
	if modelPath == "" || wav == "" {
		t.Skip("set SCORIX_WHISPER_E2E_MODEL and SCORIX_WHISPER_E2E_WAV to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	in := &Installer{Store: &model.Store{Root: t.TempDir()}, Tag: "v1.9.1"}
	exe, err := in.Ensure(ctx, probe.Probe(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("installed %s", exe)
	audio, err := os.ReadFile(wav)
	if err != nil {
		t.Fatal(err)
	}
	tr := New("whisper", exe)
	res, err := tr.Transcribe(ctx, llm.TranscribeRequest{Model: modelPath, Audio: llm.AudioClip{MIME: "audio/wav", Data: audio}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", res)
	if !strings.Contains(strings.ToLower(res.Text), "quick brown fox") || res.Language != "en" {
		t.Errorf("result = %+v", res)
	}
	if _, err := tr.Transcribe(ctx, llm.TranscribeRequest{Model: modelPath, Audio: llm.AudioClip{MIME: "audio/wav", Data: []byte("not audio at all")}}); err == nil {
		t.Error("audio whisper-cli cannot read came back as a transcript")
	}
}
