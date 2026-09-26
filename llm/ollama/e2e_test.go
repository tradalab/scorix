package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/tradalab/scorix/llm"
	"github.com/tradalab/scorix/llm/llmtest"
)

// Against a real Ollama, gated on SCORIX_OLLAMA_E2E_URL with SCORIX_OLLAMA_E2E_CHAT
// (a model that calls tools) and SCORIX_OLLAMA_E2E_EMBED pulled there.
// SCORIX_OLLAMA_E2E_RECORD=<file> keeps the traffic for TestReplayedOllama. The
// delete case works on a copy it makes, never on a model the user has.
func TestAgainstARealOllama(t *testing.T) {
	base, chat, embed := os.Getenv("SCORIX_OLLAMA_E2E_URL"), os.Getenv("SCORIX_OLLAMA_E2E_CHAT"), os.Getenv("SCORIX_OLLAMA_E2E_EMBED")
	if base == "" || chat == "" || embed == "" {
		t.Skip("set SCORIX_OLLAMA_E2E_URL, _CHAT and _EMBED to run")
	}
	hc := http.DefaultClient
	var rec *llmtest.Cassette
	if path := os.Getenv("SCORIX_OLLAMA_E2E_RECORD"); path != "" {
		rec = llmtest.Record(path, nil)
		hc = rec.Client()
	}
	exercise(t, base, hc, chat, embed)
	if rec != nil {
		if err := rec.Save(); err != nil {
			t.Fatal(err)
		}
	}
}

// Recorded from Ollama 0.33.3 by TestAgainstARealOllama.
func TestReplayedOllama(t *testing.T) {
	c, err := llmtest.Replay(filepath.Join("testdata", "ollama-0.33.3.json"))
	if err != nil {
		t.Fatal(err)
	}
	exercise(t, "http://cassette", c.Client(), "smollm2:1.7b", "all-minilm")
}

