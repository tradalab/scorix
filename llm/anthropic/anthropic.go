// Package anthropic talks Anthropic's Messages API. Claude does not speak the
// OpenAI protocol, and the parts that differ are the ones an agent loop leans
// on: signed thinking that has to come back with the tool results, tool results
// carried in the user turn, and a system prompt kept outside the messages.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/tradalab/scorix/llm"
)

const (
	DefaultBaseURL = "https://api.anthropic.com/v1"
	apiVersion     = "2023-06-01"
	driverName     = "anthropic"
	// The API requires max_tokens and has no default; thinking spends from the
	// same allowance.
	defaultMaxTokens = 16384
)

// Called per request, so an app can keep the key sealed and change it
// without rebuilding the provider.
type KeySource func(ctx context.Context) (string, error)

func StaticKey(key string) KeySource {
	return func(context.Context) (string, error) { return key, nil }
}

type Config struct {
	ID string
	// Up to and including the version segment; empty is DefaultBaseURL.
	BaseURL string
	// Nil for a proxy that adds the key itself.
	Key KeySource
	// Defaults to chat, vision and tools, which every current Claude model has.
	Capabilities []llm.Capability
	HTTPClient   *http.Client
	// Zero takes llm.DefaultRetry.
	Retry llm.RetryPolicy
}

type Provider struct {
	id   string
	base *url.URL
	key  KeySource
	caps []llm.Capability
	hc   *http.Client
	// The transport this provider created, if it did; see New.
	owned *http.Transport
	retry llm.RetryPolicy

	mu     sync.Mutex
	maxOut map[string]int
}

func New(cfg Config) (*Provider, error) {
	if cfg.ID == "" {
		return nil, fmt.Errorf("anthropic: empty provider ID")
	}
	raw := cfg.BaseURL
	if raw == "" {
		raw = DefaultBaseURL
	}
	base, err := url.Parse(raw)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, fmt.Errorf("anthropic %s: base URL %q is not an http(s) URL", cfg.ID, raw)
	}
	caps := cfg.Capabilities
	if len(caps) == 0 {
		caps = []llm.Capability{llm.Chat, llm.Vision, llm.Tools}
	}
	// No overall timeout: it would cut off a long generation, and the caller's
	// ctx is the deadline. Wrapped either way, so the key cannot follow a
	// redirect off this host or off https.
	var owned *http.Transport
	hc := llm.KeySafeClient(cfg.HTTPClient)
	if cfg.HTTPClient == nil {
		// A hosted API answers with headers long before it finishes writing, so a
		// connection that says nothing is a black hole. Not a proxy on this
		// machine or the LAN, which may be loading a model.
		hc = llm.HeaderTimeout(hc, llm.IsPrivateHost(base.Hostname()))
		// A transport here means HeaderTimeout cloned one, which is the only
		// kind this provider may close.
		owned, _ = hc.Transport.(*http.Transport)
	}
	retry := cfg.Retry
	if retry.Attempts == 0 {
		// Timing only: replacing the policy would drop OnRetry, see retry.go.
		retry.Attempts, retry.Base, retry.Max = llm.DefaultRetry.Attempts, llm.DefaultRetry.Base, llm.DefaultRetry.Max
	}
	return &Provider{id: cfg.ID, base: base, key: cfg.Key, caps: caps, hc: hc, owned: owned, retry: retry, maxOut: map[string]int{}}, nil
}

func (p *Provider) ID() string { return p.id }

// Releases the idle connections of the transport this provider created; the
// registry calls it when the provider leaves.
func (p *Provider) Close() error {
	if p.owned != nil {
		p.owned.CloseIdleConnections()
	}
	return nil
}

func (p *Provider) Capabilities() []llm.Capability { return p.caps }

// A proxy on this machine counts; api.anthropic.com never does.
func (p *Provider) Local() bool { return llm.IsLoopbackHost(p.base.Hostname()) }

func init() { llm.RegisterDriver(Driver{}) }

type Driver struct{}

func (Driver) Name() string { return driverName }

func (Driver) Fields() []llm.Field {
	return []llm.Field{
		{Key: "base_url", Label: "Base URL", Kind: llm.FieldURL, Required: true, Default: DefaultBaseURL, Description: "Up to and including the version; change it only for a proxy"},
		{Key: "api_key", Label: "API key", Kind: llm.FieldSecret, Description: "Required for api.anthropic.com"},
	}
}

