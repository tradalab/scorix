package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/tradalab/scorix/llm"
)

func TestOneProviderServesManyGoroutinesWithoutMixingTheirAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		sse(w,
			`data: {"model":"`+req.Model+`","choices":[{"delta":{"content":"`+req.Model+`"}}]}`+"\n\n",
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n",
			`data: {"choices":[],"usage":{"prompt_tokens":`+fmt.Sprint(len(req.Model))+`,"completion_tokens":3}}`+"\n\n",
			"data: [DONE]\n\n")
	}))
	defer srv.Close()
	p := newProvider(t, srv.URL+"/v1", StaticKey("sk-test"))
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			model := fmt.Sprintf("model-%03d", i)
			var onChunk func(llm.Chunk) error
			if i%2 == 1 {
				onChunk = func(llm.Chunk) error { return nil }
			}
			res, err := p.Chat(context.Background(), llm.ChatRequest{Model: model, Messages: []llm.Message{{Role: llm.User, Content: model}}}, onChunk)
			if err != nil {
				t.Error(err)
				return
			}
			if res.Model != model || res.Content != model || res.Usage.PromptTokens != len(model) || res.Usage.CompletionTokens != 3 {
				t.Errorf("goroutine %d got %+v", i, res)
			}
		}()
	}
	wg.Wait()
}

func TestEveryStreamAsksForUsageSoACallIsNeverBilledAsZero(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		sse(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n", "data: [DONE]\n\n")
	}))
	defer srv.Close()
	p := newProvider(t, srv.URL+"/v1", StaticKey("sk-test"))
	for _, onChunk := range []func(llm.Chunk) error{nil, func(llm.Chunk) error { return nil }} {
		if _, err := p.Chat(context.Background(), llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.User, Content: "x"}}}, onChunk); err != nil {
			t.Fatal(err)
		}
		opts, _ := body["stream_options"].(map[string]any)
		if body["stream"] != true || opts["include_usage"] != true {
			t.Fatalf("body = %v, a stream without include_usage carries no token counts", body)
		}
	}
}