func exercise(t *testing.T, base string, hc *http.Client, chat, embed string) {
	p, err := New(Config{ID: "test", BaseURL: base, HTTPClient: hc})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	t.Run("version and models", func(t *testing.T) {
		v, err := p.Version(ctx)
		if err != nil || v == "" {
			t.Fatalf("version %q, %v", v, err)
		}
		ms, err := p.Models(ctx)
		if err != nil || !slices.ContainsFunc(ms, func(m llm.ModelInfo) bool { return m.ID == chat }) {
			t.Errorf("%s not in %v (%v)", chat, ms, err)
		}
	})

	t.Run("describe", func(t *testing.T) {
		d, err := p.Describe(ctx, chat)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%+v", d)
		if !slices.Contains(d.Capabilities, llm.Chat) || !slices.Contains(d.Capabilities, llm.Tools) || d.ContextLength == 0 {
			t.Errorf("detail = %+v", d)
		}
		e, err := p.Describe(ctx, embed)
		if err != nil || !slices.Contains(e.Capabilities, llm.Embed) || slices.Contains(e.Capabilities, llm.Chat) {
			t.Errorf("embedder = %+v, %v", e, err)
		}
	})

	t.Run("chat streams with server timings", func(t *testing.T) {
		n := 0
		res, err := p.Chat(ctx, llm.ChatRequest{Model: chat, MaxTokens: 30, Extra: map[string]any{"seed": 1},
			Messages: []llm.Message{{Role: llm.User, Content: "Count from one to five."}}}, func(llm.Chunk) error { n++; return nil })
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%d chunks, %s, %+v: %q", n, res.FinishReason, res.Usage, res.Content)
		if n < 2 || res.Content == "" || res.Usage.GenerateDuration == 0 || res.Usage.CompletionTokens == 0 {
			t.Errorf("result = %+v", res)
		}
	})

	t.Run("stopping mid-answer", func(t *testing.T) {
		stop := errors.New("enough")
		n := 0
		_, err := p.Chat(ctx, llm.ChatRequest{Model: chat, Messages: []llm.Message{{Role: llm.User, Content: "Write a long story."}}},
			func(llm.Chunk) error {
				if n++; n == 3 {
					return stop
				}
				return nil
			})
		if !errors.Is(err, stop) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("tool call round trip", func(t *testing.T) {
		type where struct {
			City string `json:"city" desc:"City to get the weather for"`
		}
		tool, _ := llm.ToolFor[where]("get_weather", "Current weather in a city")
		ask := llm.Message{Role: llm.User, Content: "What is the weather in Hue right now?"}
		res, err := p.Chat(ctx, llm.ChatRequest{Model: chat, Tools: []llm.Tool{tool}, Messages: []llm.Message{ask}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.ToolCalls) == 0 || res.FinishReason != "tool_calls" {
			t.Fatalf("no call: %+v", res)
		}
		var w where
		if err := res.ToolCalls[0].Decode(&w); err != nil || w.City == "" {
			t.Errorf("arguments %q: %v", res.ToolCalls[0].Arguments, err)
		}
		call := res.ToolCalls[0]
		res, err = p.Chat(ctx, llm.ChatRequest{Model: chat, Tools: []llm.Tool{tool}, Messages: []llm.Message{
			ask, {Role: llm.Assistant, ToolCalls: []llm.ToolCall{call}},
			{Role: llm.ToolResult, ToolCallID: call.ID, Content: `{"sky":"rain","celsius":24}`},
		}}, nil)
		if err != nil {
			t.Fatalf("the result went back and the server refused it: %v", err)
		}
		t.Logf("after the tool: %q %+v", res.Content, res.ToolCalls)
	})

	t.Run("structured output", func(t *testing.T) {
		type person struct {
			Name string `json:"name"`
			Age  int    `json:"age"`
		}
		f, _ := llm.FormatFor[person]("person")
		res, err := p.Chat(ctx, llm.ChatRequest{Model: chat, Format: f, Messages: []llm.Message{{Role: llm.User, Content: "Extract: Lan is 30 years old."}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var got person
		if err := json.Unmarshal([]byte(res.Content), &got); err != nil || got.Name == "" {
			t.Errorf("reply %q -> %+v, %v", res.Content, got, err)
		}
	})

	t.Run("embed", func(t *testing.T) {
		res, err := p.Embed(ctx, llm.EmbedRequest{Model: embed, Input: []string{"a red apple", "a green pear"}})
		if err != nil || len(res.Vectors) != 2 || len(res.Vectors[0]) == 0 {
			t.Errorf("%+v, %v", res, err)
		}
	})

	t.Run("a model that is not there", func(t *testing.T) {
		_, err := p.Chat(ctx, llm.ChatRequest{Model: "no-such-model-xyz", Messages: []llm.Message{{Role: llm.User, Content: "hi"}}}, nil)
		if !errors.Is(err, llm.ErrModelNotFound) {
			t.Errorf("err = %v", err)
		}
		if _, err := p.Describe(ctx, "no-such-model-xyz"); !errors.Is(err, llm.ErrModelNotFound) {
			t.Errorf("describe: %v", err)
		}
	})

	t.Run("pull what is already there", func(t *testing.T) {
		var steps []llm.PullProgress
		if err := p.Pull(ctx, embed, func(pp llm.PullProgress) { steps = append(steps, pp) }); err != nil {
			t.Fatal(err)
		}
		if len(steps) == 0 || steps[len(steps)-1].Status != "success" {
			t.Errorf("steps = %+v", steps)
		}
	})

	t.Run("delete a copy", func(t *testing.T) {
		copied := "scorix-e2e-copy"
		body, _ := json.Marshal(map[string]string{"source": embed, "destination": copied})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/copy", bytes.NewReader(body))
		resp, err := hc.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("copy: %v %v", resp, err)
		}
		resp.Body.Close()
		if err := p.Delete(ctx, copied); err != nil {
			t.Fatal(err)
		}
		if err := p.Delete(ctx, copied); !errors.Is(err, llm.ErrModelNotFound) {
			t.Errorf("deleting it twice: %v", err)
		}
	})
}
