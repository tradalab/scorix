// Package openai talks the OpenAI-compatible HTTP API, which is what OpenAI,
// OpenRouter, Groq, Ollama, llama-server, LM Studio and vLLM all serve - so one
// provider type covers a hosted API and a model running on the same machine.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/tradalab/scorix/llm"
)

// Called per request, so an app can keep the key sealed and change it
// without rebuilding the provider.
type KeySource func(ctx context.Context) (string, error)

func StaticKey(key string) KeySource {
	return func(context.Context) (string, error) { return key, nil }
}

type Config struct {
	ID string
	// Up to and including the version, e.g. https://api.openai.com/v1.
	BaseURL string
	// Nil for servers without auth.
	Key KeySource
	// Defaults to chat and embed: the API cannot say which models see images,
	// so vision is the app's claim to make.
	Capabilities []llm.Capability
	HTTPClient   *http.Client
	// Zero takes llm.DefaultRetry.
	Retry llm.RetryPolicy
}

type Provider struct {
	id    string
	base  *url.URL
	key   KeySource
	caps  []llm.Capability
	retry llm.RetryPolicy
	hc    *http.Client
	// The transport this provider created, if it did; see New.
	owned *http.Transport
}

func New(cfg Config) (*Provider, error) {
	if cfg.ID == "" {
		return nil, fmt.Errorf("openai: empty provider ID")
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, fmt.Errorf("openai %s: base URL %q is not an http(s) URL", cfg.ID, cfg.BaseURL)
	}
	caps := cfg.Capabilities
	if len(caps) == 0 {
		caps = []llm.Capability{llm.Chat, llm.Embed}
	}
	// No overall timeout: it would cut off a long generation, and the caller's
	// ctx is the deadline. Wrapped either way, so the key cannot follow a
	// redirect off this host or off https.
	var owned *http.Transport
	hc := llm.KeySafeClient(cfg.HTTPClient)
	if cfg.HTTPClient == nil {
		// A hosted API answers with headers long before it finishes writing, so a
		// connection that says nothing is a black hole. Not one on this machine or
		// the LAN, which may be loading a model.
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
	return &Provider{id: cfg.ID, base: base, key: cfg.Key, caps: caps, hc: hc, owned: owned, retry: retry}, nil
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

// Loopback only: a model on another box means the data crossed the network.
func (p *Provider) Local() bool { return llm.IsLoopbackHost(p.base.Hostname()) }

func init() { llm.RegisterDriver(Driver{}) }

type Driver struct{}

func (Driver) Name() string { return "openai" }

func (Driver) Fields() []llm.Field {
	return []llm.Field{
		{Key: "base_url", Label: "Base URL", Kind: llm.FieldURL, Required: true, Description: "Up to and including the version, e.g. https://api.openai.com/v1"},
		{Key: "api_key", Label: "API key", Kind: llm.FieldSecret, Description: "Empty for servers without auth"},
		{Key: "vision", Label: "Models can see images", Kind: llm.FieldBool, Default: "false"},
		{Key: "audio", Label: "Models can hear audio", Kind: llm.FieldBool, Default: "false"},
		{Key: "tools", Label: "Models can call tools", Kind: llm.FieldBool, Default: "false"},
		{Key: "transcribe", Label: "Serves speech to text", Kind: llm.FieldBool, Default: "false"},
	}
}

func (Driver) Open(cfg llm.ProviderConfig) (llm.Provider, error) {
	caps := []llm.Capability{llm.Chat, llm.Embed}
	for _, c := range []llm.Capability{llm.Vision, llm.Audio, llm.Tools, llm.Transcribe} {
		if cfg.Values[string(c)] == "true" {
			caps = append(caps, c)
		}
	}
	var key KeySource
	if cfg.Secret != nil {
		key = func(ctx context.Context) (string, error) { return cfg.Secret(ctx, "api_key") }
	}
	return New(Config{ID: cfg.ID, BaseURL: cfg.Values["base_url"], Key: key, Capabilities: caps, Retry: llm.RetryPolicy{OnRetry: cfg.OnRetry}})
}

type wireContentPart struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	ImageURL   *wireImageURL   `json:"image_url,omitempty"`
	InputAudio *wireInputAudio `json:"input_audio,omitempty"`
}

type wireImageURL struct {
	URL string `json:"url"`
}

type wireInputAudio struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

func toWire(msgs []llm.Message) ([]wireMessage, error) {
	out := make([]wireMessage, 0, len(msgs))
	for _, m := range msgs {
		wm := wireMessage{Role: string(m.Role), ToolCallID: m.ToolCallID, Content: m.Content}
		for _, c := range m.ToolCalls {
			wc := wireToolCall{ID: c.ID, Type: "function"}
			wc.Function.Name, wc.Function.Arguments = c.Name, c.Arguments
			wm.ToolCalls = append(wm.ToolCalls, wc)
		}
		if m.Content == "" && len(m.ToolCalls) > 0 {
			wm.Content = nil // the API wants null for a turn that only called tools
		}
		if len(m.Images) > 0 || len(m.Audio) > 0 {
			parts := []wireContentPart{{Type: "text", Text: m.Content}}
			for _, img := range m.Images {
				// An iPhone photo is HEIC, which the API refuses with a flat "Invalid
				// image" and a local model may silently describe as nothing.
				mime, ok := imageCarried(img.MIME)
				if !ok {
					return nil, fmt.Errorf("image %s: the OpenAI API carries only png, jpeg, gif and webp: %w", img.MIME, llm.ErrUnsupported)
				}
				uri := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
				parts = append(parts, wireContentPart{Type: "image_url", ImageURL: &wireImageURL{URL: uri}})
			}
			for _, a := range m.Audio {
				format, ok := audioFormat(a.MIME)
				if !ok {
					return nil, fmt.Errorf("audio %s: the OpenAI API carries only wav and mp3: %w", a.MIME, llm.ErrUnsupported)
				}
				parts = append(parts, wireContentPart{Type: "input_audio", InputAudio: &wireInputAudio{Data: base64.StdEncoding.EncodeToString(a.Data), Format: format}})
			}
			wm.Content = parts
		}
		out = append(out, wm)
	}
	return out, nil
}

// The normalised type, and whether the API takes it at all: image/jpg is
// what a file picker hands over, the API knows only image/jpeg.
func imageCarried(mime string) (string, bool) {
	base, _, _ := strings.Cut(strings.ToLower(mime), ";")
	switch base = strings.TrimSpace(base); base {
	case "image/jpg", "image/jpeg":
		return "image/jpeg", true
	case "image/png", "image/gif", "image/webp":
		return base, true
	}
	return "", false
}

func audioFormat(mime string) (string, bool) {
	base, _, _ := strings.Cut(strings.ToLower(mime), ";")
	switch strings.TrimSpace(base) {
	case "audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave":
		return "wav", true
	case "audio/mpeg", "audio/mp3":
		return "mp3", true
	}
	return "", false
}

type wireUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type streamChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// Two names for one thing: llama-server and DeepSeek send
			// reasoning_content, OpenRouter and Ollama send reasoning.
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name string `json:"name"`
					// A string by the spec; some servers send the object itself.
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage `json:"usage"`
	// llama-server's extension; nothing standard carries server-side timing.
	Timings *struct {
		PromptMS    float64 `json:"prompt_ms"`
		PredictedMS float64 `json:"predicted_ms"`
	} `json:"timings"`
	Error *wireErrorBody `json:"error"`
}

