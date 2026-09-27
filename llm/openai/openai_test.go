package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tradalab/scorix/llm"
)

func newProvider(t *testing.T, base string, key KeySource) *Provider {
	t.Helper()
	// One try: every test asserting an error mapping would otherwise sit
	// through the real backoff. The retry has its own tests.
	p, err := New(Config{ID: "test", BaseURL: base, Key: key, Retry: llm.RetryPolicy{Attempts: 1}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func sse(w http.ResponseWriter, lines ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, l := range lines {
		_, _ = io.WriteString(w, l)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func TestChatStreamsDeltasAndReturnsTheWholeAnswer(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		sse(w,
			`data: {"model":"m-served","choices":[{"delta":{"content":"Hel"}}]}`+"\n\n",
			`data: {"choices":[{"delta":{"content":"lo"}}]}`+"\n\n",
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n",
			`data: {"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":5}}}`+"\n\n",
			"data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := newProvider(t, srv.URL+"/v1", StaticKey("sk-test"))
	var deltas []string
	res, err := p.Chat(context.Background(), llm.ChatRequest{
		Model:    "m",
		Messages: []llm.Message{{Role: llm.User, Content: "hi"}},
	}, func(c llm.Chunk) error { deltas = append(deltas, c.Text); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "|") != "Hel|lo" {
		t.Errorf("deltas = %q", deltas)
	}
	if res.Content != "Hello" || res.FinishReason != "stop" || res.Model != "m-served" {
		t.Errorf("result = %+v", res)
	}
	if res.Usage.PromptTokens != 7 || res.Usage.CompletionTokens != 2 || res.Usage.CacheReadTokens != 5 {
		t.Errorf("usage = %+v: cached input is part of prompt_tokens, billed apart", res.Usage)
	}
	if auth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", auth)
	}
	if got["stream"] != true || got["model"] != "m" {
		t.Errorf("request body = %v", got)
	}
}

// Servers pad the stream with comments, event names and CRLF line ends.
func TestChatToleratesTheSSEAServerIsAllowedToSend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			": keep-alive\r\n\r\n",
			"event: message\r\n",
			`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\r\n\r\n",
			"data: [DONE]\r\n")
	}))
	defer srv.Close()
	res, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{}, nil)
	if err != nil || res.Content != "ok" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

// A connection that closes cleanly mid-answer reads like the end of one.
func TestChatRefusesAStreamThatStopsBeforeTheAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `data: {"choices":[{"delta":{"content":"half an ans"}}]}`+"\n\n")
	}))
	defer srv.Close()
	res, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{}, nil)
	if !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("a truncated stream came back as res=%+v err=%v", res, err)
	}
}

func TestChatSurfacesAnErrorSentInsideTheStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `data: {"error":{"message":"model \"x\" not found, try pulling it first"}}`+"\n\n")
	}))
	defer srv.Close()
	_, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{Model: "x"}, nil)
	if !errors.Is(err, llm.ErrModelNotFound) {
		t.Errorf("err = %v, want ErrModelNotFound", err)
	}
}

// The server must see the client go away, or it keeps generating for nobody.
func TestAnOnChunkErrorStopsTheGeneration(t *testing.T) {
	gone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 1000; i++ {
			sse(w, fmt.Sprintf(`data: {"choices":[{"delta":{"content":"t%d "}}]}`+"\n\n", i))
			select {
			case <-r.Context().Done():
				close(gone)
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}))
	defer srv.Close()

	stop := errors.New("client left")
	n := 0
	_, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{}, func(llm.Chunk) error {
		n++
		if n == 3 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v, want the onChunk error", err)
	}
	select {
	case <-gone:
	case <-time.After(3 * time.Second):
		t.Error("the server never saw the client leave, so it would keep generating")
	}
}

func TestCancellingTheContextEndsAStreamPromptly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `data: {"choices":[{"delta":{"content":"first"}}]}`+"\n\n")
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	_, err := newProvider(t, srv.URL, nil).Chat(ctx, llm.ChatRequest{}, func(llm.Chunk) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	// A guard against the cancel never landing, not a stopwatch on it.
	if took := time.Since(start); took > 30*time.Second {
		t.Errorf("cancel took %s to land", took)
	}
}

func TestHTTPFailuresMapOntoWhatAnAppCanTellItsUser(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{
		{401, `{"error":{"message":"Incorrect API key provided"}}`, llm.ErrUnauthorized},
		{403, `{"error":{"message":"forbidden"}}`, llm.ErrUnauthorized},
		{429, `{"error":{"message":"slow down"}}`, llm.ErrRateLimited},
		{503, `upstream down`, llm.ErrUnavailable},
		{404, `{"error":{"message":"The model 'gpt-9' does not exist"}}`, llm.ErrModelNotFound},
		{404, `{"error":"model \"x\" not found"}`, llm.ErrModelNotFound},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.body)
		}))
		_, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{}, nil)
		srv.Close()
		if !errors.Is(err, tc.want) {
			t.Errorf("HTTP %d %s -> %v, want %v", tc.status, tc.body, err, tc.want)
		}
		var pe *llm.ProviderError
		if !errors.As(err, &pe) || pe.Status != tc.status || pe.Message == "" {
			t.Errorf("HTTP %d lost what the server said: %+v", tc.status, pe)
		}
	}
}

// A 404 for a mistyped base URL is not "pull a model", which is what the user
// would be told.
func TestA404WithoutAModelIsNotModelNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, err := newProvider(t, srv.URL+"/wrong", nil).Models(context.Background())
	if err == nil || errors.Is(err, llm.ErrModelNotFound) {
		t.Errorf("a wrong path read as %v", err)
	}
}

func TestAServerThatIsNotRunningIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	_, err := newProvider(t, base, nil).Models(context.Background())
	if !errors.Is(err, llm.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestNewRefusesAnythingButAnHTTPURL(t *testing.T) {
	for _, base := range []string{"ftp://example.com", "not a url", "http://"} {
		if _, err := New(Config{ID: "x", BaseURL: base}); err == nil {
			t.Errorf("New(%q) accepted", base)
		}
	}
}

// Decided per request because only then is it known whether a key exists: a
// key read from settings may well be empty.
func TestAKeyNeverTravelsOverPlainHTTPToAnotherHost(t *testing.T) {
	for _, tc := range []struct {
		base string
		key  string
		sent bool
	}{
		{"http://192.168.1.10:11434/v1", "sk", false},
		{"http://example.com/v1", "sk", false},
		{"http://192.168.1.10:11434/v1", "", true},
		{"http://127.0.0.1:11434/v1", "sk", true},
		{"http://localhost:8080/v1", "sk", true},
		{"http://[::1]:8080/v1", "sk", true},
		{"https://api.openai.com/v1", "sk", true},
	} {
		var sent int
		p, err := New(Config{ID: "x", BaseURL: tc.base, Key: StaticKey(tc.key), HTTPClient: &http.Client{
			Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				sent++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Request: r}, nil
			}),
		}})
		if err != nil {
			t.Fatalf("New(%s) must not judge the key, it may be empty: %v", tc.base, err)
		}
		_, err = p.Models(context.Background())
		if (sent == 1) != tc.sent || (err == nil) != tc.sent {
			t.Errorf("%s key=%q: sent %d, err %v", tc.base, tc.key, sent, err)
		}
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLocalMeansThisMachineOnly(t *testing.T) {
	for base, want := range map[string]bool{
		"http://127.0.0.1:11434/v1":    true,
		"http://localhost:8080/v1":     true,
		"http://[::1]:8080/v1":         true,
		"http://192.168.1.10:11434/v1": false,
		"https://api.openai.com/v1":    false,
	} {
		if got := newProvider(t, base, nil).Local(); got != want {
			t.Errorf("%s: Local() = %v, want %v", base, got, want)
		}
	}
}

func TestReasoningArrivesOnItsOwnChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`data: {"choices":[{"delta":{"reasoning_content":"think "}}]}`+"\n\n",
			`data: {"choices":[{"delta":{"reasoning":"harder"}}]}`+"\n\n",
			`data: {"choices":[{"delta":{"content":"42"},"finish_reason":"stop"}]}`+"\n\n",
			"data: [DONE]\n\n")
	}))
	defer srv.Close()
	var kinds []string
	res, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{}, func(c llm.Chunk) error {
		kinds = append(kinds, string(c.Kind)+":"+c.Text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "42" || res.Reasoning != "think harder" {
		t.Errorf("content %q, reasoning %q", res.Content, res.Reasoning)
	}
	if strings.Join(kinds, "|") != "reasoning:think |reasoning:harder|text:42" {
		t.Errorf("chunks = %v", kinds)
	}
}

func TestToolsFormatAndAudioGoOutInOpenAIShape(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		sse(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n", "data: [DONE]\n\n")
	}))
	defer srv.Close()
	schema := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`)
	_, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.User, Content: "hear this", Audio: []llm.AudioClip{{MIME: "audio/wav", Data: []byte("RIFF")}}},
			{Role: llm.Assistant, ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "lookup", Arguments: `{"city":"Hue"}`}}},
			{Role: llm.ToolResult, ToolCallID: "call_1", Content: "found"},
		},
		Tools:      []llm.Tool{{Name: "lookup", Description: "Find a city", Parameters: schema}},
		ToolChoice: "lookup",
		Format:     &llm.ResponseFormat{Name: "city", Schema: schema},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	enc := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	want := map[string]string{
		"tools":           `[{"function":{"description":"Find a city","name":"lookup","parameters":{"properties":{"city":{"type":"string"}},"type":"object"}},"type":"function"}]`,
		"tool_choice":     `{"function":{"name":"lookup"},"type":"function"}`,
		"response_format": `{"json_schema":{"name":"city","schema":{"properties":{"city":{"type":"string"}},"type":"object"}},"type":"json_schema"}`,
	}
	for k, w := range want {
		if got := enc(body[k]); got != w {
			t.Errorf("%s =\n%s\nwant\n%s", k, got, w)
		}
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %v", body["messages"])
	}
	wantMsgs := []string{
		`{"content":[{"text":"hear this","type":"text"},{"input_audio":{"data":"UklGRg==","format":"wav"},"type":"input_audio"}],"role":"user"}`,
		// An assistant turn that only called tools has no text: null, not "".
		`{"content":null,"role":"assistant","tool_calls":[{"function":{"arguments":"{\"city\":\"Hue\"}","name":"lookup"},"id":"call_1","type":"function"}]}`,
		`{"content":"found","role":"tool","tool_call_id":"call_1"}`,
	}
	for i, w := range wantMsgs {
		if got := enc(msgs[i]); got != w {
			t.Errorf("message %d =\n%s\nwant\n%s", i, got, w)
		}
	}
}