func (Driver) Open(cfg llm.ProviderConfig) (llm.Provider, error) {
	var key KeySource
	if cfg.Secret != nil {
		key = func(ctx context.Context) (string, error) { return cfg.Secret(ctx, "api_key") }
	}
	return New(Config{ID: cfg.ID, BaseURL: cfg.Values["base_url"], Key: key, Retry: llm.RetryPolicy{OnRetry: cfg.OnRetry}})
}

type wireMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

type textBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// Where the part worth keeping ends: Anthropic caches everything up to a
	// block carrying this and nothing after it.
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
}

func ephemeral() *cacheControl { return &cacheControl{Type: "ephemeral"} }

type imageBlock struct {
	Type   string `json:"type"`
	Source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"`
}

type toolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type toolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

var imageTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// System messages leave the list for the top-level system field, and runs
// of the same role are merged: the API refuses two in a row.
func toWire(msgs []llm.Message) ([]textBlock, []wireMessage, error) {
	var system []textBlock
	var out []wireMessage
	add := func(role string, blocks []json.RawMessage) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			return
		}
		out = append(out, wireMessage{Role: role, Content: blocks})
	}
	for i, m := range msgs {
		switch m.Role {
		case llm.System:
			if m.Content != "" {
				system = append(system, textBlock{Type: "text", Text: m.Content})
			}
		case llm.User:
			if len(m.Audio) > 0 {
				return nil, nil, fmt.Errorf("message %d: Claude cannot hear audio: %w", i, llm.ErrUnsupported)
			}
			var blocks []json.RawMessage
			// Images first: Anthropic reads them best ahead of the question.
			for _, img := range m.Images {
				if !imageTypes[img.MIME] {
					return nil, nil, fmt.Errorf("message %d: Claude takes jpeg, png, gif or webp, not %s: %w", i, img.MIME, llm.ErrUnsupported)
				}
				var b imageBlock
				b.Type, b.Source.Type, b.Source.MediaType, b.Source.Data = "image", "base64", img.MIME, base64.StdEncoding.EncodeToString(img.Data)
				blocks = append(blocks, raw(b))
			}
			// An empty text block is a 400, not a no-op.
			if m.Content != "" {
				blocks = append(blocks, raw(textBlock{Type: "text", Text: m.Content}))
			}
			add("user", blocks)
		case llm.Assistant:
			if m.Replay != nil && m.Replay.Driver == driverName {
				var blocks []json.RawMessage
				if err := json.Unmarshal(m.Replay.Data, &blocks); err != nil {
					return nil, nil, fmt.Errorf("message %d: replay: %w", i, err)
				}
				// A replay emptied by storage or an edit would take the whole turn with it.
				if len(blocks) > 0 {
					add("assistant", blocks)
					continue
				}
			}
			var blocks []json.RawMessage
			if m.Content != "" {
				blocks = append(blocks, raw(textBlock{Type: "text", Text: m.Content}))
			}
			for _, c := range m.ToolCalls {
				blocks = append(blocks, raw(toolUseBlock{Type: "tool_use", ID: c.ID, Name: c.Name, Input: inputObject(c.Arguments)}))
			}
			add("assistant", blocks)
		case llm.ToolResult:
			add("user", []json.RawMessage{raw(toolResultBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content, IsError: m.Failed})})
		default:
			return nil, nil, fmt.Errorf("message %d: role %q", i, m.Role)
		}
	}
	return system, out, nil
}

// Claude's tool input is an object: a call written by another provider
// still has to go out as one.
func inputObject(args string) json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(args), &obj) != nil || obj == nil {
		return json.RawMessage("{}")
	}
	return json.RawMessage(args)
}

