// Package ollama talks Ollama's own API rather than its OpenAI-compatible one:
// only the native API pulls, describes and deletes models, streams a model's
// thinking separately, and takes per-request options such as num_ctx.
package ollama

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
	"time"

	"github.com/tradalab/scorix/llm"
)

const DefaultURL = "http://127.0.0.1:11434"

type Config struct {
	ID string
	// DefaultURL when empty.
	BaseURL string
	// Defaults to chat and embed; what a model sees or calls is per model, and
	// Describe says.
	Capabilities []llm.Capability
	HTTPClient   *http.Client
	// Zero takes llm.DefaultRetry.
	Retry llm.RetryPolicy
}

type Provider struct {
	id    string
	base  *url.URL
	caps  []llm.Capability
	hc    *http.Client
	retry llm.RetryPolicy
}

func New(cfg Config) (*Provider, error) {
	if cfg.ID == "" {
		return nil, errors.New("ollama: empty provider ID")
	}
	raw := cfg.BaseURL
	if raw == "" {
		raw = DefaultURL
	}
	base, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, fmt.Errorf("ollama %s: base URL %q is not an http(s) URL", cfg.ID, raw)
	}
	caps := cfg.Capabilities
	if len(caps) == 0 {
		caps = []llm.Capability{llm.Chat, llm.Embed}
	}
	hc := cfg.HTTPClient
	if hc == nil {
		// No overall timeout: loading a model before the first byte can take
		// minutes, and the caller's ctx is the deadline.
		hc = &http.Client{}
	}
	retry := cfg.Retry
	if retry.Attempts == 0 {
		// Timing only: replacing the policy would drop OnRetry, see retry.go.
		retry.Attempts, retry.Base, retry.Max = llm.DefaultRetry.Attempts, llm.DefaultRetry.Base, llm.DefaultRetry.Max
	}
	return &Provider{id: cfg.ID, base: base, caps: caps, hc: hc, retry: retry}, nil
}

func (p *Provider) ID() string                     { return p.id }
func (p *Provider) Capabilities() []llm.Capability { return p.caps }

func (p *Provider) Local() bool { return llm.IsLoopbackHost(p.base.Hostname()) }

func init() { llm.RegisterDriver(Driver{}) }

type Driver struct{}

func (Driver) Name() string { return "ollama" }

func (Driver) Fields() []llm.Field {
	return []llm.Field{
		{Key: "base_url", Label: "Address", Kind: llm.FieldURL, Default: DefaultURL, Description: "Where Ollama listens"},
		{Key: "vision", Label: "Models can see images", Kind: llm.FieldBool, Default: "false"},
		{Key: "audio", Label: "Models can hear audio", Kind: llm.FieldBool, Default: "false"},
		{Key: "tools", Label: "Models can call tools", Kind: llm.FieldBool, Default: "false"},
	}
}

func (Driver) Open(cfg llm.ProviderConfig) (llm.Provider, error) {
	caps := []llm.Capability{llm.Chat, llm.Embed}
	for _, c := range []llm.Capability{llm.Vision, llm.Audio, llm.Tools} {
		if cfg.Values[string(c)] == "true" {
			caps = append(caps, c)
		}
	}
	return New(Config{ID: cfg.ID, BaseURL: cfg.Values["base_url"], Capabilities: caps, Retry: llm.RetryPolicy{OnRetry: cfg.OnRetry}})
}

type wireMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	Thinking  string         `json:"thinking,omitempty"`
	Images    []string       `json:"images,omitempty"`
	Audio     []string       `json:"audio,omitempty"`
	ToolCalls []wireToolCall `json:"tool_calls,omitempty"`
	ToolName  string         `json:"tool_name,omitempty"`
}