// OpenAI refuses a json_schema without a name, and a caller who only cares
// about the shape has no reason to invent one.
func TestAFormatWithoutANameStillHasOne(t *testing.T) {
	var body struct {
		Format struct {
			Schema struct {
				Name string `json:"name"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		sse(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
	}))
	defer srv.Close()
	_, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{Format: &llm.ResponseFormat{Schema: json.RawMessage(`{}`)}}, nil)
	if err != nil || body.Format.Schema.Name == "" {
		t.Errorf("name = %q (%v)", body.Format.Schema.Name, err)
	}
}

func TestToolChoiceWords(t *testing.T) {
	for choice, want := range map[llm.ToolChoice]any{llm.ToolAuto: nil, llm.ToolNone: "none", llm.ToolRequired: "required"} {
		var body map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			sse(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		}))
		_, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{Tools: []llm.Tool{{Name: "x"}}, ToolChoice: choice}, nil)
		srv.Close()
		if err != nil || body["tool_choice"] != want {
			t.Errorf("choice %q sent %v (%v)", choice, body["tool_choice"], err)
		}
	}
}

// Arguments arrive in fragments, and a call is only usable once whole.
func TestToolCallsAssembleFromFragments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`data: {"choices":[{"delta":{"content":"Let me check."}}]}`+"\n\n",
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"lookup","arguments":""}}]}}]}`+"\n\n",
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]}}]}`+"\n\n",
			`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"weather","arguments":"{}"}}]}}]}`+"\n\n",
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Hue\"}"}}]}}]}`+"\n\n",
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n",
			"data: [DONE]\n\n")
	}))
	defer srv.Close()
	var seen []string
	res, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{}, func(c llm.Chunk) error {
		if c.Kind == llm.ChunkToolCall {
			seen = append(seen, c.Call.ID+":"+c.Call.Name+":"+c.Call.Arguments)
		} else {
			seen = append(seen, string(c.Kind))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []llm.ToolCall{{ID: "call_a", Name: "lookup", Arguments: `{"city":"Hue"}`}, {ID: "call_b", Name: "weather", Arguments: "{}"}}
	if !reflect.DeepEqual(res.ToolCalls, want) || res.FinishReason != "tool_calls" || res.Content != "Let me check." {
		t.Errorf("result = %+v", res)
	}
	if strings.Join(seen, "|") != `text|call_a:lookup:{"city":"Hue"}|call_b:weather:{}` {
		t.Errorf("chunks = %q", seen)
	}
}

// A server that leaves index out puts two calls on index 0. A fresh id is the
// only sign that a second call started.
func TestTwoCallsOnOneIndexStayTwo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`data: {"choices":[{"delta":{"tool_calls":[{"id":"a","function":{"name":"one","arguments":{"x":1}}},{"id":"b","function":{"name":"two","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n",
			"data: [DONE]\n\n")
	}))
	defer srv.Close()
	res, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The first also sends its arguments as an object rather than a string.
	want := []llm.ToolCall{{ID: "a", Name: "one", Arguments: `{"x":1}`}, {ID: "b", Name: "two", Arguments: "{}"}}
	if !reflect.DeepEqual(res.ToolCalls, want) {
		t.Errorf("calls = %+v", res.ToolCalls)
	}
}

func TestAudioTheWireCannotCarryIsRefusedBeforeSending(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	_, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.User, Audio: []llm.AudioClip{{MIME: "audio/ogg", Data: []byte{1}}}}},
	}, nil)
	if !errors.Is(err, llm.ErrUnsupported) || called {
		t.Errorf("err = %v, server called %v", err, called)
	}
	for _, mime := range []string{"audio/mpeg", "audio/x-wav", "audio/wav; codecs=1"} {
		if _, ok := audioFormat(mime); !ok {
			t.Errorf("%s refused", mime)
		}
	}
}

// llama-server reports how long the prompt and the generation took in its own
// timings block, and sends usage in a later chunk that has none.
func TestServerTimingsSurviveTheUsageChunk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`data: {"choices":[{"delta":{"content":"hi"}}]}`+"\n\n",
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"timings":{"prompt_ms":12.5,"predicted_ms":250}}`+"\n\n",
			`data: {"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":20}}`+"\n\n",
			"data: [DONE]\n\n")
	}))
	defer srv.Close()
	res, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := llm.Usage{PromptTokens: 7, CompletionTokens: 20, PromptDuration: 12500 * time.Microsecond, GenerateDuration: 250 * time.Millisecond}
	if res.Usage != want {
		t.Errorf("usage = %+v, want %+v", res.Usage, want)
	}
}

