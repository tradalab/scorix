// Package lemonade runs AMD's Lemonade server for an app: one process that
// serves chat, embeddings and speech to text over the OpenAI API and fetches
// its own backends (llama.cpp, whisper.cpp) for the machine it runs on.
package lemonade

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/tradalab/scorix/llm"
	"github.com/tradalab/scorix/llm/openai"
)

type Config struct {
	ID string
	// Up to /api/v1, where Lemonade serves both the OpenAI API and its own.
	BaseURL string
	// Chat, embed and transcribe when empty.
	Capabilities []llm.Capability
	HTTPClient   *http.Client
}

// The OpenAI API for inference; Lemonade's own endpoints for the models it
// keeps, which that API has no words for.
type Provider struct {
	*openai.Provider
	base string
	hc   *http.Client
}

var _ llm.ModelManager = (*Provider)(nil)

func New(cfg Config) (*Provider, error) {
	caps := cfg.Capabilities
	if len(caps) == 0 {
		caps = []llm.Capability{llm.Chat, llm.Embed, llm.Transcribe}
	}
	hc := cfg.HTTPClient
	if hc == nil {
		// No overall timeout: a pull runs as long as the download does.
		hc = &http.Client{}
	}
	op, err := openai.New(openai.Config{ID: cfg.ID, BaseURL: cfg.BaseURL, Capabilities: caps, HTTPClient: hc})
	if err != nil {
		return nil, err
	}
	return &Provider{Provider: op, base: strings.TrimRight(cfg.BaseURL, "/"), hc: hc}, nil
}

type pullEvent struct {
	File        string `json:"file"`
	FileIndex   int    `json:"file_index"`
	TotalFiles  int    `json:"total_files"`
	Done        int64  `json:"bytes_downloaded"`
	Total       int64  `json:"bytes_total"`
	Percent     int    `json:"percent"`
	Code        string `json:"code"`
	ErrorString string `json:"error"`
}

// Lemonade 11.9.0 sends a progress event every few hundred bytes, 107,010 of
// them for a 77 MB model; progress hears of each whole percent once.
func (p *Provider) Pull(ctx context.Context, model string, progress func(llm.PullProgress)) error {
	resp, err := p.post(ctx, "pull", map[string]any{"model_name": model, "stream": true})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	lastFile, lastPercent := -1, -1
	var event string
	br := bufio.NewReader(resp.Body)
	for {
		line, readErr := br.ReadString('\n')
		// Before the half-written line it comes with is parsed: a download the
		// network cut short would otherwise read as a server sending nonsense,
		// and only one of the two is worth retrying.
		if readErr != nil && readErr != io.EOF {
			if ctx.Err() != nil {
				return fmt.Errorf("llm %s: %w", p.ID(), ctx.Err())
			}
			return &llm.ProviderError{Provider: p.ID(), Message: "pull broke: " + readErr.Error(), Kind: llm.ErrUnavailable}
		}
		line = strings.TrimRight(line, "\r\n")
		if name, ok := strings.CutPrefix(line, "event:"); ok {
			event = strings.TrimSpace(name)
		} else if data, ok := strings.CutPrefix(line, "data:"); ok {
			data = strings.TrimSpace(data)
			if !json.Valid([]byte(data)) {
				// Half a frame. A server that dies mid-download closes cleanly,
				// so the last line arrives with io.EOF, and that is a broken
				// connection rather than a server sending nonsense.
				if readErr == io.EOF {
					return &llm.ProviderError{Provider: p.ID(), Message: "pull ended mid-event", Kind: llm.ErrUnavailable}
				}
				return &llm.ProviderError{Provider: p.ID(), Message: "unreadable pull event"}
			}
			var e pullEvent
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				return &llm.ProviderError{Provider: p.ID(), Message: "unreadable pull event: " + err.Error()}
			}
			switch event {
			case "complete":
				return nil
			case "error":
				// A stream already answered 200, so the failure only lives here.
				return p.coded(0, e.Code, e.ErrorString)
			case "progress":
				if progress != nil && e.Total > 0 && (e.FileIndex != lastFile || e.Percent != lastPercent) {
					lastFile, lastPercent = e.FileIndex, e.Percent
					progress(llm.PullProgress{Status: fmt.Sprintf("downloading %s (%d/%d)", e.File, e.FileIndex, e.TotalFiles), Done: e.Done, Total: e.Total})
				}
			}
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("llm %s: %w", p.ID(), ctx.Err())
			}
			// A server that dies mid-download closes the stream cleanly; only the
			// missing complete event says the model is not there.
			return &llm.ProviderError{Provider: p.ID(), Message: "pull ended before it completed", Kind: llm.ErrUnavailable}
		}
	}
}

