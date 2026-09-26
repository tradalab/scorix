package anthropic

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tradalab/scorix/llm"
	"github.com/tradalab/scorix/llm/llmtest"
)

// Against the real API, which the fakes cannot stand in for: they only prove
// the parser agrees with the documentation. The turn that matters is the
// second one: a model with thinking on by default refuses a tool result whose
// signed thinking did not come back unchanged. Gated on SCORIX_ANTHROPIC_E2E_KEY
// and SCORIX_ANTHROPIC_E2E_MODEL (claude-sonnet-5 thinks by default and costs
// cents); _RECORD=<file> keeps the traffic, without the key, as a cassette.
func TestAgainstAnthropic(t *testing.T) {
	key, model := os.Getenv("SCORIX_ANTHROPIC_E2E_KEY"), os.Getenv("SCORIX_ANTHROPIC_E2E_MODEL")
	if key == "" || model == "" {
		t.Skip("set SCORIX_ANTHROPIC_E2E_KEY and SCORIX_ANTHROPIC_E2E_MODEL to run")
	}
	var hc *http.Client
	var rec *llmtest.Cassette
	if path := os.Getenv("SCORIX_ANTHROPIC_E2E_RECORD"); path != "" {
		rec = llmtest.Record(path, nil)
		hc = rec.Client()
	}
	p, err := New(Config{ID: "claude", Key: StaticKey(key), HTTPClient: hc})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	models, err := p.Models(ctx)
	if err != nil || len(models) == 0 {
		t.Fatalf("models %v, %v", models, err)
	}
	d, err := p.Describe(ctx, model)
	if err != nil || d.ContextLength == 0 {
		t.Fatalf("describe %+v, %v", d, err)
	}
	t.Logf("%s: context %d, output %d, %v", d.ID, d.ContextLength, d.MaxOutputTokens, d.Capabilities)

	tool, err := llm.ToolFor[struct {
		City string `json:"city"`
	}]("local_time", "The current local time in a city")
	if err != nil {
		t.Fatal(err)
	}
	req := llm.ChatRequest{Model: model, Tools: []llm.Tool{tool}, Messages: []llm.Message{
		{Role: llm.System, Content: "Answer in one short sentence."},
		{Role: llm.User, Content: "What time is it in Tokyo right now? Use the tool."},
	}}
	n, err := p.CountTokens(ctx, req)
	if err != nil || n == 0 {
		t.Fatalf("count %d, %v", n, err)
	}
	var chunks int
	first, err := p.Chat(ctx, req, func(llm.Chunk) error { chunks++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("first turn: %s, %d chunks, %d calls, %+v, counted %d", first.FinishReason, chunks, len(first.ToolCalls), first.Usage, n)
	if len(first.ToolCalls) == 0 || first.Replay == nil {
		t.Fatalf("no tool call: %q", first.Content)
	}
	req.Messages = append(req.Messages, first.Message())
	for _, c := range first.ToolCalls {
		req.Messages = append(req.Messages, llm.Message{Role: llm.ToolResult, ToolCallID: c.ID, Content: "10:42 JST"})
	}
	second, err := p.Chat(ctx, req, nil)
	if err != nil {
		t.Fatalf("the tool result was refused: %v", err)
	}
	t.Logf("second turn: %s %q %+v", second.FinishReason, second.Content, second.Usage)
	if !strings.Contains(second.Content, "10:42") {
		t.Errorf("the answer ignores the tool result: %q", second.Content)
	}
	if rec != nil {
		if err := rec.Save(); err != nil {
			t.Fatal(err)
		}
	}
}