// The path a settings screen takes: driver name + stored values + a secret
// reader, with no code for this engine in the app.
func TestTheDriverBuildsAProviderFromStoredSettings(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"data":[{"id":"m"}]}`)
	}))
	defer srv.Close()

	p, err := llm.Open("openai", llm.ProviderConfig{
		ID:     "local",
		Values: map[string]string{"base_url": srv.URL, "vision": "true", "tools": "true", "transcribe": "true"},
		Secret: func(_ context.Context, key string) (string, error) {
			if key != "api_key" {
				return "", fmt.Errorf("asked for %q", key)
			}
			return "sk-sealed", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !llm.Has(p, llm.Vision) || !llm.Has(p, llm.Chat) || !llm.Has(p, llm.Tools) || !llm.Has(p, llm.Transcribe) || llm.Has(p, llm.Audio) || !llm.IsLocal(p) {
		t.Errorf("capabilities %v, local %v", p.Capabilities(), llm.IsLocal(p))
	}
	if _, err := p.(llm.ModelLister).Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer sk-sealed" {
		t.Errorf("Authorization = %q", auth)
	}
	if _, err := llm.Open("openai", llm.ProviderConfig{ID: "x"}); err == nil {
		t.Error("a provider without a base URL was opened")
	}
}

func TestNoKeyMeansNoAuthorizationHeader(t *testing.T) {
	var headers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = append(headers, r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer srv.Close()
	for _, key := range []KeySource{nil, StaticKey("")} {
		if _, err := newProvider(t, srv.URL, key).Models(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range headers {
		if h != "" {
			t.Errorf("sent Authorization %q with no key", h)
		}
	}
}

// Read on every call, so a key that cannot be unsealed must stop the request
// rather than send one without it.
func TestAKeySourceErrorStopsTheRequest(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	broken := func(context.Context) (string, error) { return "", errors.New("keychain locked") }
	if _, err := newProvider(t, srv.URL, broken).Models(context.Background()); err == nil {
		t.Error("a request went out without the key it was supposed to carry")
	}
	if called {
		t.Error("the server was called anyway")
	}
}

func TestImagesTravelAsDataURLParts(t *testing.T) {
	var got struct {
		Messages []struct {
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			} `json:"content"`
		} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		sse(w, `data: {"choices":[{"delta":{"content":"a cat"},"finish_reason":"stop"}]}`+"\n\n", "data: [DONE]\n\n")
	}))
	defer srv.Close()
	_, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{Messages: []llm.Message{{
		Role: llm.User, Content: "what is this", Images: []llm.Image{{MIME: "image/png", Data: []byte("PNG")}},
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	parts := got.Messages[0].Content
	if len(parts) != 2 || parts[0].Text != "what is this" || parts[1].ImageURL.URL != "data:image/png;base64,UE5H" {
		t.Errorf("parts = %+v", parts)
	}
}

func TestTypedFieldsWinOverExtra(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		sse(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
	}))
	defer srv.Close()
	temp := 0.2
	_, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{
		Model: "m", MaxTokens: 50, Temperature: &temp,
		Extra: map[string]any{"max_tokens": 9999, "stream": false, "top_k": 40},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["max_tokens"] != float64(50) || got["stream"] != true || got["top_k"] != float64(40) || got["temperature"] != 0.2 {
		t.Errorf("body = %v", got)
	}
}

func TestEmbedKeepsEachVectorWithItsInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"model":"e","data":[{"index":1,"embedding":[2,2]},{"index":0,"embedding":[1,1]}],"usage":{"prompt_tokens":4}}`)
	}))
	defer srv.Close()
	res, err := newProvider(t, srv.URL, nil).Embed(context.Background(), llm.EmbedRequest{Model: "e", Input: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Vectors[0][0] != 1 || res.Vectors[1][0] != 2 || res.Usage.PromptTokens != 4 {
		t.Errorf("res = %+v", res)
	}
}

func TestEmbedRefusesAnAnswerShortOfItsInputs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[1]}]}`)
	}))
	defer srv.Close()
	if _, err := newProvider(t, srv.URL, nil).Embed(context.Background(), llm.EmbedRequest{Input: []string{"a", "b"}}); err == nil {
		t.Error("one vector for two inputs came back as a result")
	}
}

func TestBaseURLWithOrWithoutATrailingSlash(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = io.WriteString(w, `{"data":[{"id":"m1","owned_by":"me"}]}`)
	}))
	defer srv.Close()
	for _, base := range []string{srv.URL + "/v1", srv.URL + "/v1/"} {
		ms, err := newProvider(t, base, nil).Models(context.Background())
		if err != nil || len(ms) != 1 || ms[0].ID != "m1" || ms[0].OwnedBy != "me" {
			t.Errorf("%s: %+v %v", base, ms, err)
		}
	}
	for _, p := range paths {
		if p != "/v1/models" {
			t.Errorf("requested %q", p)
		}
	}
}

func TestTranscribeSendsTheClipAsAForm(t *testing.T) {
	var got struct {
		path, auth, filename, fileType, model, format string
		data                                          []byte
		lang                                          []string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.auth = r.URL.Path, r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		f, h, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		got.data, _ = io.ReadAll(f)
		got.filename, got.fileType = h.Filename, h.Header.Get("Content-Type")
		got.model, got.format, got.lang = r.FormValue("model"), r.FormValue("response_format"), r.MultipartForm.Value["language"]
		_, _ = io.WriteString(w, `{"text":" The quick brown fox.\n"}`)
	}))
	defer srv.Close()
	p := newProvider(t, srv.URL+"/v1", StaticKey("sk-test"))
	res, err := p.Transcribe(context.Background(), llm.TranscribeRequest{Model: "whisper-1", Language: "en", Audio: llm.AudioClip{MIME: "audio/wav", Data: []byte("RIFF")}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "The quick brown fox." || res.Language != "en" {
		t.Errorf("result %+v", res)
	}
	if got.path != "/v1/audio/transcriptions" || got.auth != "Bearer sk-test" || got.filename != "audio.wav" || got.fileType != "audio/wav" ||
		string(got.data) != "RIFF" || got.model != "whisper-1" || got.format != "json" || len(got.lang) != 1 || got.lang[0] != "en" {
		t.Errorf("sent %+v", got)
	}

	// No language means the model detects it, so none is sent.
	if _, err := p.Transcribe(context.Background(), llm.TranscribeRequest{Model: "whisper-1", Audio: llm.AudioClip{MIME: "audio/mpeg", Data: []byte("ID3")}}); err != nil {
		t.Fatal(err)
	}
	if got.lang != nil || got.filename != "audio.mp3" {
		t.Errorf("sent language %v as %s", got.lang, got.filename)
	}
}

func TestTranscribeRefusesAClipItCannotName(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	_, err := newProvider(t, srv.URL, nil).Transcribe(context.Background(), llm.TranscribeRequest{Audio: llm.AudioClip{MIME: "audio/aiff", Data: []byte{1}}})
	if !errors.Is(err, llm.ErrUnsupported) || called {
		t.Errorf("err = %v, server called %v", err, called)
	}
	for mime, ext := range map[string]string{"audio/x-wav": "wav", "audio/flac": "flac", "audio/ogg; codecs=opus": "ogg", "audio/webm": "webm", "audio/mp4": "m4a"} {
		if got, ok := audioExt(mime); !ok || got != ext {
			t.Errorf("%s -> %q %v", mime, got, ok)
		}
	}
}

// Lemonade 11.9.0 answers an unknown model this way.
func TestTranscribeErrorsReadLikeChatErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"model_not_found","message":"Model 'No-Such-Model' was not found.","type":"model_not_found"}}`)
	}))
	defer srv.Close()
	_, err := newProvider(t, srv.URL, nil).Transcribe(context.Background(), llm.TranscribeRequest{Model: "No-Such-Model", Audio: llm.AudioClip{MIME: "audio/wav"}})
	if !errors.Is(err, llm.ErrModelNotFound) || !strings.Contains(err.Error(), "was not found") {
		t.Errorf("err = %v", err)
	}
}

