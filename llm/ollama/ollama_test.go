package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tradalab/scorix/llm"
)

func provider(t *testing.T, url string) *Provider {
	t.Helper()
	p, err := New(Config{ID: "test", BaseURL: url})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func lines(w http.ResponseWriter, ls ...string) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	for _, l := range ls {
		_, _ = io.WriteString(w, l+"\n")
		w.(http.Flusher).Flush()
	}
}

// The lines are the ones Ollama 0.33.3 sent on 2026-09-19, thinking added.
func TestChatSpeaksTheNativeAPI(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		lines(w,
			`{"model":"smollm2:1.7b","message":{"role":"assistant","content":"","thinking":"Hue is a city."},"done":false}`,
			`{"model":"smollm2:1.7b","message":{"role":"assistant","content":"Checking."},"done":false}`,
			`{"model":"smollm2:1.7b","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_cy3xwkra","function":{"index":0,"name":"get_weather","arguments":{"city":"Hue"}}}]},"done":false}`,
			`{"model":"smollm2:1.7b","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","total_duration":4217137100,"prompt_eval_count":272,"prompt_eval_duration":184502000,"eval_count":32,"eval_duration":545876000}`)
	}))
	defer srv.Close()

	temp := 0.2
	var kinds []string
	res, err := provider(t, srv.URL).Chat(context.Background(), llm.ChatRequest{
		Model:       "smollm2:1.7b",
		MaxTokens:   64,
		Temperature: &temp,
		Extra:       map[string]any{"num_ctx": 4096, "think": true},
		Tools:       []llm.Tool{{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Format:      &llm.ResponseFormat{Schema: json.RawMessage(`{"type":"object"}`)},
		Messages: []llm.Message{
			{Role: llm.User, Content: "look", Images: []llm.Image{{MIME: "image/png", Data: []byte("PNG")}}},
			{Role: llm.Assistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "get_weather", Arguments: `{"city":"Hue"}`}}},
			{Role: llm.ToolResult, ToolCallID: "c1", Content: "rain"},
		},
	}, func(c llm.Chunk) error { kinds = append(kinds, string(c.Kind)); return nil })
	if err != nil {
		t.Fatal(err)
	}

	enc := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	if got := enc(body["options"]); got != `{"num_ctx":4096,"num_predict":64,"temperature":0.2}` {
		t.Errorf("options = %s", got)
	}
	if body["think"] != true || enc(body["format"]) != `{"type":"object"}` || body["stream"] != true {
		t.Errorf("top level = %v", body)
	}
	msgs := body["messages"].([]any)
	wantMsgs := []string{
		`{"content":"look","images":["UE5H"],"role":"user"}`,
		// Arguments as an object, as this API wants them.
		`{"content":"","role":"assistant","tool_calls":[{"function":{"arguments":{"city":"Hue"},"name":"get_weather"},"id":"c1"}]}`,
		// Named by the tool, which is how this API matches a result to a call.
		`{"content":"rain","role":"tool","tool_name":"get_weather"}`,
	}
	for i, w := range wantMsgs {
		if got := enc(msgs[i]); got != w {
			t.Errorf("message %d =\n%s\nwant\n%s", i, got, w)
		}
	}

	if strings.Join(kinds, ",") != "reasoning,text,tool_call" {
		t.Errorf("chunks = %v", kinds)
	}
	want := llm.Usage{PromptTokens: 272, CompletionTokens: 32, PromptDuration: 184502 * time.Microsecond, GenerateDuration: 545876 * time.Microsecond}
	if res.Usage != want || res.Reasoning != "Hue is a city." || res.Content != "Checking." || res.FinishReason != "tool_calls" {
		t.Errorf("result = %+v", res)
	}
	if !reflect.DeepEqual(res.ToolCalls, []llm.ToolCall{{ID: "call_cy3xwkra", Name: "get_weather", Arguments: `{"city":"Hue"}`}}) {
		t.Errorf("calls = %+v", res.ToolCalls)
	}
}