type wireToolCall struct {
	ID       string `json:"id,omitempty"`
	Function struct {
		Name string `json:"name"`
		// An object on this API, where OpenAI's carries a string.
		Arguments json.RawMessage `json:"arguments"`
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

// Keys of ChatRequest.Extra that are request fields; every other key is a
// model option such as num_ctx.
var topLevel = map[string]bool{"keep_alive": true, "think": true}

func (p *Provider) Chat(ctx context.Context, req llm.ChatRequest, onChunk func(llm.Chunk) error) (*llm.ChatResult, error) {
	switch req.ToolChoice {
	case llm.ToolAuto:
	case llm.ToolNone:
		req.Tools = nil
	default:
		// The API cannot require a call: sending the tools anyway would let the
		// model answer in prose where the app expects one.
		return nil, fmt.Errorf("ollama %s: cannot require a tool call (%q): %w", p.id, req.ToolChoice, llm.ErrUnsupported)
	}
	msgs, err := toWire(req.Messages)
	if err != nil {
		return nil, fmt.Errorf("ollama %s: %w", p.id, err)
	}
	body := map[string]any{"model": req.Model, "messages": msgs, "stream": true}
	options := map[string]any{}
	for k, v := range req.Extra {
		if topLevel[k] {
			body[k] = v
		} else {
			options[k] = v
		}
	}
	if req.MaxTokens > 0 {
		options["num_predict"] = req.MaxTokens
	}
	if req.Temperature != nil {
		options["temperature"] = *req.Temperature
	}
	if len(req.Stop) > 0 {
		options["stop"] = req.Stop
	}
	if len(options) > 0 {
		body["options"] = options
	}
	if len(req.Tools) > 0 {
		tools := make([]wireTool, len(req.Tools))
		for i, t := range req.Tools {
			tools[i].Type = "function"
			tools[i].Function.Name, tools[i].Function.Description, tools[i].Function.Parameters = t.Name, t.Description, t.Parameters
		}
		body["tools"] = tools
	}
	if req.Format != nil {
		body["format"] = req.Format.Schema
	}
	resp, err := p.do(ctx, http.MethodPost, "api/chat", body)
	if err != nil {
		return nil, err
	}
	// Closing the body is how a stop reaches Ollama.
	defer resp.Body.Close()
	return p.readStream(ctx, resp.Body, req.Model, onChunk)
}

func toWire(msgs []llm.Message) ([]wireMessage, error) {
	// A tool result names its tool on this API, not the call it answers.
	names := map[string]string{}
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			names[c.ID] = c.Name
		}
	}
	out := make([]wireMessage, 0, len(msgs))
	for _, m := range msgs {
		wm := wireMessage{Role: string(m.Role), Content: m.Content}
		for _, img := range m.Images {
			wm.Images = append(wm.Images, base64.StdEncoding.EncodeToString(img.Data))
		}
		for _, a := range m.Audio {
			wm.Audio = append(wm.Audio, base64.StdEncoding.EncodeToString(a.Data))
		}
		for _, c := range m.ToolCalls {
			var wc wireToolCall
			wc.ID, wc.Function.Name = c.ID, c.Name
			if !json.Valid([]byte(c.Arguments)) {
				return nil, fmt.Errorf("tool call %s: arguments are not JSON, which this API needs as an object", c.Name)
			}
			wc.Function.Arguments = json.RawMessage(c.Arguments)
			wm.ToolCalls = append(wm.ToolCalls, wc)
		}
		if m.Role == llm.ToolResult {
			wm.ToolName = names[m.ToolCallID]
		}
		out = append(out, wm)
	}
	return out, nil
}

type chatLine struct {
	Model              string      `json:"model"`
	Message            wireMessage `json:"message"`
	Done               bool        `json:"done"`
	DoneReason         string      `json:"done_reason"`
	PromptEvalCount    int         `json:"prompt_eval_count"`
	EvalCount          int         `json:"eval_count"`
	PromptEvalDuration int64       `json:"prompt_eval_duration"`
	EvalDuration       int64       `json:"eval_duration"`
	Error              string      `json:"error"`
}