// Capabilities come from the labels Lemonade's own registry gives the model.
func (p *Provider) Describe(ctx context.Context, model string) (*llm.ModelDetail, error) {
	resp, err := p.request(ctx, http.MethodGet, "models/"+url.PathEscape(model), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		ID            string   `json:"id"`
		Labels        []string `json:"labels"`
		ContextLength int      `json:"context_length"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, llm.Unreadable(p.ID(), "unreadable model", err)
	}
	d := &llm.ModelDetail{ID: out.ID, ContextLength: out.ContextLength}
	for _, l := range out.Labels {
		if c, ok := labelCaps[l]; ok {
			d.Capabilities = append(d.Capabilities, c)
		}
	}
	return d, nil
}

var labelCaps = map[string]llm.Capability{
	"chat":          llm.Chat,
	"embeddings":    llm.Embed,
	"vision":        llm.Vision,
	"tool-calling":  llm.Tools,
	"transcription": llm.Transcribe,
}

// Lemonade answers success for a model that was never downloaded, so this is
// safe to repeat.
func (p *Provider) Delete(ctx context.Context, model string) error {
	resp, err := p.post(ctx, "delete", map[string]any{"model_name": model})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (p *Provider) post(ctx context.Context, path string, body any) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("llm %s: encode request: %w", p.ID(), err)
	}
	return p.request(ctx, http.MethodPost, path, bytes.NewReader(buf))
}

func (p *Provider) request(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	r, err := http.NewRequestWithContext(ctx, method, p.base+"/"+path, body)
	if err != nil {
		return nil, fmt.Errorf("llm %s: %w", p.ID(), err)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.hc.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("llm %s: %w", p.ID(), ctx.Err())
		}
		return nil, &llm.ProviderError{Provider: p.ID(), Message: err.Error(), Kind: llm.ErrUnavailable, Err: err}
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, p.failure(resp)
	}
	return resp, nil
}

// Two shapes: the OpenAI envelope {"error":{"code","message"}} on the
// inference and model endpoints, and {"code","error":"text"} on pull.
func (p *Provider) failure(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var flat struct {
		Code  string          `json:"code"`
		Error json.RawMessage `json:"error"`
	}
	code, msg := "", strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &flat) == nil && len(flat.Error) > 0 {
		var nested struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		var text string
		switch {
		case json.Unmarshal(flat.Error, &text) == nil:
			code, msg = flat.Code, text
		case json.Unmarshal(flat.Error, &nested) == nil && nested.Message != "":
			code, msg = nested.Code, nested.Message
		}
	}
	return p.coded(resp.StatusCode, code, msg)
}

func (p *Provider) coded(status int, code, msg string) error {
	e := &llm.ProviderError{Provider: p.ID(), Status: status, Message: msg}
	switch {
	case code == "model_not_found" || code == "unknown_model":
		e.Kind = llm.ErrModelNotFound
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		e.Kind = llm.ErrUnauthorized
	case status == http.StatusServiceUnavailable:
		e.Kind = llm.ErrUnavailable
	}
	return e
}