// The API cannot make a model call a tool; sending the tools and hoping would
// hand the app prose where it waits for a call.
func TestAToolChoiceTheAPICannotKeepIsRefused(t *testing.T) {
	var got map[string]any
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewDecoder(r.Body).Decode(&got)
		lines(w, `{"message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop"}`)
	}))
	defer srv.Close()
	p := provider(t, srv.URL)
	tools := []llm.Tool{{Name: "x"}}
	for _, choice := range []llm.ToolChoice{llm.ToolRequired, "x"} {
		if _, err := p.Chat(context.Background(), llm.ChatRequest{Tools: tools, ToolChoice: choice}, nil); !errors.Is(err, llm.ErrUnsupported) {
			t.Errorf("%q: %v", choice, err)
		}
	}
	if calls != 0 {
		t.Error("a refused request reached the server")
	}
	if _, err := p.Chat(context.Background(), llm.ChatRequest{Tools: tools, ToolChoice: llm.ToolNone}, nil); err != nil {
		t.Fatal(err)
	}
	if _, sent := got["tools"]; sent {
		t.Error("ToolNone still sent the tools")
	}
}

func TestAStreamThatStopsShortIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lines(w, `{"message":{"role":"assistant","content":"Hel"},"done":false}`)
	}))
	defer srv.Close()
	if _, err := provider(t, srv.URL).Chat(context.Background(), llm.ChatRequest{}, nil); !errors.Is(err, llm.ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
}

// A runner that dies mid-answer says so in the stream; losing that line would
// leave only "the stream ended", which tells the user nothing.
func TestAnErrorInTheStreamKeepsItsWords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lines(w, `{"message":{"role":"assistant","content":"Hel"},"done":false}`, `{"error":"model runner has unexpectedly stopped"}`)
	}))
	defer srv.Close()
	_, err := provider(t, srv.URL).Chat(context.Background(), llm.ChatRequest{}, nil)
	if err == nil || !strings.Contains(err.Error(), "model runner has unexpectedly stopped") {
		t.Errorf("err = %v", err)
	}
}

func TestErrorsReadTheWayTheAppNeeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/chat":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"model 'nope-xyz' not found"}`)
		case "/api/embed":
			lines(w, `{"error":"model 'nope-xyz' not found"}`)
		}
	}))
	defer srv.Close()
	p := provider(t, srv.URL)
	if _, err := p.Chat(context.Background(), llm.ChatRequest{Model: "nope-xyz"}, nil); !errors.Is(err, llm.ErrModelNotFound) || !strings.Contains(err.Error(), "nope-xyz") {
		t.Errorf("chat: %v", err)
	}
	if _, err := p.Embed(context.Background(), llm.EmbedRequest{Input: []string{"a"}}); err == nil {
		t.Error("an error body read as embeddings")
	}
}

// Tool arguments go out as a JSON object on this API, so a string that is not
// JSON has no form to send in.
func TestToolArgumentsThatAreNotJSONAreRefusedBeforeSending(t *testing.T) {
	_, err := provider(t, "http://127.0.0.1:1").Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.Assistant, ToolCalls: []llm.ToolCall{{Name: "x", Arguments: `{"city":`}}}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Errorf("err = %v", err)
	}
}

// /api/show as Ollama 0.33.3 answered for smollm2:1.7b and all-minilm.
func TestDescribeReadsWhatTheRuntimeSays(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Model == "all-minilm" {
			_, _ = io.WriteString(w, `{"capabilities":["embedding"],"details":{"family":"bert"},"model_info":{"general.architecture":"bert","bert.context_length":512}}`)
			return
		}
		_, _ = io.WriteString(w, `{"capabilities":["completion","tools","thinking"],"details":{"family":"llama","parameter_size":"1.7B","quantization_level":"Q8_0"},"model_info":{"general.architecture":"llama","llama.context_length":8192}}`)
	}))
	defer srv.Close()
	p := provider(t, srv.URL)
	d, err := p.Describe(context.Background(), "smollm2:1.7b")
	if err != nil {
		t.Fatal(err)
	}
	want := &llm.ModelDetail{ID: "smollm2:1.7b", ContextLength: 8192, Capabilities: []llm.Capability{llm.Chat, llm.Tools}, Family: "llama", ParameterSize: "1.7B", Quantization: "Q8_0"}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("detail = %+v", d)
	}
	e, _ := p.Describe(context.Background(), "all-minilm")
	if !reflect.DeepEqual(e.Capabilities, []llm.Capability{llm.Embed}) || e.ContextLength != 512 {
		t.Errorf("embedder = %+v", e)
	}
}