func (p *Provider) readStream(ctx context.Context, body io.Reader, model string, onChunk func(llm.Chunk) error) (*llm.ChatResult, error) {
	res := &llm.ChatResult{Model: model}
	var text, reasoning strings.Builder
	emit := func(c llm.Chunk) error {
		if onChunk == nil {
			return nil
		}
		return onChunk(c)
	}
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	done := false
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var c chatLine
		if err := json.Unmarshal(line, &c); err != nil {
			return nil, &llm.ProviderError{Provider: p.id, Message: "unreadable stream line: " + err.Error()}
		}
		if c.Error != "" {
			return nil, &llm.ProviderError{Provider: p.id, Message: c.Error, Kind: kindFromMessage(c.Error)}
		}
		if c.Model != "" {
			res.Model = c.Model
		}
		if s := c.Message.Thinking; s != "" {
			reasoning.WriteString(s)
			if err := emit(llm.Chunk{Kind: llm.ChunkReasoning, Text: s}); err != nil {
				return nil, err
			}
		}
		if s := c.Message.Content; s != "" {
			text.WriteString(s)
			if err := emit(llm.Chunk{Kind: llm.ChunkText, Text: s}); err != nil {
				return nil, err
			}
		}
		for _, tc := range c.Message.ToolCalls {
			res.ToolCalls = append(res.ToolCalls, llm.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: string(tc.Function.Arguments)})
		}
		if c.Done {
			done = true
			res.FinishReason = c.DoneReason
			res.Usage = llm.Usage{
				PromptTokens: c.PromptEvalCount, CompletionTokens: c.EvalCount,
				PromptDuration: time.Duration(c.PromptEvalDuration), GenerateDuration: time.Duration(c.EvalDuration),
			}
			break
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("ollama %s: %w", p.id, ctx.Err())
		}
		return nil, &llm.ProviderError{Provider: p.id, Message: "stream broke: " + err.Error(), Kind: llm.ErrUnavailable}
	}
	// A connection that closes cleanly mid-answer reads like the end of one.
	if !done {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("ollama %s: %w", p.id, ctx.Err())
		}
		return nil, &llm.ProviderError{Provider: p.id, Message: "stream ended before the answer did", Kind: llm.ErrUnavailable}
	}
	// Ollama says "stop" for a turn that only called tools, and an app that
	// branches on the reason would take it as a finished answer.
	if len(res.ToolCalls) > 0 && (res.FinishReason == "" || res.FinishReason == "stop") {
		res.FinishReason = "tool_calls"
	}
	for i := range res.ToolCalls {
		if err := emit(llm.Chunk{Kind: llm.ChunkToolCall, Call: &res.ToolCalls[i]}); err != nil {
			return nil, err
		}
	}
	res.Content, res.Reasoning = text.String(), reasoning.String()
	return res, nil
}

func (p *Provider) Embed(ctx context.Context, req llm.EmbedRequest) (*llm.EmbedResult, error) {
	resp, err := p.do(ctx, http.MethodPost, "api/embed", map[string]any{"model": req.Model, "input": req.Input})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Model           string      `json:"model"`
		Embeddings      [][]float32 `json:"embeddings"`
		PromptEvalCount int         `json:"prompt_eval_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, llm.Unreadable(p.id, "unreadable embeddings", err)
	}
	if len(out.Embeddings) != len(req.Input) {
		return nil, &llm.ProviderError{Provider: p.id, Message: fmt.Sprintf("%d embeddings for %d inputs", len(out.Embeddings), len(req.Input))}
	}
	return &llm.EmbedResult{Model: out.Model, Vectors: out.Embeddings, Usage: llm.Usage{PromptTokens: out.PromptEvalCount}}, nil
}

func (p *Provider) Models(ctx context.Context) ([]llm.ModelInfo, error) {
	resp, err := p.do(ctx, http.MethodGet, "api/tags", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Models []struct {
			Name       string    `json:"name"`
			Size       int64     `json:"size"`
			Digest     string    `json:"digest"`
			ModifiedAt time.Time `json:"modified_at"`
			Details    struct {
				Family            string `json:"family"`
				ParameterSize     string `json:"parameter_size"`
				QuantizationLevel string `json:"quantization_level"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, llm.Unreadable(p.id, "unreadable model list", err)
	}
	ms := make([]llm.ModelInfo, len(out.Models))
	for i, m := range out.Models {
		ms[i] = llm.ModelInfo{ID: m.Name, Size: m.Size, Digest: m.Digest, Modified: m.ModifiedAt,
			Family: m.Details.Family, ParameterSize: m.Details.ParameterSize, Quantization: m.Details.QuantizationLevel}
	}
	return ms, nil
}

