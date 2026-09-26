package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tradalab/scorix/llm"
)

type seen struct {
	method, path, query string
	header              http.Header
	body                map[string]any
}

type fake struct {
	mu   sync.Mutex
	reqs []seen
}

func (f *fake) got() []seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seen(nil), f.reqs...)
}

func (f *fake) messages() []seen {
	var out []seen
	for _, r := range f.got() {
		if r.path == "/v1/messages" {
			out = append(out, r)
		}
	}
	return out
}

func serve(t *testing.T, h http.HandlerFunc) (*Provider, *fake) {
	t.Helper()
	f := &fake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := seen{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, header: r.Header.Clone()}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			if err := json.Unmarshal(b, &s.body); err != nil {
				t.Errorf("request body is not JSON: %s", b)
			}
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, s)
		f.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	// One try: every test that asserts an error mapping would otherwise sit
	// through the real backoff. The retry has its own test.
	p, err := New(Config{ID: "claude", BaseURL: srv.URL + "/v1", Key: StaticKey("sk-test"), Retry: llm.RetryPolicy{Attempts: 1}})
	if err != nil {
		t.Fatal(err)
	}
	return p, f
}

func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(e), &probe)
		b.WriteString("event: " + probe.Type + "\ndata: " + e + "\n\n")
	}
	return b.String()
}

// The tool-use stream from Anthropic's streaming guide, with a thinking block
// as Opus 5 sends it by default: display omitted, so empty but signed.
var toolTurn = sse(
	`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[],"stop_reason":null,"usage":{"input_tokens":472,"cache_creation_input_tokens":100,"cache_read_input_tokens":2000,"output_tokens":2}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EqQBCgIYAhIM"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
	`{"type": "ping"}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Okay"}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":", checking."}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_01","name":"get_weather","input":{}}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":""}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"location\":"}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":" \"San Francisco, CA\"}"}}`,
	`{"type":"content_block_stop","index":2}`,
	`{"type":"some_future_event","index":9}`,
	`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":89}}`,
	`{"type":"message_stop"}`,
)

func streamWith(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}
}

