package llamacpp

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/tradalab/scorix/llm"
	"github.com/tradalab/scorix/llm/model"
	"github.com/tradalab/scorix/llm/probe"
)

// The whole path against GitHub and a real model, gated on
// SCORIX_LLAMACPP_E2E_MODEL=<a .gguf>: probe this machine, choose and install
// the b9934 build (about 30 MB for Vulkan), start it, chat, stop.
func TestAgainstARealRelease(t *testing.T) {
	gguf := os.Getenv("SCORIX_LLAMACPP_E2E_MODEL")
	if gguf == "" {
		t.Skip("set SCORIX_LLAMACPP_E2E_MODEL to a .gguf to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	m := probe.Probe()
	t.Logf("probe: %d GPU(s), skipped %v", len(m.GPUs), m.Skipped)

	in := &Installer{Store: &model.Store{Root: t.TempDir()}, Tag: "b9934"}
	start := time.Now()
	exe, err := in.Ensure(ctx, m, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("installed %s in %s", exe, time.Since(start).Round(time.Millisecond))

	srv, err := Start(ctx, exe, gguf, ServerOptions{Context: 2048})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer scancel()
		if err := srv.Stop(sctx); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	p, err := srv.Provider("managed", llm.Chat)
	if err != nil {
		t.Fatal(err)
	}
	if !llm.IsLocal(p) {
		t.Error("a server this process started is not counted local")
	}
	res, err := p.Chat(ctx, llm.ChatRequest{MaxTokens: 24, Messages: []llm.Message{{Role: llm.User, Content: "Say hello."}}}, nil)
	if err != nil {
		t.Fatalf("chat: %v\n%v", err, srv.Logs())
	}
	t.Logf("reply %q, %+v", res.Content, res.Usage)
	if res.Content == "" || res.Usage.GenerateDuration == 0 {
		t.Errorf("no reply with timings: %+v", res)
	}
}