var capabilityNames = map[string]llm.Capability{
	"completion": llm.Chat, "embedding": llm.Embed, "vision": llm.Vision, "tools": llm.Tools, "audio": llm.Audio,
}

// What a build that does not list capabilities leaves to go on: the
// projector shows up as a second family.
func looksMultimodal(families []string, info map[string]any) bool {
	// Only a projector or an architecture with no text-only member. Not one
	// shared with a text-only model: gemma3 covers gemma3:1b, which cannot
	// see, and saying it can is what got this heuristic deleted once.
	for _, f := range families {
		switch strings.ToLower(f) {
		case "clip", "mllama", "vision":
			return true
		}
	}
	for k := range info {
		if strings.HasPrefix(k, "clip.") || strings.Contains(k, ".vision.") {
			return true
		}
	}
	return false
}

func (p *Provider) Describe(ctx context.Context, model string) (*llm.ModelDetail, error) {
	resp, err := p.do(ctx, http.MethodPost, "api/show", map[string]any{"model": model})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Capabilities []string `json:"capabilities"`
		Details      struct {
			Family            string   `json:"family"`
			Families          []string `json:"families"`
			ParameterSize     string   `json:"parameter_size"`
			QuantizationLevel string   `json:"quantization_level"`
		} `json:"details"`
		ModelInfo map[string]any `json:"model_info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, llm.Unreadable(p.id, "unreadable model detail", err)
	}
	d := &llm.ModelDetail{ID: model, Family: out.Details.Family, ParameterSize: out.Details.ParameterSize, Quantization: out.Details.QuantizationLevel}
	for _, name := range out.Capabilities {
		if c, ok := capabilityNames[name]; ok {
			d.Capabilities = append(d.Capabilities, c)
		}
	}
	// Only when the server says nothing at all: a build old enough not to have
	// the field describes every vision model as text-only.
	if len(out.Capabilities) == 0 {
		// Chat as well, not only Vision: a build that says nothing would otherwise
		// describe every model as unable to answer a question.
		d.Capabilities = append(d.Capabilities, llm.Chat)
		if looksMultimodal(out.Details.Families, out.ModelInfo) {
			d.Capabilities = append(d.Capabilities, llm.Vision)
		}
	}
	// The key is named after the architecture: llama.context_length,
	// gemma3.context_length.
	if arch, _ := out.ModelInfo["general.architecture"].(string); arch != "" {
		if n, ok := out.ModelInfo[arch+".context_length"].(float64); ok {
			d.ContextLength = int(n)
		}
	}
	// A build that names no architecture still says how long the context is,
	// under some key ending in it. Family first, then the largest of the rest:
	// a multimodal GGUF carries its projector's window too (clip is 77), and
	// picking one at random answered differently on every call.
	if d.ContextLength == 0 {
		if n, ok := out.ModelInfo[out.Details.Family+".context_length"].(float64); ok {
			d.ContextLength = int(n)
		}
	}
	if d.ContextLength == 0 {
		for k, v := range out.ModelInfo {
			if n, ok := v.(float64); ok && strings.HasSuffix(k, ".context_length") && int(n) > d.ContextLength {
				d.ContextLength = int(n)
			}
		}
	}
	return d, nil
}

func (p *Provider) Pull(ctx context.Context, model string, progress func(llm.PullProgress)) error {
	resp, err := p.do(ctx, http.MethodPost, "api/pull", map[string]any{"model": model, "stream": true})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var line struct {
			Status    string `json:"status"`
			Digest    string `json:"digest"`
			Total     int64  `json:"total"`
			Completed int64  `json:"completed"`
			Error     string `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if line.Error != "" {
			return &llm.ProviderError{Provider: p.id, Message: line.Error, Kind: kindFromMessage(line.Error)}
		}
		if progress != nil {
			progress(llm.PullProgress{Status: line.Status, Digest: line.Digest, Done: line.Completed, Total: line.Total})
		}
		if line.Status == "success" {
			return nil
		}
	}
	if ctx.Err() != nil {
		return fmt.Errorf("ollama %s: %w", p.id, ctx.Err())
	}
	if err := sc.Err(); err != nil {
		return &llm.ProviderError{Provider: p.id, Message: "pull broke: " + err.Error(), Kind: llm.ErrUnavailable}
	}
	return &llm.ProviderError{Provider: p.id, Message: "pull ended without success", Kind: llm.ErrUnavailable}
}