// llama-server refuses a prompt past its -c window with a 400 whose wording
// is not OpenAI's, and an app trims and retries on ErrContextFull.
func TestAPromptPastTheWindowSaysSo(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"This model's maximum context length is 4096 tokens, however you requested 5000","code":"context_length_exceeded"}}`,
		`{"error":{"message":"the request exceeds the available context size, try increasing it","type":"exceed_context_size_error"}}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, body)
		}))
		_, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{Model: "m"}, nil)
		if !errors.Is(err, llm.ErrContextFull) {
			t.Errorf("%s -> %v", body, err)
		}
		srv.Close()
	}
}

// A 400 that is not about the window keeps its own wording and no sentinel.
func TestAnOrdinaryBadRequestIsNotAFullWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"Unsupported parameter: 'max_tokens'"}}`)
	}))
	defer srv.Close()
	_, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{Model: "m"}, nil)
	if errors.Is(err, llm.ErrContextFull) || !strings.Contains(err.Error(), "Unsupported parameter") {
		t.Errorf("err = %v", err)
	}
}

// Wi-Fi drops mid-answer and the last line arrives half written: parsing it
// before looking at the read error reports a malformed server.
func TestAStreamCutMidLineIsABrokenConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `data: {"choices":[{"delta":{"content":"hi"`)
		panic(http.ErrAbortHandler)
	}))
	defer srv.Close()
	_, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{Model: "m"}, nil)
	if !errors.Is(err, llm.ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
}

// Two names for one thing, so a gateway that normalises and sends both must not
// make the user read the model's thinking twice.
func TestReasoningUnderBothNamesIsNotDoubled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `data: {"choices":[{"delta":{"reasoning":"Let me think.","reasoning_content":"Let me think."}}]}`+"\n\n",
			`data: {"choices":[{"delta":{"content":"42"},"finish_reason":"stop"}]}`+"\n\n")
	}))
	defer srv.Close()
	res, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reasoning != "Let me think." {
		t.Errorf("reasoning = %q", res.Reasoning)
	}
}

// HEIC is what an iPhone photo is: the API answers "Invalid image" and a
// local model may describe an image nobody sent.
func TestAnImageTheWireCannotCarryIsRefusedBeforeSending(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	_, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{
		Model:    "m",
		Messages: []llm.Message{{Role: llm.User, Content: "what is this", Images: []llm.Image{{MIME: "image/heic", Data: []byte{1}}}}},
	}, nil)
	if !errors.Is(err, llm.ErrUnsupported) || called {
		t.Errorf("err = %v, called = %v", err, called)
	}
	for _, mime := range []string{"image/png", "image/jpeg", "image/gif", "image/webp", "IMAGE/PNG"} {
		if _, err := toWire([]llm.Message{{Role: llm.User, Images: []llm.Image{{MIME: mime, Data: []byte{1}}}}}); err != nil {
			t.Errorf("%s: %v", mime, err)
		}
	}
}

// The reasoning models refuse max_tokens and want max_completion_tokens. A
// caller who sends that must not also get max_tokens, or every request 400s.
func TestMaxCompletionTokensReplacesMaxTokens(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		sse(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
	}))
	defer srv.Close()
	_, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{
		Model: "gpt-5", MaxTokens: 50, Extra: map[string]any{"max_completion_tokens": 400},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["max_tokens"]; ok || got["max_completion_tokens"] != float64(400) {
		t.Errorf("body = %v", got)
	}
}

// Go copies Authorization across a same-host redirect whatever the scheme,
// and strips nothing from x-api-key. All httptest servers are loopback,
// where a key over plain http is allowed, so this needs stub transports.
func TestAKeyDoesNotFollowARedirectOffHTTPS(t *testing.T) {
	var hops []*http.Request
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hops = append(hops, r)
		rec := httptest.NewRecorder()
		if len(hops) == 1 {
			rec.Header().Set("Location", "http://gw.example.com/v1/chat/completions")
			rec.WriteHeader(http.StatusTemporaryRedirect)
		} else {
			rec.Header().Set("Content-Type", "text/event-stream")
			_, _ = rec.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n")
		}
		resp := rec.Result()
		resp.Request = r
		return resp, nil
	})
	p, err := New(Config{ID: "test", BaseURL: "https://gw.example.com/v1", HTTPClient: &http.Client{Transport: rt},
		Key: func(context.Context) (string, error) { return "sk-secret", nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Chat(context.Background(), llm.ChatRequest{Model: "m"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(hops) != 2 {
		t.Fatalf("hops = %d", len(hops))
	}
	if hops[0].Header.Get("Authorization") == "" {
		t.Error("the key never went out at all")
	}
	if got := hops[1].Header.Get("Authorization"); got != "" {
		t.Errorf("the key reached plain http: %q", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A gateway that ignores stream:true answers one JSON object with HTTP 200.
func TestAnAnswerSentWithoutSSEIsStillAnAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"m","choices":[{"message":{"content":"The answer is 42."},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":5}}`)
	}))
	defer srv.Close()
	var text string
	res, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{Model: "m"}, func(c llm.Chunk) error {
		if c.Kind == llm.ChunkText {
			text += c.Text
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "The answer is 42." || text != res.Content || res.FinishReason != "stop" || res.Usage.CompletionTokens != 5 {
		t.Errorf("res = %+v, streamed %q", res, text)
	}
}

// Some gateways report a failure with HTTP 200 and an error envelope. Losing
// the message leaves the user with "unreachable" for an account problem.
func TestAnErrorEnvelopeSentWithHTTP200IsSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"error":{"message":"your credit balance is too low"}}`)
	}))
	defer srv.Close()
	_, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{Model: "m"}, nil)
	if err == nil || !strings.Contains(err.Error(), "credit balance is too low") {
		t.Errorf("err = %v", err)
	}
}

// The non-streaming fallback is for a body that never streamed: read after
// a stream it hands the app the same tool calls twice.
func TestAnAnswerAlreadyStreamedIsNotRepeated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `data: {"choices":[{"delta":{"content":"42","tool_calls":[{"index":0,"id":"c1","function":{"name":"rm","arguments":"{}"}}]}}]}`+"\n\n",
			`{"choices":[{"message":{"content":"42","tool_calls":[{"id":"c1","function":{"name":"rm","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`+"\n")
	}))
	defer srv.Close()
	var text string
	var calls int
	res, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{Model: "m"}, func(c llm.Chunk) error {
		switch c.Kind {
		case llm.ChunkText:
			text += c.Text
		case llm.ChunkToolCall:
			calls++
		}
		return nil
	})
	// No finish_reason and no [DONE], so the stream really was cut short and
	// saying so is right. What must not happen is the second copy.
	if !errors.Is(err, llm.ErrUnavailable) {
		t.Errorf("err = %v, res = %+v", err, res)
	}
	if text != "42" || calls != 0 {
		t.Errorf("the app was handed %q and %d tool calls", text, calls)
	}
}

// A proxy that drops a response closes the body cleanly, so the half line
// arrives with io.EOF and reads like the end of an answer.
func TestAStreamCutAtACleanCloseIsABrokenConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `data: {"choices":[{"delta":{"content":"hi"`)
	}))
	defer srv.Close()
	_, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{Model: "m"}, nil)
	if !errors.Is(err, llm.ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
}