func TestAToolTurnStreamsAndKeepsItsBlocks(t *testing.T) {
	p, _ := serve(t, streamWith(toolTurn))
	var kinds []string
	res, err := p.Chat(context.Background(), llm.ChatRequest{Model: "opus", Messages: []llm.Message{{Role: llm.User, Content: "Weather in SF?"}}}, func(c llm.Chunk) error {
		kinds = append(kinds, string(c.Kind)+":"+c.Text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(kinds, "|") != "text:Okay|text:, checking.|tool_call:" {
		t.Errorf("chunks %q; an empty thinking delta is not a chunk", kinds)
	}
	if res.Content != "Okay, checking." || res.Reasoning != "" || res.FinishReason != "tool_use" || res.Model != "claude-opus-5" {
		t.Errorf("result %+v: the model is the one that answered, not the alias asked for", res)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "toolu_01" || res.ToolCalls[0].Name != "get_weather" {
		t.Fatalf("calls %+v", res.ToolCalls)
	}
	var args struct{ Location string }
	if err := res.ToolCalls[0].Decode(&args); err != nil || args.Location != "San Francisco, CA" {
		t.Errorf("arguments %q: %v", res.ToolCalls[0].Arguments, err)
	}
	if u := res.Usage; u.PromptTokens != 2572 || u.CacheReadTokens != 2000 || u.CacheWriteTokens != 100 || u.CompletionTokens != 89 {
		t.Errorf("usage %+v: output is the running total, input counts the cached part too", u)
	}
	if res.Replay == nil || res.Replay.Driver != "anthropic" {
		t.Fatalf("replay %+v", res.Replay)
	}
	var blocks []map[string]any
	if err := json.Unmarshal(res.Replay.Data, &blocks); err != nil || len(blocks) != 3 {
		t.Fatalf("replay %s: %v", res.Replay.Data, err)
	}
	thinking, hasText := blocks[0]["thinking"]
	if blocks[0]["type"] != "thinking" || !hasText || thinking != "" || blocks[0]["signature"] != "EqQBCgIYAhIM" {
		t.Errorf("thinking block %v: it goes back with its empty text and its signature", blocks[0])
	}
	if blocks[1]["text"] != "Okay, checking." {
		t.Errorf("text block %v", blocks[1])
	}
	if in, _ := blocks[2]["input"].(map[string]any); blocks[2]["id"] != "toolu_01" || in["location"] != "San Francisco, CA" {
		t.Errorf("tool block %v: input is an object, not the streamed string", blocks[2])
	}
}

// With display summarized the thinking has text, which is shown as reasoning
// and still goes back inside its block; a tool with no arguments gets "{}".
func TestSummarizedThinkingIsReasoningAndStaysInItsBlock(t *testing.T) {
	p, _ := serve(t, streamWith(sse(
		`{"type":"message_start","message":{"model":"claude-opus-5","usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"GCD of 1071"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" and 462."}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"Sig"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"opaque"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_2","name":"now","input":{}}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":12,"output_tokens":40}}`,
		`{"type":"message_stop"}`,
	)))
	var reasoning []string
	res, err := p.Chat(context.Background(), llm.ChatRequest{Model: "m", MaxTokens: 5, Messages: []llm.Message{{Role: llm.User, Content: "gcd?"}}}, func(c llm.Chunk) error {
		if c.Kind == llm.ChunkReasoning {
			reasoning = append(reasoning, c.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reasoning != "GCD of 1071 and 462." || len(reasoning) != 2 || res.Content != "" {
		t.Errorf("reasoning %q from %q, content %q", res.Reasoning, reasoning, res.Content)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Arguments != "{}" {
		t.Errorf("calls %+v", res.ToolCalls)
	}
	if res.Usage.PromptTokens != 12 || res.Usage.CompletionTokens != 40 {
		t.Errorf("usage %+v: a later running total replaces the first", res.Usage)
	}
	want := `[{"signature":"Sig","thinking":"GCD of 1071 and 462.","type":"thinking"},{"data":"opaque","type":"redacted_thinking"},{"id":"toolu_2","input":{},"name":"now","type":"tool_use"}]`
	if string(res.Replay.Data) != want {
		t.Errorf("replay\n %s\nwant\n %s", res.Replay.Data, want)
	}
}

func TestTheToolTurnGoesBackAsItCame(t *testing.T) {
	p, f := serve(t, streamWith(toolTurn))
	ctx := context.Background()
	first, err := p.Chat(ctx, llm.ChatRequest{Model: "claude-opus-5", MaxTokens: 1000, Messages: []llm.Message{{Role: llm.User, Content: "Weather in SF?"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Chat(ctx, llm.ChatRequest{Model: "claude-opus-5", MaxTokens: 1000, Messages: []llm.Message{
		{Role: llm.System, Content: "Be brief."},
		{Role: llm.User, Content: "Weather in SF?"},
		first.Message(),
		{Role: llm.ToolResult, ToolCallID: "toolu_01", Content: "service down", Failed: true},
		{Role: llm.User, Content: "Try again later then."},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := f.messages()[1]
	if req.header.Get("x-api-key") != "sk-test" || req.header.Get("anthropic-version") != "2023-06-01" {
		t.Errorf("headers %v", req.header)
	}
	if sys, _ := json.Marshal(req.body["system"]); string(sys) != `[{"text":"Be brief.","type":"text"}]` {
		t.Errorf("system %s", sys)
	}
	msgs, _ := req.body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages %v: system leaves the list, the tool result and the next question share a turn", msgs)
	}
	sent, _ := json.Marshal(msgs[1].(map[string]any)["content"])
	var want []any
	_ = json.Unmarshal(first.Replay.Data, &want)
	if kept, _ := json.Marshal(want); string(sent) != string(kept) {
		t.Errorf("assistant turn\n sent %s\n want %s", sent, kept)
	}
	last := msgs[2].(map[string]any)
	blocks, _ := last["content"].([]any)
	if last["role"] != "user" || len(blocks) != 2 {
		t.Fatalf("last turn %v", last)
	}
	result := blocks[0].(map[string]any)
	if result["type"] != "tool_result" || result["tool_use_id"] != "toolu_01" || result["is_error"] != true || result["content"] != "service down" {
		t.Errorf("tool result %v", result)
	}
	if blocks[1].(map[string]any)["text"] != "Try again later then." {
		t.Errorf("text after the result %v", blocks[1])
	}
}

// A turn another provider wrote has no replay: it is rebuilt from what the
// message says, and a call whose arguments are not an object still goes out.
func TestATurnFromAnotherProviderIsRebuilt(t *testing.T) {
	p, f := serve(t, streamWith(toolTurn))
	_, err := p.Chat(context.Background(), llm.ChatRequest{Model: "m", MaxTokens: 10, Messages: []llm.Message{
		{Role: llm.User, Content: "hi"},
		{Role: llm.Assistant, Content: "calling", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "look", Arguments: `{"q":1}`}, {ID: "c2", Name: "look", Arguments: "not json"}}, Replay: &llm.Replay{Driver: "gemini", Data: json.RawMessage(`"sig"`)}},
		{Role: llm.ToolResult, ToolCallID: "c1", Content: "one"},
		{Role: llm.ToolResult, ToolCallID: "c2"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	msgs := f.messages()[0].body["messages"].([]any)
	got, _ := json.Marshal(msgs[1:])
	want := `[{"content":[{"text":"calling","type":"text"},{"id":"c1","input":{"q":1},"name":"look","type":"tool_use"},{"id":"c2","input":{},"name":"look","type":"tool_use"}],"role":"assistant"},` +
		`{"content":[{"content":"one","tool_use_id":"c1","type":"tool_result"},{"tool_use_id":"c2","type":"tool_result"}],"role":"user"}]`
	if string(got) != want {
		t.Errorf("sent\n %s\nwant\n %s", got, want)
	}
}

func TestTheRequestCarriesWhatTheAppAskedFor(t *testing.T) {
	p, f := serve(t, streamWith(toolTurn))
	temp := 0.2
	schema := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`)
	req := llm.ChatRequest{
		Model: "claude-sonnet-5", MaxTokens: 50, Temperature: &temp, Stop: []string{"END"},
		Messages: []llm.Message{{Role: llm.User, Content: "look", Images: []llm.Image{{MIME: "image/png", Data: []byte{1, 2}}}}, {Role: llm.Assistant}, {Role: llm.User}},
		Tools:    []llm.Tool{{Name: "find", Description: "Finds", Parameters: schema}, {Name: "bare"}},
		Format:   &llm.ResponseFormat{Schema: schema},
		Extra:    map[string]any{"output_config": map[string]any{"effort": "low"}, "metadata": map[string]any{"user_id": "u"}},
	}
	for choice, want := range map[llm.ToolChoice]string{
		llm.ToolAuto:     "null",
		llm.ToolNone:     `{"type":"none"}`,
		llm.ToolRequired: `{"type":"any"}`,
		"find":           `{"name":"find","type":"tool"}`,
	} {
		req.ToolChoice = choice
		if _, err := p.Chat(context.Background(), req, nil); err != nil {
			t.Fatal(err)
		}
		all := f.messages()
		body := all[len(all)-1].body
		if tc, _ := json.Marshal(body["tool_choice"]); string(tc) != want {
			t.Errorf("tool_choice for %q: %s", choice, tc)
		}
	}
	body := f.messages()[0].body
	for key, want := range map[string]string{
		"max_tokens":     "50",
		"temperature":    "0.2",
		"stop_sequences": `["END"]`,
		"stream":         "true",
		"model":          `"claude-sonnet-5"`,
		"metadata":       `{"user_id":"u"}`,
		"output_config":  `{"effort":"low","format":{"schema":{"properties":{"city":{"type":"string"}},"type":"object"},"type":"json_schema"}}`,
		"tools":          `[{"description":"Finds","input_schema":{"properties":{"city":{"type":"string"}},"type":"object"},"name":"find"},{"input_schema":{"type":"object"},"name":"bare"}]`,
		"messages":       `[{"content":[{"source":{"data":"AQI=","media_type":"image/png","type":"base64"},"type":"image"},{"text":"look","type":"text"}],"role":"user"}]`,
	} {
		if got, _ := json.Marshal(body[key]); string(got) != want {
			t.Errorf("%s: %s, want %s", key, got, want)
		}
	}
	if _, ok := body["system"]; ok {
		t.Error("system sent with no system message")
	}
}

func TestWhatClaudeCannotTakeIsRefusedBeforeSending(t *testing.T) {
	p, f := serve(t, streamWith(toolTurn))
	for name, m := range map[string]llm.Message{
		"audio": {Role: llm.User, Audio: []llm.AudioClip{{MIME: "audio/wav"}}},
		"bmp":   {Role: llm.User, Images: []llm.Image{{MIME: "image/bmp"}}},
	} {
		if _, err := p.Chat(context.Background(), llm.ChatRequest{Model: "m", MaxTokens: 1, Messages: []llm.Message{m}}, nil); !errors.Is(err, llm.ErrUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := p.Chat(context.Background(), llm.ChatRequest{Model: "m", MaxTokens: 1, Messages: []llm.Message{{Role: "narrator", Content: "x"}}}, nil); err == nil {
		t.Error("a role Claude has no place for was sent")
	}
	if n := len(f.got()); n != 0 {
		t.Errorf("%d requests went out", n)
	}
}

// max_tokens is required: the model's own limit caps the default, the lookup
// happens once per model, and one that fails leaves the default in place.
func TestMaxTokensComesFromTheModelWhenNotGiven(t *testing.T) {
	p, f := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models/claude-3-haiku":
			_, _ = io.WriteString(w, `{"id":"claude-3-haiku","max_input_tokens":200000,"max_tokens":4096}`)
		case "/v1/models/claude-opus-5":
			_, _ = io.WriteString(w, `{"id":"claude-opus-5","max_input_tokens":1000000,"max_tokens":128000}`)
		case "/v1/messages":
			streamWith(toolTurn)(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	for _, model := range []string{"claude-3-haiku", "claude-3-haiku", "claude-opus-5", "proxy-model", "proxy-model"} {
		if _, err := p.Chat(ctx, llm.ChatRequest{Model: model, Messages: []llm.Message{{Role: llm.User, Content: "hi"}}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	var sent []string
	lookups := 0
	for _, r := range f.got() {
		if r.path == "/v1/messages" {
			b, _ := json.Marshal(r.body["max_tokens"])
			sent = append(sent, string(b))
		} else {
			lookups++
		}
	}
	if strings.Join(sent, ",") != "4096,4096,16384,16384,16384" || lookups != 3 {
		t.Errorf("max_tokens %v after %d lookups", sent, lookups)
	}
}

func TestErrorsKeepTheirKind(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		kind   error
	}{
		{401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, llm.ErrUnauthorized},
		{403, `{"type":"error","error":{"type":"permission_error","message":"no"}}`, llm.ErrUnauthorized},
		{429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, llm.ErrRateLimited},
		{529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, llm.ErrUnavailable},
		{500, `{"type":"error","error":{"type":"api_error","message":"oops"}}`, llm.ErrUnavailable},
		{404, `{"type":"error","error":{"type":"not_found_error","message":"model: claude-nope"}}`, llm.ErrModelNotFound},
	} {
		p, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			_, _ = io.WriteString(w, c.body)
		})
		_, err := p.Chat(context.Background(), llm.ChatRequest{Model: "m", MaxTokens: 1, Messages: []llm.Message{{Role: llm.User, Content: "x"}}}, nil)
		var pe *llm.ProviderError
		if !errors.Is(err, c.kind) || !errors.As(err, &pe) || pe.Status != c.status || pe.Message == "" || strings.Contains(pe.Message, "{") {
			t.Errorf("%d: %v", c.status, err)
		}
	}
	for _, body := range []string{"404 page not found", `{"type":"error","error":{"type":"not_found_error","message":"Not found: /v2/messages"}}`} {
		p, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, body)
		})
		_, err := p.Chat(context.Background(), llm.ChatRequest{Model: "m", MaxTokens: 1, Messages: []llm.Message{{Role: llm.User, Content: "x"}}}, nil)
		if err == nil || errors.Is(err, llm.ErrModelNotFound) {
			t.Errorf("a proxy at the wrong path is not a missing model: %v", err)
		}
	}
}

func TestAStreamThatFailsIsNotAnAnswer(t *testing.T) {
	start := `{"type":"message_start","message":{"model":"m","usage":{"input_tokens":1}}}`
	text := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"half an"}}`,
	}
	stop := `{"type":"message_stop"}`
	for name, c := range map[string]struct {
		body, says string
		kind       error
	}{
		// Each ends with message_stop, so only the event under test can fail it.
		"overloaded mid-stream":   {sse(append(append([]string{start}, text...), `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, stop)...), "Overloaded", llm.ErrUnavailable},
		"rate limited mid-stream": {sse(append(append([]string{start}, text...), `{"type":"error","error":{"type":"rate_limit_error","message":"Slow down"}}`, stop)...), "Slow down", llm.ErrRateLimited},
		"cut off":                 {sse(append([]string{start}, text...)...), "stream ended", llm.ErrUnavailable},
		"delta for no block":      {sse(start, `{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"x"}}`, stop), "never started", nil},
	} {
		p, _ := serve(t, streamWith(c.body))
		res, err := p.Chat(context.Background(), llm.ChatRequest{Model: "m", MaxTokens: 1, Messages: []llm.Message{{Role: llm.User, Content: "x"}}}, nil)
		if err == nil || res != nil || !strings.Contains(err.Error(), c.says) || (c.kind != nil && !errors.Is(err, c.kind)) {
			t.Errorf("%s: %+v, %v", name, res, err)
		}
	}
}

// A connection dropping mid-frame is the ordinary way a long answer fails,
// and half an event must not read as the end of one.
func TestAConnectionThatDropsMidFrameIsUnavailable(t *testing.T) {
	half := sse(`{"type":"message_start","message":{"model":"m","usage":{"input_tokens":1}}}`) +
		"event: content_block_start\ndata: {\"type\":\"content_bl"
	p, _ := serve(t, streamWith(half))
	_, err := p.Chat(context.Background(), llm.ChatRequest{Model: "m", MaxTokens: 1, Messages: []llm.Message{{Role: llm.User, Content: "x"}}}, nil)
	if !errors.Is(err, llm.ErrUnavailable) {
		t.Errorf("%v", err)
	}
}

// A conversation through storage can come back with its replay emptied, and
// an empty thinking block is a 400 for the whole turn.
func TestAnEmptyReplayFallsBackToTheMessage(t *testing.T) {
	for _, data := range []string{"[]", "null"} {
		p, f := serve(t, streamWith(toolTurn))
		_, err := p.Chat(context.Background(), llm.ChatRequest{Model: "m", MaxTokens: 10, Messages: []llm.Message{
			{Role: llm.User, Content: "q"},
			{Role: llm.Assistant, Content: "on it", ToolCalls: []llm.ToolCall{{ID: "t1", Name: "look", Arguments: `{}`}}, Replay: &llm.Replay{Driver: "anthropic", Data: json.RawMessage(data)}},
			{Role: llm.ToolResult, ToolCallID: "t1", Content: "done"},
		}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		msgs, _ := json.Marshal(f.messages()[0].body["messages"])
		want := `[{"content":[{"text":"q","type":"text"}],"role":"user"},` +
			`{"content":[{"text":"on it","type":"text"},{"id":"t1","input":{},"name":"look","type":"tool_use"}],"role":"assistant"},` +
			`{"content":[{"content":"done","tool_use_id":"t1","type":"tool_result"}],"role":"user"}]`
		if string(msgs) != want {
			t.Errorf("replay %s sent\n %s\nwant\n %s", data, msgs, want)
		}
	}
}

// Server tools stream their input the same way, and the block that carries the
// result points back at it: an empty input leaves a turn that answers itself.
func TestEveryStreamedInputIsKeptNotOnlyACustomTool(t *testing.T) {
	p, _ := serve(t, streamWith(sse(
		`{"type":"message_start","message":{"model":"m","usage":{"input_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"go\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		// Blocks the stream never closes, which a proxy can cause.
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"searching"}}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_9","name":"look","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"q\":1}"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
		`{"type":"message_stop"}`,
	)))
	var calls []string
	res, err := p.Chat(context.Background(), llm.ChatRequest{Model: "m", MaxTokens: 5, Messages: []llm.Message{{Role: llm.User, Content: "q"}}}, func(c llm.Chunk) error {
		if c.Kind == llm.ChunkToolCall {
			calls = append(calls, c.Call.Name+c.Call.Arguments)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The call in the unclosed block still reaches the app, or the model asked
	// for something nobody runs and the turn hangs on a missing result.
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "toolu_9" || res.ToolCalls[0].Arguments != `{"q":1}` || strings.Join(calls, "") != `look{"q":1}` {
		t.Errorf("calls %+v, chunks %q", res.ToolCalls, calls)
	}
	want := `[{"id":"srv_1","input":{"query":"go"},"name":"web_search","type":"server_tool_use"},{"text":"searching","type":"text"},{"id":"toolu_9","input":{"q":1},"name":"look","type":"tool_use"}]`
	if string(res.Replay.Data) != want {
		t.Errorf("replay\n %s\nwant\n %s", res.Replay.Data, want)
	}
	if res.Content != "searching" {
		t.Errorf("content %q", res.Content)
	}
}

// One lookup for a model, however many chats start at once; a lookup that
// failed for the moment is asked again rather than pinning the default.
func TestTheModelLimitIsAskedOnceAndNotPinnedByAFailure(t *testing.T) {
	var lookups, fail atomic.Int32
	p, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/messages":
			streamWith(toolTurn)(w, r)
		case fail.Load() == 1:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"later"}}`)
		case fail.Load() == 2:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"not_found_error","message":"no such endpoint"}}`)
		default:
			lookups.Add(1)
			time.Sleep(20 * time.Millisecond)
			_, _ = io.WriteString(w, `{"id":"m","max_input_tokens":200000,"max_tokens":8192}`)
		}
	})
	chat := func(model string) error {
		_, err := p.Chat(context.Background(), llm.ChatRequest{Model: model, Messages: []llm.Message{{Role: llm.User, Content: "x"}}}, nil)
		return err
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := chat("m"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := lookups.Load(); n != 1 {
		t.Errorf("%d lookups for one model", n)
	}

	fail.Store(1)
	if err := chat("later"); err != nil {
		t.Fatal(err)
	}
	fail.Store(0)
	if err := chat("later"); err != nil {
		t.Fatal(err)
	}
	if n := lookups.Load(); n != 2 {
		t.Errorf("a lookup that failed for the moment was remembered as the default")
	}

	fail.Store(2)
	for range 2 {
		if err := chat("gone"); err != nil {
			t.Fatal(err)
		}
	}
	if n := lookups.Load(); n != 2 {
		t.Errorf("a proxy without the endpoint was asked again: %d lookups", n)
	}
}

func TestAPromptOverTheWindowSaysSo(t *testing.T) {
	p, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 250000 tokens > 200000 maximum"}}`)
	})
	_, err := p.Chat(context.Background(), llm.ChatRequest{Model: "m", MaxTokens: 1, Messages: []llm.Message{{Role: llm.User, Content: "x"}}}, nil)
	if !errors.Is(err, llm.ErrContextFull) {
		t.Errorf("%v", err)
	}
}

// Settings the app passed through Extra are its own; silently dropping one
// means paying for an answer it did not ask for.
func TestAnExtraItCannotMergeIsAnError(t *testing.T) {
	p, f := serve(t, streamWith(toolTurn))
	_, err := p.Chat(context.Background(), llm.ChatRequest{Model: "m", MaxTokens: 1,
		Messages: []llm.Message{{Role: llm.User, Content: "x"}},
		Format:   &llm.ResponseFormat{Schema: json.RawMessage(`{"type":"object"}`)},
		Extra:    map[string]any{"output_config": "max"},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "output_config") {
		t.Errorf("%v", err)
	}
	if n := len(f.got()); n != 0 {
		t.Errorf("%d requests went out", n)
	}
	// The shape it can merge still merges.
	if _, err := p.Chat(context.Background(), llm.ChatRequest{Model: "m", MaxTokens: 1,
		Messages: []llm.Message{{Role: llm.User, Content: "x"}},
		Format:   &llm.ResponseFormat{Schema: json.RawMessage(`{"type":"object"}`)},
		Extra:    map[string]any{"output_config": map[string]any{"effort": "max"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	oc, _ := json.Marshal(f.messages()[0].body["output_config"])
	if string(oc) != `{"effort":"max","format":{"schema":{"type":"object"},"type":"json_schema"}}` {
		t.Errorf("output_config %s", oc)
	}
}

func TestAStopFromTheAppEndsTheStream(t *testing.T) {
	p, _ := serve(t, streamWith(toolTurn))
	stop := errors.New("client left")
	_, err := p.Chat(context.Background(), llm.ChatRequest{Model: "m", MaxTokens: 1, Messages: []llm.Message{{Role: llm.User, Content: "x"}}}, func(llm.Chunk) error { return stop })
	if !errors.Is(err, stop) {
		t.Errorf("%v", err)
	}
}

func TestModelsAreListedAcrossPages(t *testing.T) {
	p, f := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("after_id") {
		case "":
			_, _ = io.WriteString(w, `{"data":[{"id":"claude-opus-5","created_at":"2026-07-24T00:00:00Z"}],"has_more":true,"last_id":"claude-opus-5"}`)
		case "claude-opus-5":
			_, _ = io.WriteString(w, `{"data":[{"id":"claude-haiku-4-5","created_at":"2025-10-01T00:00:00Z"}],"has_more":false,"last_id":"claude-haiku-4-5"}`)
		}
	})
	ms, err := p.Models(context.Background())
	if err != nil || len(ms) != 2 || ms[0].ID != "claude-opus-5" || ms[1].ID != "claude-haiku-4-5" || ms[0].Modified.Year() != 2026 {
		t.Errorf("%+v, %v", ms, err)
	}
	if q := f.got()[0].query; !strings.Contains(q, "limit=1000") {
		t.Errorf("query %q", q)
	}
}

func TestAPageThatNeverEndsStops(t *testing.T) {
	p, f := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"a"}],"has_more":true,"last_id":"a"}`)
	})
	if _, err := p.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(f.got()); n != 2 {
		t.Errorf("%d requests for a cursor that does not move", n)
	}
}

func TestDescribeSaysWhatTheAPISays(t *testing.T) {
	p, f := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "blind") {
			_, _ = io.WriteString(w, `{"id":"blind","max_input_tokens":8000,"capabilities":{"image_input":{"supported":false}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"claude-opus-5","max_input_tokens":1000000,"max_tokens":128000,"capabilities":{"image_input":{"supported":true}}}`)
	})
	d, err := p.Describe(context.Background(), "claude-opus-5")
	if err != nil || d.ContextLength != 1000000 || d.MaxOutputTokens != 128000 || !hasCap(d.Capabilities, llm.Vision) || !hasCap(d.Capabilities, llm.Tools) {
		t.Errorf("%+v, %v", d, err)
	}
	if b, _ := p.Describe(context.Background(), "blind"); b == nil || hasCap(b.Capabilities, llm.Vision) {
		t.Errorf("a model without image input was described as seeing: %+v", b)
	}
	if path := f.got()[0].path; path != "/v1/models/claude-opus-5" {
		t.Errorf("path %s", path)
	}
}

func hasCap(cs []llm.Capability, c llm.Capability) bool {
	for _, x := range cs {
		if x == c {
			return true
		}
	}
	return false
}

func TestCountTokensSendsOnlyWhatTheEndpointTakes(t *testing.T) {
	p, f := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"input_tokens":403}`)
	})
	temp := 1.0
	n, err := p.CountTokens(context.Background(), llm.ChatRequest{
		Model: "claude-opus-5", MaxTokens: 99, Temperature: &temp, Stop: []string{"x"},
		Messages: []llm.Message{{Role: llm.System, Content: "s"}, {Role: llm.User, Content: "weather?"}},
		Tools:    []llm.Tool{{Name: "get_weather"}},
		Extra:    map[string]any{"thinking": map[string]any{"type": "adaptive"}, "metadata": "x"},
	})
	if err != nil || n != 403 {
		t.Fatalf("%d, %v", n, err)
	}
	r := f.got()[0]
	if r.path != "/v1/messages/count_tokens" {
		t.Errorf("path %s", r.path)
	}
	var keys []string
	for k := range r.body {
		keys = append(keys, k)
	}
	for _, k := range []string{"model", "messages", "system", "tools", "thinking"} {
		if _, ok := r.body[k]; !ok {
			t.Errorf("%s missing from %v", k, keys)
		}
	}
	for _, k := range []string{"max_tokens", "temperature", "stop_sequences", "stream", "metadata"} {
		if _, ok := r.body[k]; ok {
			t.Errorf("%s sent to count_tokens, which refuses it", k)
		}
	}
	bad, _ := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{}`) })
	if _, err := bad.CountTokens(context.Background(), llm.ChatRequest{Model: "m"}); err == nil {
		t.Error("a reply without a count read as zero tokens")
	}
}

func TestAKeyIsNotSentInTheClearToAnotherMachine(t *testing.T) {
	p, err := New(Config{ID: "c", BaseURL: "http://192.0.2.1/v1", Key: StaticKey("sk")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Models(context.Background()); err == nil || !strings.Contains(err.Error(), "plain http") {
		t.Errorf("%v", err)
	}
	if p.Local() {
		t.Error("another machine is not local")
	}
}

func TestTheDriverOpensWithItsDefaults(t *testing.T) {
	prov, err := llm.Open("anthropic", llm.ProviderConfig{ID: "work", Secret: func(_ context.Context, key string) (string, error) {
		if key != "api_key" {
			return "", errors.New("no secret " + key)
		}
		return "sk", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	p := prov.(*Provider)
	if p.base.String() != DefaultBaseURL || p.Local() || !llm.Has(p, llm.Tools) || !llm.Has(p, llm.Vision) || llm.Has(p, llm.Embed) || llm.Has(p, llm.Audio) {
		t.Errorf("%s %v", p.base, p.caps)
	}
	if key, _ := p.key(context.Background()); key != "sk" {
		t.Errorf("key %q", key)
	}
	if _, err := New(Config{ID: "x", BaseURL: "ftp://h"}); err == nil {
		t.Error("an ftp base URL")
	}
	if _, err := New(Config{BaseURL: DefaultBaseURL}); err == nil {
		t.Error("an empty ID")
	}
	for _, u := range []string{"http://127.0.0.1:4000/v1", "http://localhost:4000/v1"} {
		if lp, _ := New(Config{ID: "proxy", BaseURL: u}); !lp.Local() {
			t.Errorf("a proxy at %s is on this machine", u)
		}
	}
}

// Go strips Authorization when a redirect leaves the domain but knows
// nothing about x-api-key. httptest is all loopback, where a key over
// plain http is allowed, so this needs stub transports.
func TestTheKeyDoesNotFollowARedirect(t *testing.T) {
	for _, tc := range []struct{ name, to string }{
		{"another host", "https://elsewhere.example.com/v1/messages"},
		{"plain http on the same host", "http://gw.example.com/v1/messages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hops []*http.Request
			rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				hops = append(hops, r)
				rec := httptest.NewRecorder()
				if len(hops) == 1 {
					rec.Header().Set("Location", tc.to)
					rec.WriteHeader(http.StatusTemporaryRedirect)
				} else {
					rec.Header().Set("Content-Type", "text/event-stream")
					_, _ = rec.WriteString(sse(`{"type":"message_stop"}`))
				}
				resp := rec.Result()
				resp.Request = r
				return resp, nil
			})
			p, err := New(Config{ID: "claude", BaseURL: "https://gw.example.com/v1", Key: StaticKey("sk-ant-secret"),
				HTTPClient: &http.Client{Transport: rt}})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = p.Chat(context.Background(), llm.ChatRequest{Model: "claude-opus-5", MaxTokens: 16}, nil)
			if len(hops) != 2 {
				t.Fatalf("hops = %d", len(hops))
			}
			if hops[0].Header.Get("X-Api-Key") == "" {
				t.Error("the key never went out at all")
			}
			got := hops[1].Header
			if got.Get("X-Api-Key") != "" || got.Get("Authorization") != "" {
				t.Errorf("the key reached %s: %q %q", tc.to, got.Get("X-Api-Key"), got.Get("Authorization"))
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// 429 and 529 are what a hosted API hands out under load.
func TestARateLimitIsSentAgain(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, 529} {
		var hits int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			if hits < 3 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(code)
				_, _ = io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"try later"}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, sse(
				`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-opus-5","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"42"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
				`{"type":"message_stop"}`))
		}))
		p, err := New(Config{ID: "claude", BaseURL: srv.URL + "/v1", Key: StaticKey("sk"),
			Retry: llm.RetryPolicy{Attempts: 3, Base: time.Millisecond, Max: time.Millisecond}})
		if err != nil {
			t.Fatal(err)
		}
		res, err := p.Chat(context.Background(), llm.ChatRequest{Model: "claude-opus-5", MaxTokens: 16,
			Messages: []llm.Message{{Role: llm.User, Content: "x"}}}, nil)
		if err != nil || res.Content != "42" {
			t.Errorf("%d: res %+v, err %v", code, res, err)
		}
		if hits != 3 {
			t.Errorf("%d: sent %d times", code, hits)
		}
		srv.Close()
	}
}

// The body is read once, so a retry that reuses the reader sends an empty one
// and the API answers about a request nobody made.
func TestARetriedRequestSendsItsBodyAgain(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(bodies) < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(`{"type":"message_stop"}`))
	}))
	defer srv.Close()
	p, err := New(Config{ID: "claude", BaseURL: srv.URL + "/v1", Key: StaticKey("sk"),
		Retry: llm.RetryPolicy{Attempts: 2, Base: time.Millisecond, Max: time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = p.Chat(context.Background(), llm.ChatRequest{Model: "claude-opus-5", MaxTokens: 16,
		Messages: []llm.Message{{Role: llm.User, Content: "x"}}}, nil)
	if len(bodies) != 2 || bodies[0] == "" || bodies[0] != bodies[1] {
		t.Errorf("bodies = %q", bodies)
	}
}

// An agent loop resends the whole conversation every step, which is where
// caching pays - and the two marks have to land in the right places.
func TestCacheMarksWhatIsWorthKeeping(t *testing.T) {
	p, f := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(`{"type":"message_stop"}`))
	})
	req := llm.ChatRequest{Model: "claude-opus-5", MaxTokens: 16, Cache: true, Messages: []llm.Message{
		{Role: llm.System, Content: "be brief"},
		{Role: llm.User, Content: "one"},
		{Role: llm.Assistant, Content: "two"},
		{Role: llm.User, Content: "three"},
	}}
	if _, err := p.Chat(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	body := f.messages()[0].body

	system, _ := body["system"].([]any)
	if len(system) == 0 {
		t.Fatalf("no system in %v", body)
	}
	if cc, _ := system[len(system)-1].(map[string]any)["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Errorf("the system prompt is not marked: %v", system)
	}

	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("no messages in %v", body)
	}
	last, _ := msgs[len(msgs)-1].(map[string]any)
	blocks, _ := last["content"].([]any)
	if len(blocks) == 0 {
		t.Fatalf("no content in %v", last)
	}
	block, _ := blocks[len(blocks)-1].(map[string]any)
	if cc, _ := block["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Errorf("the end of the conversation is not marked: %v", last)
	}
	// Only the end: a mark in the middle would cut the cache short there.
	for i, m := range msgs[:len(msgs)-1] {
		if strings.Contains(mustJSON(t, m), "cache_control") {
			t.Errorf("message %d is marked too: %v", i, m)
		}
	}
}

// Off by default, because a cache write costs more than a plain read and an
// app that asks one question does not get it back.
func TestNothingIsMarkedUnlessAsked(t *testing.T) {
	p, f := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(`{"type":"message_stop"}`))
	})
	if _, err := p.Chat(context.Background(), llm.ChatRequest{Model: "claude-opus-5", MaxTokens: 16,
		Messages: []llm.Message{{Role: llm.System, Content: "be brief"}, {Role: llm.User, Content: "one"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := mustJSON(t, f.messages()[0].body); strings.Contains(got, "cache_control") {
		t.Errorf("marked without being asked: %s", got)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Every thinking block has to go back exactly as it came: adding anything
// to one is a permanent 400 on a resumed turn, and only with caching on.
func TestCacheNeverMarksAThinkingBlock(t *testing.T) {
	thinking := json.RawMessage(`{"type":"thinking","thinking":"1 < 2","signature":"EqQBCgIYAhIM"}`)
	text := json.RawMessage(`{"type":"text","text":"so far"}`)

	for _, tc := range []struct {
		name   string
		blocks []json.RawMessage
		marked int // index that should carry the field, -1 for none
	}{
		{"a turn that ends in thinking", []json.RawMessage{text, thinking}, 0},
		{"a turn that is only thinking", []json.RawMessage{thinking}, -1},
		{"a turn that ends in text", []json.RawMessage{thinking, text}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgs := []wireMessage{{Role: "assistant", Content: append([]json.RawMessage(nil), tc.blocks...)}}
			if err := markLast(msgs); err != nil {
				t.Fatal(err)
			}
			for i, b := range msgs[0].Content {
				has := strings.Contains(string(b), "cache_control")
				if has != (i == tc.marked) {
					t.Errorf("block %d marked = %v: %s", i, has, b)
				}
			}
			// Whatever was not marked has to be byte for byte what arrived.
			for i, b := range msgs[0].Content {
				if i != tc.marked && string(b) != string(tc.blocks[i]) {
					t.Errorf("block %d changed:\n%s\n%s", i, tc.blocks[i], b)
				}
			}
			// And the signature survives the round trip through the map.
			if tc.marked >= 0 {
				if got := string(msgs[0].Content[tc.marked]); strings.Contains(string(tc.blocks[tc.marked]), "signature") && !strings.Contains(got, "EqQBCgIYAhIM") {
					t.Errorf("the signature was lost: %s", got)
				}
			}
		})
	}
}

// Two turns of a tool loop, to see the mark move rather than pile up.
func TestTheCacheMarkMovesToTheEndOfEachTurn(t *testing.T) {
	p, f := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(`{"type":"message_stop"}`))
	})
	first := llm.ChatRequest{Model: "claude-opus-5", MaxTokens: 16, Cache: true, Messages: []llm.Message{
		{Role: llm.System, Content: "be brief"},
		{Role: llm.User, Content: "weather?"},
	}}
	if _, err := p.Chat(context.Background(), first, nil); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Messages = append(append([]llm.Message(nil), first.Messages...),
		llm.Message{Role: llm.Assistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "weather", Arguments: "{}"}}},
		llm.Message{Role: llm.ToolResult, ToolCallID: "c1", Content: "rain"})
	if _, err := p.Chat(context.Background(), second, nil); err != nil {
		t.Fatal(err)
	}

	sent := f.messages()
	if len(sent) != 2 {
		t.Fatalf("%d requests", len(sent))
	}
	for i, req := range sent {
		msgs, _ := req.body["messages"].([]any)
		var marked []int
		for j, m := range msgs {
			if strings.Contains(mustJSON(t, m), "cache_control") {
				marked = append(marked, j)
			}
		}
		// Exactly one, and the last: more than one would spend a breakpoint
		// per turn, and an earlier one would cut the cache short there.
		if len(marked) != 1 || marked[0] != len(msgs)-1 {
			t.Errorf("turn %d marked %v of %d messages", i+1, marked, len(msgs))
		}
	}
}