// Sent once, deliberately: the only call here where trying again is not
// free. A delete that landed with its answer lost comes back 404, which
// reads as "model not found" for a model that is gone.
func (p *Provider) Delete(ctx context.Context, model string) error {
	resp, err := p.doRetry(ctx, llm.RetryPolicy{Attempts: 1}, http.MethodDelete, "api/delete", map[string]any{"model": model})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (p *Provider) Version(ctx context.Context) (string, error) {
	resp, err := p.do(ctx, http.MethodGet, "api/version", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", llm.Unreadable(p.id, "unreadable version", err)
	}
	return out.Version, nil
}

var _ llm.ModelManager = (*Provider)(nil)

// One call from the caller's side, however many times it goes out: Ollama
// answers 503 while it is still starting.
func (p *Provider) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	return p.doRetry(ctx, p.retry, method, path, body)
}

// One call from the caller's point of view, however many times it goes out.
func (p *Provider) doRetry(ctx context.Context, pol llm.RetryPolicy, method, path string, body any) (*http.Response, error) {
	var buf []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("ollama %s: encode request: %w", p.id, err)
		}
		buf = b
	}
	resp, err := llm.Retry(ctx, pol, func() (*http.Response, error) {
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

// One attempt. The body is bytes, not a reader: a reader is read once.
func (p *Provider) send(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	r, err := http.NewRequestWithContext(ctx, method, p.base.JoinPath(path).String(), rd)
	if err != nil {
		return nil, fmt.Errorf("ollama %s: %w", p.id, err)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.hc.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("ollama %s: %w", p.id, ctx.Err())
		}
		return nil, &llm.ProviderError{Provider: p.id, Message: err.Error(), Kind: llm.ErrUnavailable, Err: err}
	}
	return resp, nil
}

// Ollama says what went wrong in a bare "error" string, and the same
// wording turns up on a 404 and inside a stream.
func (p *Provider) statusError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	msg := strings.TrimSpace(string(raw))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		msg = e.Error
	}
	pe := &llm.ProviderError{Provider: p.id, Status: resp.StatusCode, Message: msg}
	switch resp.StatusCode {
	case http.StatusNotFound:
		pe.Kind = kindFromMessage(msg)
	case http.StatusUnauthorized, http.StatusForbidden:
		pe.Kind = llm.ErrUnauthorized
	case http.StatusTooManyRequests:
		pe.Kind = llm.ErrRateLimited
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		pe.Kind = llm.ErrUnavailable
	case http.StatusBadRequest:
		// Ollama usually shifts the window instead of failing, but a runtime behind
		// the same API may not.
		if llm.IsContextFull(msg) {
			pe.Kind = llm.ErrContextFull
		}
	}
	return pe
}

// "model 'x' not found" - the same wording on 404 and in a stream.
func kindFromMessage(msg string) error {
	if strings.Contains(strings.ToLower(msg), "model") && strings.Contains(strings.ToLower(msg), "not found") {
		return llm.ErrModelNotFound
	}
	return nil
}