func (p *Provider) Chat(ctx context.Context, req llm.ChatRequest, onChunk func(llm.Chunk) error) (*llm.ChatResult, error) {
	msgs, err := toWire(req.Messages)
	if err != nil {
		return nil, fmt.Errorf("llm %s: %w", p.id, err)
	}
	body := map[string]any{}
	for k, v := range req.Extra {
		body[k] = v
	}
	body["model"] = req.Model
	body["messages"] = msgs
	body["stream"] = true
	body["stream_options"] = map[string]any{"include_usage": true}
	// The reasoning models refuse max_tokens, so a caller who reached for
	// max_completion_tokens must not also get the one that 400s.
	if _, renamed := body["max_completion_tokens"]; renamed {
		// Whichever way it was set: Extra is copied in above.
		delete(body, "max_tokens")
	} else if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if len(req.Stop) > 0 {
		body["stop"] = req.Stop
	}
	if len(req.Tools) > 0 {
		tools := make([]wireTool, len(req.Tools))
		for i, t := range req.Tools {
			tools[i].Type = "function"
			tools[i].Function.Name, tools[i].Function.Description, tools[i].Function.Parameters = t.Name, t.Description, t.Parameters
		}
		body["tools"] = tools
		switch req.ToolChoice {
		case llm.ToolAuto:
		case llm.ToolNone, llm.ToolRequired:
			body["tool_choice"] = string(req.ToolChoice)
		default:
			body["tool_choice"] = map[string]any{"type": "function", "function": map[string]string{"name": string(req.ToolChoice)}}
		}
	}
	if f := req.Format; f != nil {
		name := f.Name
		if name == "" {
			name = "response" // the API refuses a json_schema without one
		}
		body["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": name, "schema": f.Schema}}
	}

	resp, err := p.do(ctx, http.MethodPost, "chat/completions", body)
	if err != nil {
		return nil, err
	}
	// Closing the body is also how a stop reaches the server.
	defer resp.Body.Close()
	return p.readStream(ctx, resp.Body, req.Model, onChunk)
}