func (p *Provider) body(ctx context.Context, req llm.ChatRequest, counting bool) (map[string]any, error) {
	system, msgs, err := toWire(req.Messages)
	if err != nil {
		return nil, fmt.Errorf("llm %s: %w", p.id, err)
	}
	body := map[string]any{}
	if counting {
		// count_tokens refuses fields it does not take; thinking changes the count.
		if t, ok := req.Extra["thinking"]; ok {
			body["thinking"] = t
		}
	} else {
		for k, v := range req.Extra {
			body[k] = v
		}
		body["max_tokens"] = req.MaxTokens
		if req.MaxTokens <= 0 {
			body["max_tokens"] = p.maxTokens(ctx, req.Model)
		}
		if req.Temperature != nil {
			body["temperature"] = *req.Temperature
		}
		if len(req.Stop) > 0 {
			body["stop_sequences"] = req.Stop
		}
		if f := req.Format; f != nil {
			oc, ok := body["output_config"].(map[string]any)
			if body["output_config"] != nil && !ok {
				// Dropping it silently would answer at a setting the app did not choose,
				// and charge for it.
				return nil, fmt.Errorf("llm %s: output_config from Extra is %T, which cannot carry the response format", p.id, body["output_config"])
			}
			merged := map[string]any{}
			for k, v := range oc {
				merged[k] = v
			}
			merged["format"] = map[string]any{"type": "json_schema", "schema": f.Schema}
			body["output_config"] = merged
		}
	}
	if req.Cache && !counting {
		// Two marks, which is what an agent loop needs: after the system prompt,
		// and at the end of the conversation so far. Two of the four the API
		// allows; the others are spare, not reserved - a caller could not place
		// one anyway.
		if n := len(system); n > 0 {
			system[n-1].CacheControl = ephemeral()
		}
		if err := markLast(msgs); err != nil {
			return nil, fmt.Errorf("llm %s: %w", p.id, err)
		}
	}
	body["model"] = req.Model
	body["messages"] = msgs
	if len(system) > 0 {
		body["system"] = system
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			schema := t.Parameters
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			tools[i] = map[string]any{"name": t.Name, "input_schema": schema}
			if t.Description != "" {
				tools[i]["description"] = t.Description
			}
		}
		body["tools"] = tools
		switch req.ToolChoice {
		case llm.ToolAuto:
		case llm.ToolNone:
			body["tool_choice"] = map[string]string{"type": "none"}
		case llm.ToolRequired:
			body["tool_choice"] = map[string]string{"type": "any"}
		default:
			body["tool_choice"] = map[string]string{"type": "tool", "name": string(req.ToolChoice)}
		}
	}
	return body, nil
}

// Asked once per model, with the lock held so eight chats starting together
// ask once between them - which is why the lookup below does not retry. A
// lookup that failed for the moment is not remembered: pinning the default
// would cap every later answer.
func (p *Provider) maxTokens(ctx context.Context, model string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n, ok := p.maxOut[model]; ok {
		return n
	}
	n := defaultMaxTokens
	out, err := p.modelMaxOut(ctx, model)
	switch {
	case err == nil:
		if out > 0 && out < n {
			n = out
		}
	case !hasNoModelEndpoint(err):
		return n
	}
	p.maxOut[model] = n
	return n
}

// One attempt, deliberately not p.retry: the answer has a safe fallback and
// the caller holds the lock across it, so riding out a rate limit here
// would stall every chat on that model.
func (p *Provider) modelMaxOut(ctx context.Context, model string) (int, error) {
	resp, err := p.send(ctx, http.MethodGet, "models/"+url.PathEscape(model), nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 0, p.statusError(resp)
	}
	var m wireModel
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return 0, llm.Unreadable(p.id, "unreadable reply", err)
	}
	return m.MaxTokens, nil
}

// A 404 or 405 is an endpoint that is not there and will not be on the
// next chat either.
func hasNoModelEndpoint(err error) bool {
	var pe *llm.ProviderError
	return errors.As(err, &pe) && (pe.Status == http.StatusNotFound || pe.Status == http.StatusMethodNotAllowed)
}

func (p *Provider) Chat(ctx context.Context, req llm.ChatRequest, onChunk func(llm.Chunk) error) (*llm.ChatResult, error) {
	body, err := p.body(ctx, req, false)
	if err != nil {
		return nil, err
	}
	body["stream"] = true
	resp, err := p.do(ctx, http.MethodPost, "messages", body)
	if err != nil {
		return nil, err
	}
	// Closing the body is how a stop reaches the server.
	defer resp.Body.Close()
	return p.readStream(ctx, resp.Body, req.Model, onChunk)
}

type wireUsage struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
}