// A model list shows size and quantization; without them the user picks
// between names.
func TestModelsCarryWhatTheListShows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"models":[{"name":"smollm2:1.7b","modified_at":"2026-09-03T16:50:02Z","size":1820414927,"digest":"cef4a1e09247","details":{"family":"llama","parameter_size":"1.7B","quantization_level":"Q8_0"}}]}`)
	}))
	defer srv.Close()
	ms, err := provider(t, srv.URL).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := llm.ModelInfo{ID: "smollm2:1.7b", Size: 1820414927, Digest: "cef4a1e09247", Modified: time.Date(2026, 9, 3, 16, 50, 2, 0, time.UTC),
		Family: "llama", ParameterSize: "1.7B", Quantization: "Q8_0"}
	if len(ms) != 1 || !ms[0].Modified.Equal(want.Modified) || ms[0].ID != want.ID || ms[0].Size != want.Size || ms[0].Digest != want.Digest ||
		ms[0].Family != want.Family || ms[0].ParameterSize != want.ParameterSize || ms[0].Quantization != want.Quantization {
		t.Errorf("models = %+v", ms)
	}
}

func TestPullReportsProgressAndSaysWhenItFailed(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []string
		ok   bool
	}{
		{"success", []string{`{"status":"pulling manifest"}`, `{"status":"pulling 797b70c4","digest":"sha256:797b","total":100,"completed":40}`, `{"status":"success"}`}, true},
		{"error line", []string{`{"status":"pulling manifest"}`, `{"error":"pull model manifest: file does not exist"}`}, false},
		{"ended early", []string{`{"status":"pulling manifest"}`}, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { lines(w, tc.body...) }))
		var seen []llm.PullProgress
		err := provider(t, srv.URL).Pull(context.Background(), "m", func(p llm.PullProgress) { seen = append(seen, p) })
		srv.Close()
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v", tc.name, err)
		}
		// The registry's reason, not a generic "ended", is what the user can act on.
		if tc.name == "error line" && (err == nil || !strings.Contains(err.Error(), "file does not exist")) {
			t.Errorf("the pull error lost its words: %v", err)
		}
		if tc.ok && (len(seen) != 3 || seen[1].Done != 40 || seen[1].Total != 100 || seen[1].Digest != "sha256:797b") {
			t.Errorf("%s: progress = %+v", tc.name, seen)
		}
	}
}

func TestTheDriverOpensFromStoredSettings(t *testing.T) {
	p, err := llm.Open("ollama", llm.ProviderConfig{ID: "o", Values: map[string]string{"vision": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if !llm.Has(p, llm.Vision) || llm.Has(p, llm.Tools) || !llm.IsLocal(p) {
		t.Errorf("caps %v local %v", p.Capabilities(), llm.IsLocal(p))
	}
	if _, isManager := p.(llm.ModelManager); !isManager {
		t.Error("an Ollama provider cannot manage models")
	}
}

// Ollama says "stop" for a turn that only called tools, which an app reads
// as a finished answer.
func TestATurnCutShortKeepsItsReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lines(w, `{"message":{"role":"assistant","tool_calls":[{"function":{"name":"sum","arguments":{"a":1}}}]},"done":true,"done_reason":"length"}`)
	}))
	defer srv.Close()
	res, err := provider(t, srv.URL).Chat(context.Background(), llm.ChatRequest{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.FinishReason != "length" || len(res.ToolCalls) != 1 {
		t.Errorf("finish = %q, calls = %+v", res.FinishReason, res.ToolCalls)
	}
}

func TestAToolTurnStillReadsAsOneAcrossDrivers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lines(w, `{"message":{"role":"assistant","tool_calls":[{"function":{"name":"sum","arguments":{"a":1}}}]},"done":true,"done_reason":"stop"}`)
	}))
	defer srv.Close()
	res, err := provider(t, srv.URL).Chat(context.Background(), llm.ChatRequest{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.FinishReason != "tool_calls" {
		t.Errorf("finish = %q", res.FinishReason)
	}
}

// An app that trims and retries needs the sentinel, whichever driver it is on.
func TestAPromptPastTheWindowSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"the request exceeds the available context size"}`)
	}))
	defer srv.Close()
	_, err := provider(t, srv.URL).Chat(context.Background(), llm.ChatRequest{Model: "m"}, nil)
	if !errors.Is(err, llm.ErrContextFull) {
		t.Errorf("err = %v", err)
	}
}