func (p *Provider) readStream(ctx context.Context, body io.Reader, model string, onChunk func(llm.Chunk) error) (*llm.ChatResult, error) {
	res := &llm.ChatResult{Model: model}
	var text, reasoning strings.Builder
	emit := func(kind llm.ChunkKind, s string) error {
		if s == "" {
			return nil
		}
		if kind == llm.ChunkReasoning {
			reasoning.WriteString(s)
		} else {
			text.WriteString(s)
		}
		if onChunk == nil {
			return nil
		}
		return onChunk(llm.Chunk{Kind: kind, Text: s})
	}
	var calls []*llm.ToolCall
	callAt := map[int]*llm.ToolCall{}
	finished := false
	br := bufio.NewReader(body)
	var other strings.Builder
	for {
		line, readErr := br.ReadString('\n')
		// Before the half-written line it comes with is parsed: a dropped
		// connection would otherwise be reported as a malformed server.
		if readErr != nil && readErr != io.EOF {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("llm %s: %w", p.id, ctx.Err())
			}
			return nil, &llm.ProviderError{Provider: p.id, Message: "stream broke: " + readErr.Error(), Kind: llm.ErrUnavailable}
		}
		payload, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "data:")
		if !ok {
			// Kept for the bottom of the loop: a server that ignores stream:true
			// answers one JSON object, and that body is every line but a data one.
			if s := strings.TrimRight(line, "\r\n"); s != "" && !strings.HasPrefix(s, ":") && other.Len() < 64<<10 {
				other.WriteString(s)
			}
		}
		if ok {
			payload = strings.TrimSpace(payload)
			if payload == "[DONE]" {
				finished = true
				break
			}
			// An empty data line is a keepalive, not a frame.
			if payload == "" {
				if readErr == io.EOF {
					break
				}
				continue
			}
			if !json.Valid([]byte(payload)) {
				// Half a frame at the end of the body is a dropped connection - a proxy
				// closes cleanly, so the last line arrives with io.EOF.
				if readErr == io.EOF {
					return nil, &llm.ProviderError{Provider: p.id, Message: "stream ended mid-frame", Kind: llm.ErrUnavailable}
				}
				return nil, &llm.ProviderError{Provider: p.id, Message: "unreadable stream chunk: " + clipLine(payload)}
			}
			var c streamChunk
			if err := json.Unmarshal([]byte(payload), &c); err != nil {
				return nil, &llm.ProviderError{Provider: p.id, Message: "unreadable stream chunk: " + err.Error()}
			}
			if c.Error != nil {
				return nil, &llm.ProviderError{Provider: p.id, Message: c.Error.Message, Kind: kindFromMessage(c.Error.Message)}
			}
			if c.Model != "" {
				res.Model = c.Model
			}
			if c.Usage != nil {
				res.Usage.PromptTokens = c.Usage.PromptTokens
				res.Usage.CacheReadTokens = c.Usage.PromptTokensDetails.CachedTokens
				res.Usage.CompletionTokens = c.Usage.CompletionTokens
			}
			if c.Timings != nil {
				res.Usage.PromptDuration = time.Duration(c.Timings.PromptMS * float64(time.Millisecond))
				res.Usage.GenerateDuration = time.Duration(c.Timings.PredictedMS * float64(time.Millisecond))
			}
			for _, ch := range c.Choices {
				if ch.FinishReason != nil && *ch.FinishReason != "" {
					res.FinishReason = *ch.FinishReason
					finished = true
				}
				if err := emit(llm.ChunkReasoning, either(ch.Delta.ReasoningContent, ch.Delta.Reasoning)); err != nil {
					return nil, err
				}
				if err := emit(llm.ChunkText, ch.Delta.Content); err != nil {
					return nil, err
				}
				for _, d := range ch.Delta.ToolCalls {
					cur := callAt[d.Index]
					// The id comes only with a call's first fragment, so a different id on one
					// index is a new call. Guards a server that leaves index out.
					if cur == nil || (d.ID != "" && cur.ID != "" && d.ID != cur.ID) {
						cur = &llm.ToolCall{}
						callAt[d.Index] = cur
						calls = append(calls, cur)
					}
					if d.ID != "" {
						cur.ID = d.ID
					}
					// Added to, unless it is the whole name again: a proxy with an incremental
					// parser splits it, some servers repeat it whole. Skipping what the name
					// merely STARTS with loses a fragment - "list_", "list", "s" is
					// "list_lists".
					if n := d.Function.Name; n != "" && n != cur.Name {
						cur.Name += n
					}
					cur.Arguments += argumentText(d.Function.Arguments)
				}
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	if !finished {
		// Only when the stream delivered nothing: read after an answer that
		// arrived, it hands the app the same tool calls twice - and a Destructive
		// tool runs twice.
		streamed := text.Len() > 0 || reasoning.Len() > 0 || len(calls) > 0
		done, err := false, error(nil)
		if !streamed {
			done, err = p.whole(other.String(), res, emit, &calls)
		}
		if err != nil {
			return nil, err
		}
		// A connection closing cleanly mid-answer reads like the end of one.
		if !done {
			return nil, &llm.ProviderError{Provider: p.id, Message: "stream ended before the answer did", Kind: llm.ErrUnavailable}
		}
	}
	for _, c := range calls {
		res.ToolCalls = append(res.ToolCalls, *c)
		if onChunk != nil {
			if err := onChunk(llm.Chunk{Kind: llm.ChunkToolCall, Call: c}); err != nil {
				return nil, err
			}
		}
	}
	res.Content = text.String()
	res.Reasoning = reasoning.String()
	return res, nil
}

// Enough to recognise which server sent it, not enough to fill a log with
// someone's prompt.
func clipLine(s string) string {
	if len(s) > 200 {
		return strings.ToValidUTF8(s[:200], "") + "..."
	}
	return s
}

// One field under two spellings: a gateway that sends both must not make
// the user read the thinking twice.
func either(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// The answer a server sends when it ignores stream:true, and the error
// envelope some gateways send with HTTP 200.
func (p *Provider) whole(body string, res *llm.ChatResult, emit func(llm.ChunkKind, string) error, calls *[]*llm.ToolCall) (bool, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return false, nil
	}
	var w struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage *wireUsage     `json:"usage"`
		Error *wireErrorBody `json:"error"`
	}
	if json.Unmarshal([]byte(body), &w) != nil {
		return false, nil
	}
	if w.Error != nil && w.Error.Message != "" {
		return false, &llm.ProviderError{Provider: p.id, Message: w.Error.Message, Kind: kindFromMessage(w.Error.Message)}
	}
	if len(w.Choices) == 0 {
		return false, nil
	}
	if w.Model != "" {
		res.Model = w.Model
	}
	if w.Usage != nil {
		res.Usage.PromptTokens = w.Usage.PromptTokens
		res.Usage.CacheReadTokens = w.Usage.PromptTokensDetails.CachedTokens
		res.Usage.CompletionTokens = w.Usage.CompletionTokens
	}
	c := w.Choices[0]
	if c.FinishReason != nil {
		res.FinishReason = *c.FinishReason
	}
	if err := emit(llm.ChunkReasoning, either(c.Message.ReasoningContent, c.Message.Reasoning)); err != nil {
		return false, err
	}
	if err := emit(llm.ChunkText, c.Message.Content); err != nil {
		return false, err
	}
	for _, tc := range c.Message.ToolCalls {
		*calls = append(*calls, &llm.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: argumentText(tc.Function.Arguments)})
	}
	return true, nil
}