type wireError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type event struct {
	Type    string `json:"type"`
	Message struct {
		Model string    `json:"model"`
		Usage wireUsage `json:"usage"`
	} `json:"message"`
	Index        int             `json:"index"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *wireUsage `json:"usage"`
	Error *wireError `json:"error"`
}

type block struct {
	fields map[string]json.RawMessage
	kind   string
	done   bool
	text   strings.Builder
	sig    strings.Builder
	input  strings.Builder
}

func (b *block) set(key string, v any) { b.fields[key] = raw(v) }

// Folds what was streamed into the block as the API would have sent it
// whole, so a custom tool sees one shape.
func (b *block) finish() *llm.ToolCall {
	if b.done {
		return nil
	}
	b.done = true
	switch b.kind {
	case "text":
		b.set("text", b.text.String())
	case "thinking":
		// Sent back even when empty: with display omitted, that is what Claude
		// expects to see again.
		b.set("thinking", b.text.String())
		b.set("signature", b.sig.String())
	}
	if b.input.Len() > 0 || b.kind == "tool_use" {
		args := b.input.String()
		if args == "" {
			args = "{}"
		}
		b.fields["input"] = inputObject(args)
		if b.kind == "tool_use" {
			var call llm.ToolCall
			_ = json.Unmarshal(b.fields["id"], &call.ID)
			_ = json.Unmarshal(b.fields["name"], &call.Name)
			call.Arguments = args
			return &call
		}
	}
	return nil
}

func (p *Provider) readStream(ctx context.Context, body io.Reader, model string, onChunk func(llm.Chunk) error) (*llm.ChatResult, error) {
	res := &llm.ChatResult{Model: model}
	var in, out, cacheWrite, cacheRead int
	usage := func(u *wireUsage) {
		if u == nil {
			return
		}
		// Counts in message_delta are running totals, not increments.
		for _, f := range []struct {
			v   *int
			dst *int
		}{{u.InputTokens, &in}, {u.OutputTokens, &out}, {u.CacheCreationInputTokens, &cacheWrite}, {u.CacheReadInputTokens, &cacheRead}} {
			if f.v != nil {
				*f.dst = *f.v
			}
		}
	}
	emit := func(c llm.Chunk) error {
		if onChunk == nil {
			return nil
		}
		return onChunk(c)
	}
	var text, reasoning strings.Builder
	var order []*block
	byIndex := map[int]*block{}
	finished := false

	handle := func(data []byte) error {
		var ev event
		if err := json.Unmarshal(data, &ev); err != nil {
			return &llm.ProviderError{Provider: p.id, Message: "unreadable stream event: " + err.Error()}
		}
		switch ev.Type {
		case "message_start":
			if ev.Message.Model != "" {
				res.Model = ev.Message.Model
			}
			usage(&ev.Message.Usage)
		case "content_block_start":
			b := &block{fields: map[string]json.RawMessage{}}
			if err := json.Unmarshal(ev.ContentBlock, &b.fields); err != nil {
				return &llm.ProviderError{Provider: p.id, Message: "unreadable content block: " + err.Error()}
			}
			_ = json.Unmarshal(b.fields["type"], &b.kind)
			byIndex[ev.Index] = b
			order = append(order, b)
		case "content_block_delta":
			b := byIndex[ev.Index]
			if b == nil {
				return &llm.ProviderError{Provider: p.id, Message: fmt.Sprintf("delta for block %d, which never started", ev.Index)}
			}
			switch ev.Delta.Type {
			case "text_delta":
				b.text.WriteString(ev.Delta.Text)
				text.WriteString(ev.Delta.Text)
				if ev.Delta.Text != "" {
					return emit(llm.Chunk{Kind: llm.ChunkText, Text: ev.Delta.Text})
				}
			case "thinking_delta":
				b.text.WriteString(ev.Delta.Thinking)
				reasoning.WriteString(ev.Delta.Thinking)
				if ev.Delta.Thinking != "" {
					return emit(llm.Chunk{Kind: llm.ChunkReasoning, Text: ev.Delta.Thinking})
				}
			case "signature_delta":
				b.sig.WriteString(ev.Delta.Signature)
			case "input_json_delta":
				b.input.WriteString(ev.Delta.PartialJSON)
			}
		case "content_block_stop":
			b := byIndex[ev.Index]
			if b == nil {
				return nil
			}
			if call := b.finish(); call != nil {
				res.ToolCalls = append(res.ToolCalls, *call)
				return emit(llm.Chunk{Kind: llm.ChunkToolCall, Call: call})
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				res.FinishReason = ev.Delta.StopReason
			}
			usage(ev.Usage)
		case "message_stop":
			finished = true
		case "error":
			if ev.Error == nil {
				return &llm.ProviderError{Provider: p.id, Message: "error event without an error"}
			}
			return &llm.ProviderError{Provider: p.id, Message: ev.Error.Message, Kind: kindOf(ev.Error.Type, ev.Error.Message)}
		}
		return nil
	}

	br := bufio.NewReader(body)
	var data bytes.Buffer
	for !finished {
		line, readErr := br.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if d, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(d, " "))
		}
		// A blank line ends an event, and so does the end of the stream - but only
		// if what arrived is a whole one.
		if (line == "" || readErr != nil) && data.Len() > 0 && (line == "" || json.Valid(data.Bytes())) {
			if err := handle(data.Bytes()); err != nil {
				return nil, err
			}
			data.Reset()
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("llm %s: %w", p.id, ctx.Err())
			}
			return nil, &llm.ProviderError{Provider: p.id, Message: "stream broke: " + readErr.Error(), Kind: llm.ErrUnavailable}
		}
	}
	// A connection that closes cleanly mid-answer reads exactly like the end of one.
	if !finished {
		return nil, &llm.ProviderError{Provider: p.id, Message: "stream ended before the answer did", Kind: llm.ErrUnavailable}
	}
	// A proxy can end a stream with a block still open; what it carried is
	// still what the user saw.
	for _, b := range order {
		if call := b.finish(); call != nil {
			res.ToolCalls = append(res.ToolCalls, *call)
			if err := emit(llm.Chunk{Kind: llm.ChunkToolCall, Call: call}); err != nil {
				return nil, err
			}
		}
	}
	res.Content, res.Reasoning = text.String(), reasoning.String()
	res.Usage = llm.Usage{PromptTokens: in + cacheWrite + cacheRead, CacheReadTokens: cacheRead, CacheWriteTokens: cacheWrite, CompletionTokens: out}
	if len(order) > 0 {
		blocks := make([]map[string]json.RawMessage, len(order))
		for i, b := range order {
			blocks[i] = b.fields
		}
		res.Replay = &llm.Replay{Driver: driverName, Data: raw(blocks)}
	}
	return res, nil
}

type wireModel struct {
	ID             string    `json:"id"`
	CreatedAt      time.Time `json:"created_at"`
	MaxInputTokens int       `json:"max_input_tokens"`
	MaxTokens      int       `json:"max_tokens"`
	Capabilities   *struct {
		ImageInput struct {
			Supported bool `json:"supported"`
		} `json:"image_input"`
	} `json:"capabilities"`
}

func (p *Provider) Models(ctx context.Context) ([]llm.ModelInfo, error) {
	var models []llm.ModelInfo
	after := ""
	for {
		path := "models?limit=1000"
		if after != "" {
			path += "&after_id=" + url.QueryEscape(after)
		}
		var page struct {
			Data    []wireModel `json:"data"`
			HasMore bool        `json:"has_more"`
			LastID  string      `json:"last_id"`
		}
		if err := p.getJSON(ctx, path, &page); err != nil {
			return nil, err
		}
		for _, m := range page.Data {
			models = append(models, llm.ModelInfo{ID: m.ID, Modified: m.CreatedAt})
		}
		// A page that says there is more but names no cursor would loop forever.
		if !page.HasMore || page.LastID == "" || page.LastID == after {
			return models, nil
		}
		after = page.LastID
	}
}

func (p *Provider) Describe(ctx context.Context, model string) (*llm.ModelDetail, error) {
	var m wireModel
	if err := p.getJSON(ctx, "models/"+url.PathEscape(model), &m); err != nil {
		return nil, err
	}
	d := &llm.ModelDetail{ID: m.ID, ContextLength: m.MaxInputTokens, MaxOutputTokens: m.MaxTokens, Capabilities: []llm.Capability{llm.Chat, llm.Tools}}
	if m.Capabilities != nil && m.Capabilities.ImageInput.Supported {
		d.Capabilities = append(d.Capabilities, llm.Vision)
	}
	return d, nil
}

func (p *Provider) CountTokens(ctx context.Context, req llm.ChatRequest) (int, error) {
	body, err := p.body(ctx, req, true)
	if err != nil {
		return 0, err
	}
	resp, err := p.do(ctx, http.MethodPost, "messages/count_tokens", body)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out struct {
		InputTokens *int `json:"input_tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, llm.Unreadable(p.id, "unreadable token count", err)
	}
	if out.InputTokens == nil {
		// Told apart from a decode failure: the reply parsed, it just did not carry
		// the number that was asked for.
		return 0, &llm.ProviderError{Provider: p.id, Message: "the token count is missing from the reply"}
	}
	return *out.InputTokens, nil
}