// An empty data line is a keepalive, not a frame. Reading it as one throws
// away an answer that had already arrived.
func TestAnEmptyDataLineIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, "data:\n\n", `data: {"choices":[{"delta":{"content":"42"},"finish_reason":"stop"}]}`+"\n\n", "data: [DONE]\n\n")
	}))
	defer srv.Close()
	res, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{Model: "m"}, nil)
	if err != nil || res.Content != "42" {
		t.Errorf("res %+v, err %v", res, err)
	}
}

// The check normalises the MIME; the data URL has to carry the same thing, or
// the gate says "this one is carried" and the API still answers Invalid image.
func TestAnImageMIMEGoesOutNormalised(t *testing.T) {
	msgs, err := toWire([]llm.Message{{Role: llm.User, Images: []llm.Image{{MIME: "IMAGE/PNG; charset=binary", Data: []byte("PNG")}}}})
	if err != nil {
		t.Fatal(err)
	}
	parts, ok := msgs[0].Content.([]wireContentPart)
	if !ok || len(parts) != 2 || parts[1].ImageURL == nil {
		t.Fatalf("content = %+v", msgs[0].Content)
	}
	if got := parts[1].ImageURL.URL; !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Errorf("url = %q", got)
	}
}

// The caller reached for the name the reasoning models want, so the name they
// refuse must not go out beside it - whichever way it was set.
func TestMaxTokensNeverTravelsWithMaxCompletionTokens(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		sse(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
	}))
	defer srv.Close()
	_, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{
		Model: "gpt-5", Extra: map[string]any{"max_completion_tokens": 400, "max_tokens": 9999},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["max_tokens"]; ok || got["max_completion_tokens"] != float64(400) {
		t.Errorf("body = %v", got)
	}
}