func argumentText(raw json.RawMessage) string {
	var s string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &s) == nil {
		return s
	}
	if string(raw) == "null" {
		return ""
	}
	return string(raw)
}

func (p *Provider) Embed(ctx context.Context, req llm.EmbedRequest) (*llm.EmbedResult, error) {
	resp, err := p.do(ctx, http.MethodPost, "embeddings", map[string]any{"model": req.Model, "input": req.Input})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Model string `json:"model"`
		Data  []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Usage wireUsage `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, llm.Unreadable(p.id, "unreadable embeddings", err)
	}
	// A short answer would shift every vector after the gap onto the wrong
	// input, and nothing downstream could tell.
	if len(out.Data) != len(req.Input) {
		return nil, &llm.ProviderError{Provider: p.id, Message: fmt.Sprintf("%d embeddings for %d inputs", len(out.Data), len(req.Input))}
	}
	sort.Slice(out.Data, func(i, j int) bool { return out.Data[i].Index < out.Data[j].Index })
	res := &llm.EmbedResult{Model: out.Model, Vectors: make([][]float32, len(out.Data)), Usage: llm.Usage{PromptTokens: out.Usage.PromptTokens}}
	for i, d := range out.Data {
		v := make([]float32, len(d.Embedding))
		for j, f := range d.Embedding {
			v[j] = float32(f)
		}
		res.Vectors[i] = v
	}
	return res, nil
}

func (p *Provider) Models(ctx context.Context) ([]llm.ModelInfo, error) {
	resp, err := p.do(ctx, http.MethodGet, "models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, llm.Unreadable(p.id, "unreadable model list", err)
	}
	models := make([]llm.ModelInfo, 0, len(out.Data))
	for _, m := range out.Data {
		models = append(models, llm.ModelInfo{ID: m.ID, OwnedBy: m.OwnedBy})
	}
	return models, nil
}

// What this provider can do with a model. OpenAI says almost nothing, so
// the capabilities are the provider's own - the boxes on its form. A local
// server does better: llama-server carries meta.n_ctx. A model the listing
// does not mention is described anyway, because the request that uses it is
// where a missing model is found out.
func (p *Provider) Describe(ctx context.Context, model string) (*llm.ModelDetail, error) {
	d := &llm.ModelDetail{ID: model, Capabilities: slices.Clone(p.caps)}
	resp, err := p.do(ctx, http.MethodGet, "models", nil)
	if err != nil {
		return d, nil
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID   string `json:"id"`
			Meta struct {
				Ctx      int `json:"n_ctx"`
				CtxTrain int `json:"n_ctx_train"`
			} `json:"meta"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return d, nil
	}
	for _, m := range out.Data {
		if m.ID != model {
			continue
		}
		// The window it is serving beats the one it could serve.
		if d.ContextLength = m.Meta.Ctx; d.ContextLength == 0 {
			d.ContextLength = m.Meta.CtxTrain
		}
		break
	}
	return d, nil
}