func (p *Provider) getJSON(ctx context.Context, path string, v any) error {
	resp, err := p.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return llm.Unreadable(p.id, "unreadable reply", err)
	}
	return nil
}

// One call from the caller's side, however many times it goes out.
func (p *Provider) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var buf []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("llm %s: encode request: %w", p.id, err)
		}
		buf = b
	}
	resp, err := llm.Retry(ctx, p.retry, func() (*http.Response, error) {
		return p.send(ctx, method, path, buf)
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, p.statusError(resp)
	}
	return resp, nil
}

// One attempt. The body is bytes, not a reader: a reader is read once and a
// retry needs a request of its own.
func (p *Provider) send(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	target, err := p.base.Parse(strings.TrimSuffix(p.base.Path, "/") + "/" + path)
	if err != nil {
		return nil, fmt.Errorf("llm %s: %w", p.id, err)
	}
	r, err := http.NewRequestWithContext(ctx, method, target.String(), rd)
	if err != nil {
		return nil, fmt.Errorf("llm %s: %w", p.id, err)
	}
	r.Header.Set("anthropic-version", apiVersion)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if p.key != nil {
		key, err := p.key(ctx)
		if err != nil {
			return nil, fmt.Errorf("llm %s: read API key: %w", p.id, err)
		}
		if key != "" {
			// A key over plain http to another machine is handed to everyone on
			// the path.
			if p.base.Scheme == "http" && !llm.IsLoopbackHost(p.base.Hostname()) {
				return nil, fmt.Errorf("llm %s: refusing to send an API key over plain http to %s", p.id, p.base.Host)
			}
			r.Header.Set("x-api-key", key)
		}
	}
	resp, err := p.hc.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("llm %s: %w", p.id, ctx.Err())
		}
		return nil, &llm.ProviderError{Provider: p.id, Message: err.Error(), Kind: llm.ErrUnavailable, Err: err}
	}
	return resp, nil
}