// A rate limit is the same request a moment later, and the header is the
// server saying when. Without this every app writes the loop itself.
func TestARateLimitIsSentAgain(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		sse(w, `data: {"choices":[{"delta":{"content":"42"},"finish_reason":"stop"}]}`+"\n\n")
	}))
	defer srv.Close()
	p, err := New(Config{ID: "test", BaseURL: srv.URL, Retry: llm.RetryPolicy{Attempts: 3, Base: time.Millisecond, Max: time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Chat(context.Background(), llm.ChatRequest{Model: "m"}, nil)
	if err != nil || res.Content != "42" {
		t.Fatalf("res %+v, err %v", res, err)
	}
	if hits != 3 {
		t.Errorf("sent %d times", hits)
	}
}

// The request body is read once, so a retry that reuses the reader sends an
// empty body the second time and the server answers something else entirely.
func TestARetriedRequestSendsItsBodyAgain(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(bodies) < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		sse(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
	}))
	defer srv.Close()
	p, err := New(Config{ID: "test", BaseURL: srv.URL, Retry: llm.RetryPolicy{Attempts: 2, Base: time.Millisecond, Max: time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Chat(context.Background(), llm.ChatRequest{Model: "m"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0] == "" || bodies[0] != bodies[1] {
		t.Errorf("bodies = %q", bodies)
	}
}

// Arguments were always assembled from fragments; the name was taken from
// the first and the rest dropped, so a proxy with an incremental parser
// left the model told its own tool does not exist.
func TestAToolNameArrivesWholeHoweverItIsSplit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames []string
		want   string
	}{
		{"whole in the first fragment, as the spec says", []string{`{"index":0,"id":"c1","function":{"name":"get_weather","arguments":"{}"}}`}, "get_weather"},
		{"split across fragments", []string{
			`{"index":0,"id":"c1","function":{"name":"get_"}}`,
			`{"index":0,"function":{"name":"weather","arguments":"{}"}}`,
		}, "get_weather"},
		{"repeated whole in every fragment", []string{
			`{"index":0,"id":"c1","function":{"name":"get_weather","arguments":"{\"a\":"}}`,
			`{"index":0,"function":{"name":"get_weather","arguments":"1}"}}`,
		}, "get_weather"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for _, f := range tc.frames {
					sse(w, `data: {"choices":[{"delta":{"tool_calls":[`+f+`]}}]}`+"\n\n")
				}
				sse(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
			}))
			defer srv.Close()
			res, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{Model: "m"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != tc.want {
				t.Errorf("calls = %+v", res.ToolCalls)
			}
		})
	}
}

