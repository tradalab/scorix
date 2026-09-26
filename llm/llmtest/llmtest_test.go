package llmtest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tradalab/scorix/llm"
)

func TestProviderStreamsItsScriptThroughTheRegistry(t *testing.T) {
	p := New("fake", llm.Chat, llm.Tools)
	p.OnThisMachine = true
	call := llm.ToolCall{ID: "c1", Name: "lookup", Arguments: `{"q":"x"}`}
	p.Reply(Reply{Text: "hello there world", Reasoning: "hmm"}, Reply{ToolCalls: []llm.ToolCall{call}})

	r := llm.NewRegistry()
	if err := r.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Bind("chat", llm.Binding{Provider: "fake", Model: "m", LocalOnly: true}); err != nil {
		t.Fatalf("a provider on this machine was refused a local-only slot: %v", err)
	}
	var chunks []string
	res, err := r.Chat(context.Background(), "chat", llm.ChatRequest{}, func(c llm.Chunk) error {
		chunks = append(chunks, string(c.Kind)+":"+c.Text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(chunks, "|") != "reasoning:hmm|text:hello |text:there |text:world" || res.Content != "hello there world" || res.FinishReason != "stop" {
		t.Errorf("chunks %q, result %+v", chunks, res)
	}
	res, err = r.Chat(context.Background(), "chat", llm.ChatRequest{Tools: []llm.Tool{{Name: "lookup"}}}, nil)
	if err != nil || !reflect.DeepEqual(res.ToolCalls, []llm.ToolCall{call}) || res.FinishReason != "tool_calls" {
		t.Errorf("tool reply = %+v, %v", res, err)
	}
	if reqs := p.Requests(); len(reqs) != 2 || reqs[0].Model != "m" {
		t.Errorf("requests = %+v", reqs)
	}
	if _, err := r.Chat(context.Background(), "chat", llm.ChatRequest{}, nil); err == nil || !strings.Contains(err.Error(), "no reply queued for request 3") {
		t.Errorf("asking past the script: %v", err)
	}
}

func TestProviderStopsLikeAServer(t *testing.T) {
	stop := errors.New("enough")
	p := New("fake").Reply(Reply{Text: "a b c d"}, Reply{Text: "x"}, Reply{Err: llm.ErrRateLimited})
	n := 0
	_, err := p.Chat(context.Background(), llm.ChatRequest{}, func(llm.Chunk) error {
		if n++; n == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || n != 2 {
		t.Errorf("onChunk error did not stop the stream: %v after %d", err, n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Chat(ctx, llm.ChatRequest{}, func(llm.Chunk) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled ctx: %v", err)
	}
	if _, err := p.Chat(context.Background(), llm.ChatRequest{}, nil); !errors.Is(err, llm.ErrRateLimited) {
		t.Errorf("scripted error: %v", err)
	}
}

func TestEmbedIsStableAndTellsTextsApart(t *testing.T) {
	p := New("fake")
	a, _ := p.Embed(context.Background(), llm.EmbedRequest{Input: []string{"cat", "dog", "cat"}})
	b, _ := p.Embed(context.Background(), llm.EmbedRequest{Input: []string{"cat"}})
	if !reflect.DeepEqual(a.Vectors[0], a.Vectors[2]) || !reflect.DeepEqual(a.Vectors[0], b.Vectors[0]) || reflect.DeepEqual(a.Vectors[0], a.Vectors[1]) || len(a.Vectors[0]) != 8 {
		t.Errorf("vectors = %v / %v", a.Vectors, b.Vectors)
	}
}

func TestACassettePlaysBackWhatTheServerSent(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: answer "+string(rune('0'+calls))+"\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "c.json")

	post := func(c *http.Client, body string) (int, string, error) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer sk-secret-123")
		resp, err := c.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), nil
	}

	rec := Record(path, nil)
	_, first, _ := post(rec.Client(), `{"model":"m","stream":true}`)
	_, second, _ := post(rec.Client(), `{"model":"m","stream":true}`)
	if err := rec.Save(); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); strings.Contains(string(raw), "sk-secret") {
		t.Fatal("the API key was written into the cassette")
	}

	play, err := Replay(path)
	if err != nil {
		t.Fatal(err)
	}
	// Same request with its keys in another order: still the same request.
	status, got1, err := post(play.Client(), `{"stream": true, "model": "m"}`)
	if err != nil || status != 200 || got1 != first {
		t.Errorf("replay 1 = %d %q %v, want %q", status, got1, err, first)
	}
	if _, got2, _ := post(play.Client(), `{"model":"m","stream":true}`); got2 != second {
		t.Errorf("replay 2 = %q, want the second recording %q", got2, second)
	}
	if _, _, err := post(play.Client(), `{"model":"m","stream":true}`); err == nil {
		t.Error("a third identical request replayed from nothing")
	}
	if _, _, err := post(play.Client(), `{"model":"other"}`); err == nil || !strings.Contains(err.Error(), "changed since recording") {
		t.Errorf("a request that was never recorded: %v", err)
	}
	if calls != 2 {
		t.Errorf("replay reached the server: %d calls", calls)
	}
}

// A client that stops a stream closes it half read; the cassette has to replay
// that same half, not wait for a whole body that never came.
func TestACassetteKeepsAStreamCutShort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "part one|")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "c.json")
	rec := Record(path, nil)
	resp, err := rec.Client().Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("part one|"))
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := rec.Save(); err != nil {
		t.Fatal(err)
	}
	play, _ := Replay(path)
	resp, err = play.Client().Get("http://anywhere/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != "part one|" {
		t.Errorf("replayed %q", got)
	}
}