// The last block of the last message that may carry the field. A thinking
// block may not: adding anything to one is refused with "thinking or
// redacted_thinking blocks in the latest assistant message cannot be
// modified", which turns resuming a paused answer into a permanent 400.
func markLast(msgs []wireMessage) error {
	if len(msgs) == 0 {
		return nil
	}
	last := &msgs[len(msgs)-1]
	for i := len(last.Content) - 1; i >= 0; i-- {
		var block map[string]json.RawMessage
		if err := json.Unmarshal(last.Content[i], &block); err != nil {
			return fmt.Errorf("cache: unreadable block: %w", err)
		}
		var kind string
		if err := json.Unmarshal(block["type"], &kind); err == nil && !cacheable(kind) {
			continue
		}
		mark, err := json.Marshal(ephemeral())
		if err != nil {
			return err
		}
		block["cache_control"] = mark
		out, err := json.Marshal(block)
		if err != nil {
			return err
		}
		last.Content[i] = out
		return nil
	}
	return nil
}

// Anything unknown is allowed: a new block type the API accepts should not
// lose caching, while the two that are refused are named and stable.
func cacheable(kind string) bool {
	return kind != "thinking" && kind != "redacted_thinking"
}

func (p *Provider) statusError(resp *http.Response) error {
	rawBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	msg := strings.TrimSpace(string(rawBody))
	var envelope struct {
		Error *wireError `json:"error"`
	}
	typ := ""
	if json.Unmarshal(rawBody, &envelope) == nil && envelope.Error != nil && envelope.Error.Message != "" {
		msg, typ = envelope.Error.Message, envelope.Error.Type
	}
	e := &llm.ProviderError{Provider: p.id, Status: resp.StatusCode, Message: msg}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		e.Kind = llm.ErrUnauthorized
	case http.StatusTooManyRequests:
		e.Kind = llm.ErrRateLimited
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		e.Kind = llm.ErrUnavailable
	case http.StatusNotFound:
		e.Kind = kindOf(typ, msg)
	case http.StatusBadRequest:
		// The one 400 an app can act on: trim the conversation and try again.
		if llm.IsContextFull(msg) {
			e.Kind = llm.ErrContextFull
		}
	}
	return e
}

// The same types arrive as an HTTP status before the stream and as an error
// event inside it.
func kindOf(typ, msg string) error {
	switch typ {
	case "overloaded_error", "api_error", "timeout_error":
		return llm.ErrUnavailable
	case "rate_limit_error":
		return llm.ErrRateLimited
	case "authentication_error", "permission_error":
		return llm.ErrUnauthorized
	case "not_found_error":
		// A proxy at the wrong path is also not found; only a missing model is
		// worth telling the user to pick another.
		if strings.Contains(strings.ToLower(msg), "model") {
			return llm.ErrModelNotFound
		}
	}
	return nil
}
