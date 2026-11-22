package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
		if r.Header.Get("x-api-key") != "sk-test" {
			http.Error(w, "key", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(
			`{"type":"message_start","message":{"model":"`+req.Model+`","usage":{"input_tokens":`+fmt.Sprint(len(req.Model))+`,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+req.Model+`"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
			`{"type":"message_stop"}`,
		))
	}))
	defer srv.Close()
	p, err := New(Config{ID: "claude", BaseURL: srv.URL + "/v1", Key: StaticKey("sk-test"), Retry: llm.RetryPolicy{Attempts: 1}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			model := fmt.Sprintf("model-%03d", i)
			var chunks []string
			var onChunk func(llm.Chunk) error
			if i%2 == 1 {
				onChunk = func(c llm.Chunk) error { chunks = append(chunks, c.Text); return nil }
			}
			res, err := p.Chat(context.Background(), llm.ChatRequest{Model: model, MaxTokens: 10, Messages: []llm.Message{{Role: llm.User, Content: model}}}, onChunk)
			if err != nil {
				t.Error(err)
				return
			}
			if res.Model != model || res.Content != model || res.Usage.PromptTokens != len(model) || res.Usage.CompletionTokens != 3 {
				t.Errorf("goroutine %d got %+v", i, res)
			}
			if onChunk != nil && (len(chunks) != 1 || chunks[0] != model) {
				t.Errorf("goroutine %d streamed %q", i, chunks)
			}
		}()
	}
	wg.Wait()
}