// What a prefix test gets wrong: skipping a fragment the name starts with
// loses it.
func TestAToolNameKeepsEveryFragmentThatIsNotARepeat(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames []string
		want   string
	}{
		{"a fragment that is a prefix of what came before", []string{"list_", "list", "s"}, "list_lists"},
		{"a short fragment after a long one", []string{"get_weather_", "get"}, "get_weather_get"},
		{"three ways up to one name", []string{"g", "et_", "weather"}, "get_weather"},
		{"the same name in every fragment", []string{"get_weather", "get_weather", "get_weather"}, "get_weather"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for i, n := range tc.frames {
					id := ""
					if i == 0 {
						id = `"id":"c1",`
					}
					sse(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,`+id+`"function":{"name":"`+n+`"}}]}}]}`+"\n\n")
				}
				sse(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
			}))
			defer srv.Close()
			res, err := newProvider(t, srv.URL, nil).Chat(context.Background(), llm.ChatRequest{Model: "m"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != tc.want {
				t.Errorf("calls = %+v, want name %q", res.ToolCalls, tc.want)
			}
		})
	}
}

// Without this the frontend asks "can this model see", gets an error, reads
// it as "no", and the attach button is gone for every OpenAI model.
func TestDescribeSaysWhatTheProviderCanDoAndWhatTheServerKnows(t *testing.T) {
	// The shape a real llama-server sends, from testdata/llama-server-b9934.json.
	const list = `{"object":"list","data":[
		{"id":"smollm2:1.7b","object":"model","owned_by":"llamacpp","meta":{"n_ctx":4096,"n_ctx_train":8192}},
		{"id":"trained-only","object":"model","meta":{"n_ctx_train":32768}},
		{"id":"gpt-5","object":"model","owned_by":"openai"}]}`

	for _, tc := range []struct {
		name  string
		model string
		reply string
		code  int
		ctx   int
	}{
		{"a server that says which window it is serving", "smollm2:1.7b", list, 200, 4096},
		{"only the window it was trained for", "trained-only", list, 200, 32768},
		{"a hosted API, which says neither", "gpt-5", list, 200, 0},
		// Both of these used to be an error, and an error is read as "cannot".
		{"a model the listing does not mention", "gpt-5-turbo-2031", list, 200, 0},
		{"a listing nobody can read", "gpt-5", "}{", 200, 0},
		{"no listing endpoint at all", "gpt-5", `{"error":{"message":"nope"}}`, 404, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/models" {
					t.Errorf("asked for %s", r.URL.Path)
				}
				w.WriteHeader(tc.code)
				_, _ = io.WriteString(w, tc.reply)
			}))
			defer srv.Close()
			p, err := New(Config{ID: "test", BaseURL: srv.URL, Retry: llm.RetryPolicy{Attempts: 1},
				Capabilities: []llm.Capability{llm.Chat, llm.Vision}})
			if err != nil {
				t.Fatal(err)
			}
			d, err := p.Describe(context.Background(), tc.model)
			if err != nil {
				t.Fatalf("Describe: %v", err)
			}
			if d.ID != tc.model || d.ContextLength != tc.ctx {
				t.Errorf("id %q context %d, want %q %d", d.ID, d.ContextLength, tc.model, tc.ctx)
			}
			if !slices.Equal(d.Capabilities, []llm.Capability{llm.Chat, llm.Vision}) {
				t.Errorf("capabilities = %v", d.Capabilities)
			}
		})
	}
}

// The boxes on the provider form are what the answer above is built from, so a
// provider added without ticking them must not claim to see.
func TestTheDriverOnlyClaimsWhatTheFormTicked(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values map[string]string
		want   []llm.Capability
	}{
		{"nothing ticked", map[string]string{"base_url": "https://x/v1"}, []llm.Capability{llm.Chat, llm.Embed}},
		{"vision ticked", map[string]string{"base_url": "https://x/v1", "vision": "true"}, []llm.Capability{llm.Chat, llm.Embed, llm.Vision}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Driver{}.Open(llm.ProviderConfig{ID: "p", Values: tc.values})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(p.Capabilities(), tc.want) {
				t.Errorf("capabilities = %v, want %v", p.Capabilities(), tc.want)
			}
		})
	}
}

// The same through a driver, because the helper is only worth having if the
// call sites use it.
func TestACutOffReplyComesBackAsUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name        string
		body        string
		length      string
		unavailable bool
	}{
		{"cut off after the headers promised more", `{"data":[{"embed`, "400", true},
		{"a complete answer that is not JSON", `<html>maintenance</html>`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.length != "" {
					w.Header().Set("Content-Length", tc.length)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			_, err := newProvider(t, srv.URL, nil).Embed(context.Background(), llm.EmbedRequest{Model: "m", Input: []string{"x"}})
			if err == nil {
				t.Fatal("a cut-off reply was accepted")
			}
			if got := errors.Is(err, llm.ErrUnavailable); got != tc.unavailable {
				t.Errorf("ErrUnavailable = %v, want %v (%v)", got, tc.unavailable, err)
			}
		})
	}
}

// Getting this wrong is worse than the leak: closing the shared default
// transport takes the idle connections of everything else with it.
func TestOnlyATransportItMadeItselfIsClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cfg   Config
		owned bool
	}{
		{"a hosted API, where it clones one", Config{ID: "a", BaseURL: "https://api.openai.com/v1"}, true},
		{"a server on this machine, where it leaves the default alone", Config{ID: "b", BaseURL: "http://127.0.0.1:11434/v1"}, false},
		{"a server on the LAN, same reason", Config{ID: "c", BaseURL: "http://192.168.1.50:8080/v1"}, false},
		{"a client the caller brought", Config{ID: "d", BaseURL: "https://api.openai.com/v1", HTTPClient: &http.Client{}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := New(tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if (p.owned != nil) != tc.owned {
				t.Errorf("owned = %v, want %v", p.owned != nil, tc.owned)
			}
			if p.owned != nil && p.owned == http.DefaultTransport {
				t.Error("it claimed the shared default transport as its own")
			}
			// Twice, and on a provider that owns nothing.
			if err := p.Close(); err != nil {
				t.Error(err)
			}
			if err := p.Close(); err != nil {
				t.Error(err)
			}
		})
	}
}

// A provider opened through the registry is the only one that asks for a
// trace, and the numbers-only default used to drop the hook with it.
func TestAProviderOpenedByTheRegistryStillReportsItsRetries(t *testing.T) {
	var tries int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&tries, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"embedding":[0.5],"index":0}],"model":"m"}`)
	}))
	defer srv.Close()

	var mu sync.Mutex
	var seen []string
	p, err := Driver{}.Open(llm.ProviderConfig{
		ID:     "work",
		Values: map[string]string{"base_url": srv.URL},
		OnRetry: func(attempt int, wait time.Duration, status int, err error) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, fmt.Sprintf("attempt=%d status=%d err=%v", attempt, status, err))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.(*Provider).Embed(context.Background(), llm.EmbedRequest{Model: "m", Input: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || !strings.Contains(seen[0], "status=429") {
		t.Errorf("reported %q", seen)
	}
	// And the timing default is still in force: one retry, not none.
	if n := atomic.LoadInt32(&tries); n != 2 {
		t.Errorf("sent %d times", n)
	}
}