// A build old enough not to list capabilities reported every vision model as
// text-only, so the attach button never appeared.
func TestAnOllamaTooOldToListCapabilitiesIsNotTakenAsANo(t *testing.T) {
	for _, tc := range []struct {
		name   string
		show   string
		vision bool
		ctx    int
	}{
		{"no capabilities, a projector in the families", `{"details":{"family":"llama","families":["llama","clip"]},"model_info":{"general.architecture":"llama","llama.context_length":4096}}`, true, 4096},
		{"no capabilities, projector keys in model_info", `{"details":{"family":"llama"},"model_info":{"clip.vision.block_count":1,"llama.context_length":2048}}`, true, 2048},
		{"no capabilities and nothing multimodal", `{"details":{"family":"llama"},"model_info":{"general.architecture":"llama","llama.context_length":8192}}`, false, 8192},
		{"capabilities without vision is believed", `{"capabilities":["completion"],"details":{"family":"llama","families":["llama","clip"]},"model_info":{"general.architecture":"llama","llama.context_length":4096}}`, false, 4096},
		{"capabilities with vision", `{"capabilities":["completion","vision"],"details":{"family":"llama"},"model_info":{"general.architecture":"llama","llama.context_length":4096}}`, true, 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tc.show)
			}))
			defer srv.Close()
			d, err := provider(t, srv.URL).Describe(context.Background(), "m")
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(d.Capabilities, llm.Vision); got != tc.vision {
				t.Errorf("vision = %v, want %v (%+v)", got, tc.vision, d)
			}
			if d.ContextLength != tc.ctx {
				t.Errorf("context = %d, want %d", d.ContextLength, tc.ctx)
			}
		})
	}
}

// Three ways the fallback was wrong: a text-only model named after a
// multimodal architecture, several context lengths picked at random, and a
// fallback that said what a model could SEE but not that it could answer.
func TestTheFallbackDoesNotInventWhatAModelCanDo(t *testing.T) {
	for _, tc := range []struct {
		name string
		show string
		want []llm.Capability
		ctx  int
	}{
		{
			// gemma3:1b is text-only and reports the same family as the 4b that
			// can see. Saying it can see is what got this heuristic deleted once.
			name: "an architecture shared with a text-only model",
			show: `{"details":{"family":"gemma3","families":["gemma3"]},"model_info":{"general.architecture":"gemma3","gemma3.context_length":32768}}`,
			want: []llm.Capability{llm.Chat}, ctx: 32768,
		},
		{
			name: "a projector family",
			show: `{"details":{"family":"llama","families":["llama","clip"]},"model_info":{"general.architecture":"llama","llama.context_length":4096}}`,
			want: []llm.Capability{llm.Chat, llm.Vision}, ctx: 4096,
		},
		{
			// The projector carries a window of its own - clip's is 77 - so picking one
			// key at random answered differently on every call.
			name: "several context lengths, none under the architecture",
			show: `{"details":{"family":"gemma3"},"model_info":{"clip.context_length":77,"gemma3.vision.context_length":4096,"gemma3.context_length":131072}}`,
			want: []llm.Capability{llm.Chat, llm.Vision}, ctx: 131072,
		},
		{
			name: "several context lengths and no family either",
			show: `{"details":{},"model_info":{"clip.context_length":77,"whatever.context_length":8192}}`,
			want: []llm.Capability{llm.Chat, llm.Vision}, ctx: 8192,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tc.show)
			}))
			defer srv.Close()
			p := provider(t, srv.URL)
			// Asked many times: map order is randomised per iteration, so one
			// call proves nothing about which key was read.
			for range 50 {
				d, err := p.Describe(context.Background(), "m")
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(d.Capabilities, tc.want) {
					t.Fatalf("capabilities = %v, want %v", d.Capabilities, tc.want)
				}
				if d.ContextLength != tc.ctx {
					t.Fatalf("context = %d, want %d", d.ContextLength, tc.ctx)
				}
			}
		})
	}
}

// Every other call here is safe to repeat. This one is not: the delete may have
// landed, and only its answer been lost.
func TestADeleteIsSentOnce(t *testing.T) {
	var tries int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&tries, 1)
		if n == 1 {
			// The delete landed; the answer did not come back.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("no hijacker")
				return
			}
			c, _, err := hj.Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			c.Close()
			return
		}
		// What a retry would find, and what it would be reported as.
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"model 'gone' not found"}`)
	}))
	defer srv.Close()

	p, err := New(Config{ID: "test", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	err = p.Delete(context.Background(), "gone")
	if n := atomic.LoadInt32(&tries); n != 1 {
		t.Errorf("sent %d times", n)
	}
	// It failed, and it failed as "the server did not answer" rather than as
	// "there is no such model".
	if !errors.Is(err, llm.ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
	if errors.Is(err, llm.ErrModelNotFound) {
		t.Errorf("a delete that may have landed was reported as a missing model: %v", err)
	}
}