// The file's extension is what most servers go by, so a clip whose type is
// not on the API's list is refused here rather than misread there.
func (p *Provider) Transcribe(ctx context.Context, req llm.TranscribeRequest) (*llm.TranscribeResult, error) {
	ext, ok := audioExt(req.Audio.MIME)
	if !ok {
		return nil, fmt.Errorf("llm %s: audio %s cannot be transcribed over the OpenAI API: %w", p.id, req.Audio.MIME, llm.ErrUnsupported)
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="audio.`+ext+`"`)
	h.Set("Content-Type", req.Audio.MIME)
	part, err := w.CreatePart(h)
	if err == nil {
		_, err = part.Write(req.Audio.Data)
	}
	if err != nil {
		return nil, fmt.Errorf("llm %s: encode request: %w", p.id, err)
	}
	_ = w.WriteField("model", req.Model)
	if req.Language != "" {
		_ = w.WriteField("language", req.Language)
	}
	// json is the one format every transcription model accepts.
	_ = w.WriteField("response_format", "json")
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("llm %s: encode request: %w", p.id, err)
	}
	resp, err := p.request(ctx, http.MethodPost, "audio/transcriptions", w.FormDataContentType(), buf.Bytes())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, llm.Unreadable(p.id, "unreadable transcription", err)
	}
	return &llm.TranscribeResult{Text: strings.TrimSpace(out.Text), Language: req.Language}, nil
}

func audioExt(mime string) (string, bool) {
	base, _, _ := strings.Cut(strings.ToLower(mime), ";")
	switch strings.TrimSpace(base) {
	case "audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave":
		return "wav", true
	case "audio/mpeg", "audio/mp3":
		return "mp3", true
	case "audio/flac", "audio/x-flac":
		return "flac", true
	case "audio/ogg":
		return "ogg", true
	case "audio/webm":
		return "webm", true
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return "m4a", true
	}
	return "", false
}

func (p *Provider) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	if body == nil {
		return p.request(ctx, method, path, "", nil)
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("llm %s: encode request: %w", p.id, err)
	}
	return p.request(ctx, method, path, "application/json", buf)
}

// One call from the caller's side, however many times it goes out. The body
// is bytes because a reader is read once.
func (p *Provider) request(ctx context.Context, method, path, contentType string, body []byte) (*http.Response, error) {
	resp, err := llm.Retry(ctx, p.retry, func() (*http.Response, error) {
		var r io.Reader
		if body != nil {
			r = bytes.NewReader(body)
		}
		return p.send(ctx, method, path, contentType, r)
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

// One attempt. A status is left as it came: only request() knows whether
// this was the last try.
func (p *Provider) send(ctx context.Context, method, path, contentType string, body io.Reader) (*http.Response, error) {
	r, err := http.NewRequestWithContext(ctx, method, p.base.JoinPath(path).String(), body)
	if err != nil {
		return nil, fmt.Errorf("llm %s: %w", p.id, err)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if p.key != nil {
		key, err := p.key(ctx)
		if err != nil {
			return nil, fmt.Errorf("llm %s: read API key: %w", p.id, err)
		}
		if key != "" {
			// Decided per request, when it is known whether there is a key at all: a
			// key over plain http to another machine is handed to everyone on the path.
			if p.base.Scheme == "http" && !llm.IsLoopbackHost(p.base.Hostname()) {
				return nil, fmt.Errorf("llm %s: refusing to send an API key over plain http to %s", p.id, p.base.Host)
			}
			r.Header.Set("Authorization", "Bearer "+key)
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

// OpenAI nests the message under error.message; Ollama's native endpoints and
// some proxies send "error" as a bare string.
type wireErrorBody struct {
	Message string `json:"message"`
}

func (e *wireErrorBody) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		e.Message = s
		return nil
	}
	type plain wireErrorBody
	return json.Unmarshal(b, (*plain)(e))
}

func (p *Provider) statusError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	msg := strings.TrimSpace(string(raw))
	var envelope struct {
		Error *wireErrorBody `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) == nil && envelope.Error != nil && envelope.Error.Message != "" {
		msg = envelope.Error.Message
	}
	e := &llm.ProviderError{Provider: p.id, Status: resp.StatusCode, Message: msg}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		e.Kind = llm.ErrUnauthorized
	case http.StatusTooManyRequests:
		e.Kind = llm.ErrRateLimited
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		e.Kind = llm.ErrUnavailable
	case http.StatusNotFound:
		e.Kind = kindFromMessage(msg)
	case http.StatusBadRequest:
		// llama-server and OpenAI both answer 400 for a prompt past the window.
		// Without the sentinel an app cannot trim and retry, so the conversation
		// is dead from that turn on while the same app recovers on Claude.
		if llm.IsContextFull(msg) {
			e.Kind = llm.ErrContextFull
		}
	}
	return e
}

// A 404 is either a model the server does not have or a base URL pointing at
// the wrong path; only the first is worth telling the user to pull a model.
func kindFromMessage(msg string) error {
	if strings.Contains(strings.ToLower(msg), "model") {
		return llm.ErrModelNotFound
	}
	return nil
}
