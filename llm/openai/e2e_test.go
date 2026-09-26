package openai

import (
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

// What a server can be asked differs: llama-server serves whatever model it
// loaded under any name, and a GGUF whose chat template has no tools never
// calls one.
type server struct {
	chat, embed   string // no embed: skipped
	tools         bool
	refusesModels bool // answers an unknown model name with 404
}

// Against a real server, which the fakes cannot stand in for: they only prove
// the parser agrees with what I think the wire looks like. Gated because it
// needs one: SCORIX_LLM_E2E_URL and SCORIX_LLM_E2E_CHAT, plus _EMBED, _TOOLS=1
// and _STRICT=1 for what that server supports. _RECORD=<file> keeps the traffic
// as a cassette for TestReplayedServers.
func TestAgainstARealServer(t *testing.T) {
	base, chat := os.Getenv("SCORIX_LLM_E2E_URL"), os.Getenv("SCORIX_LLM_E2E_CHAT")
	if base == "" || chat == "" {
		t.Skip("set SCORIX_LLM_E2E_URL and SCORIX_LLM_E2E_CHAT to run")
	}
	srv := server{chat: chat, embed: os.Getenv("SCORIX_LLM_E2E_EMBED"), tools: os.Getenv("SCORIX_LLM_E2E_TOOLS") == "1", refusesModels: os.Getenv("SCORIX_LLM_E2E_STRICT") == "1"}
	var hc *http.Client
	var rec *llmtest.Cassette
	if path := os.Getenv("SCORIX_LLM_E2E_RECORD"); path != "" {
		rec = llmtest.Record(path, nil)
		hc = rec.Client()
	}
	p, err := New(Config{ID: "test", BaseURL: base, HTTPClient: hc})
	if err != nil {
		t.Fatal(err)
	}
	exercise(t, p, srv)
	if rec != nil {
		if err := rec.Save(); err != nil {
			t.Fatal(err)
		}
	}
}

// Recorded from real servers by TestAgainstARealServer, so the parser stays
// checked against what they send long after those servers are gone.
func TestReplayedServers(t *testing.T) {
	for file, srv := range map[string]server{
		"ollama-0.33.3.json":      {chat: "smollm2:1.7b", embed: "all-minilm", tools: true, refusesModels: true},
		"llama-server-b9934.json": {chat: "smollm2:1.7b"},
	} {
		t.Run(file, func(t *testing.T) {
			c, err := llmtest.Replay(filepath.Join("testdata", file))
			if err != nil {
				t.Fatal(err)
			}
			p, err := New(Config{ID: "test", BaseURL: "http://cassette/v1", HTTPClient: c.Client()})
			if err != nil {
				t.Fatal(err)
			}
			exercise(t, p, srv)
		})
	}
}

func exercise(t *testing.T, p *Provider, srv server) {
	chatModel := srv.chat
	// The first request loads the model from disk.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	t.Run("models", func(t *testing.T) {
		ms, err := p.Models(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(ms))
		for _, m := range ms {
			ids = append(ids, m.ID)
		}
		if !slices.ContainsFunc(ids, func(id string) bool { return id == chatModel || id == chatModel+":latest" }) {
			t.Errorf("%s not in %v", chatModel, ids)
		}
	})

	t.Run("chat streams", func(t *testing.T) {
		var deltas int
		res, err := p.Chat(ctx, llm.ChatRequest{
			Model:     chatModel,
			MaxTokens: 40,
			Messages:  []llm.Message{{Role: llm.User, Content: "Count from one to five."}},
		}, func(llm.Chunk) error { deltas++; return nil })
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%d deltas, finish=%s, usage=%+v: %q", deltas, res.FinishReason, res.Usage, res.Content)
		if deltas < 2 || res.Content == "" || res.FinishReason == "" {
			t.Errorf("not a streamed, finished answer: %d deltas, %+v", deltas, res)
		}
	})

	t.Run("stopping mid-answer", func(t *testing.T) {
		stop := errors.New("enough")
		n := 0
		start := time.Now()
		_, err := p.Chat(ctx, llm.ChatRequest{
			Model:    chatModel,
			Messages: []llm.Message{{Role: llm.User, Content: "Write a long story about the sea."}},
		}, func(llm.Chunk) error {
			if n++; n == 3 {
				return stop
			}
			return nil
		})
		t.Logf("stopped after %d deltas in %s", n, time.Since(start).Round(time.Millisecond))
		if !errors.Is(err, stop) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("embed", func(t *testing.T) {
		if srv.embed == "" {
			t.Skip("no embedding model on this server")
		}
		res, err := p.Embed(ctx, llm.EmbedRequest{Model: srv.embed, Input: []string{"a red apple", "a green pear"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%d vectors of %d dims", len(res.Vectors), len(res.Vectors[0]))
		if len(res.Vectors) != 2 || len(res.Vectors[0]) == 0 || len(res.Vectors[0]) != len(res.Vectors[1]) {
			t.Errorf("vectors = %d, dims %d/%d", len(res.Vectors), len(res.Vectors[0]), len(res.Vectors[1]))
		}
	})

	// Whether the server enforces the schema is the server's business; what this
	// checks is that the request is one it accepts and the reply decodes.
	t.Run("structured output", func(t *testing.T) {
		type person struct {
			Name string `json:"name"`
			Age  int    `json:"age"`
		}
		format, err := llm.FormatFor[person]("person")
		if err != nil {
			t.Fatal(err)
		}
		res, err := p.Chat(ctx, llm.ChatRequest{
			Model:    chatModel,
			Format:   format,
			Messages: []llm.Message{{Role: llm.User, Content: "Extract the person: Lan is 30 years old."}},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var got person
		err = json.Unmarshal([]byte(res.Content), &got)
		t.Logf("reply %q -> %+v (%v)", res.Content, got, err)
		if err != nil || got.Name == "" {
			t.Errorf("reply does not decode into the schema's type")
		}
	})

	t.Run("tool call", func(t *testing.T) {
		if !srv.tools {
			t.Skip("this server's model does not call tools")
		}
		type where struct {
			City string `json:"city" desc:"City to get the weather for"`
		}
		tool, err := llm.ToolFor[where]("get_weather", "Current weather in a city")
		if err != nil {
			t.Fatal(err)
		}
		var streamed int
		res, err := p.Chat(ctx, llm.ChatRequest{
			Model:      chatModel,
			Tools:      []llm.Tool{tool},
			ToolChoice: llm.ToolRequired,
			Messages:   []llm.Message{{Role: llm.User, Content: "What is the weather in Hue right now?"}},
		}, func(c llm.Chunk) error {
			if c.Kind == llm.ChunkToolCall {
				streamed++
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("finish=%s calls=%+v content=%q", res.FinishReason, res.ToolCalls, res.Content)
		if len(res.ToolCalls) == 0 || res.ToolCalls[0].Name != "get_weather" || streamed != len(res.ToolCalls) {
			t.Fatalf("no get_weather call (streamed %d)", streamed)
		}
		var w where
		if err := res.ToolCalls[0].Decode(&w); err != nil || w.City == "" {
			t.Errorf("arguments %q: %v", res.ToolCalls[0].Arguments, err)
		}

		// The result goes back and the model answers from it.
		call := res.ToolCalls[0]
		res, err = p.Chat(ctx, llm.ChatRequest{
			Model: chatModel,
			Tools: []llm.Tool{tool},
			Messages: []llm.Message{
				{Role: llm.User, Content: "What is the weather in Hue right now?"},
				{Role: llm.Assistant, ToolCalls: []llm.ToolCall{call}},
				{Role: llm.ToolResult, ToolCallID: call.ID, Content: `{"city":"Hue","sky":"rain","celsius":24}`},
			},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		// What the model does with the result is the model's business - a small
		// one calls the tool again as often as it answers. That the server took
		// the conversation back without an error is the check.
		t.Logf("after the tool: %q, calls %+v", res.Content, res.ToolCalls)
		if res.Content == "" && len(res.ToolCalls) == 0 {
			t.Error("neither an answer nor a call after the tool result")
		}
	})

	// The 404 mapping is a guess about the server's wording; this is the check.
	t.Run("a model that is not there", func(t *testing.T) {
		if !srv.refusesModels {
			t.Skip("this server answers any model name with the one it loaded")
		}
		_, err := p.Chat(ctx, llm.ChatRequest{Model: "no-such-model-xyz", Messages: []llm.Message{{Role: llm.User, Content: "hi"}}}, nil)
		t.Logf("server said: %v", err)
		if !errors.Is(err, llm.ErrModelNotFound) {
			t.Errorf("err = %v, want ErrModelNotFound", err)
		}
	})
}
