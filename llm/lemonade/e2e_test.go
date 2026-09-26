package lemonade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tradalab/scorix/llm"
	"github.com/tradalab/scorix/llm/model"
	"github.com/tradalab/scorix/llm/probe"
)

// Installs Lemonade v11.9.0 from GitHub, pulls Whisper-Tiny through it and
// transcribes a real recording, gated on SCORIX_LEMONADE_E2E_WAV (speech
// saying "The quick brown fox jumps over the lazy dog"). Set
// SCORIX_LEMONADE_E2E_CHAT to a Lemonade model name to chat with it as well;
// that pulls the model and a llama.cpp build.
func TestAgainstARealLemonade(t *testing.T) {
	wav := os.Getenv("SCORIX_LEMONADE_E2E_WAV")
	if wav == "" {
		t.Skip("set SCORIX_LEMONADE_E2E_WAV to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	root := t.TempDir()
	in := &Installer{Store: &model.Store{Root: filepath.Join(root, "bin")}, Tag: "v11.9.0"}
	exe, err := in.Ensure(ctx, probe.Probe(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("installed %s", exe)
	dir := filepath.Join(root, "state")
	s, err := Start(ctx, exe, ServerOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop(context.Background())
	p, err := s.Provider("lemonade")
	if err != nil {
		t.Fatal(err)
	}

	var steps int
	if err := p.Pull(ctx, "Whisper-Tiny", func(llm.PullProgress) { steps++ }); err != nil {
		t.Fatal(err)
	}
	t.Logf("pull reported %d steps", steps)
	if steps == 0 || steps > 200 {
		t.Errorf("%d progress steps for one file", steps)
	}
	if hub, _ := filepath.Glob(filepath.Join(dir, "huggingface", "models--*")); len(hub) == 0 {
		t.Error("the model did not land in the server's own directory")
	}
	d, err := p.Describe(ctx, "Whisper-Tiny")
	if err != nil || len(d.Capabilities) != 1 || d.Capabilities[0] != llm.Transcribe {
		t.Errorf("describe %+v %v", d, err)
	}

	r := llm.NewRegistry()
	if err := r.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Bind("dictation", llm.Binding{Provider: "lemonade", Model: "Whisper-Tiny", LocalOnly: true}); err != nil {
		t.Fatal(err)
	}
	audio, err := os.ReadFile(wav)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Transcribe(ctx, "dictation", llm.TranscribeRequest{Audio: llm.AudioClip{MIME: "audio/wav", Data: audio}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", res)
	if !strings.Contains(strings.ToLower(res.Text), "quick brown fox") {
		t.Errorf("result = %+v", res)
	}
	if _, err := r.Transcribe(ctx, "dictation", llm.TranscribeRequest{Audio: llm.AudioClip{MIME: "audio/wav", Data: []byte("not audio at all")}}); err == nil {
		t.Error("audio the server cannot read came back as a transcript")
	}

	if name := os.Getenv("SCORIX_LEMONADE_E2E_CHAT"); name != "" {
		if err := p.Pull(ctx, name, nil); err != nil {
			t.Fatal(err)
		}
		reply, err := p.Chat(ctx, llm.ChatRequest{Model: name, Messages: []llm.Message{{Role: llm.User, Content: "Say hello in one word."}}, MaxTokens: 64}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("chat: %q (reasoning %d chars) %+v", reply.Content, len(reply.Reasoning), reply.Usage)
		if reply.Content == "" && reply.Reasoning == "" {
			t.Error("empty reply")
		}
	}

	if err := p.Delete(ctx, "Whisper-Tiny"); err != nil {
		t.Error(err)
	}
	if ms, err := p.Models(ctx); err != nil || len(ms) > 1 {
		t.Errorf("models after delete %+v %v", ms, err)
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
