// Package llmtest stands in for a model in an app's tests: a scripted Provider
// for logic that only needs answers, and a Cassette that replays real HTTP
// traffic for code that talks to a server.
package llmtest

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"

	"github.com/tradalab/scorix/llm"
)

type Reply struct {
	Text      string
	Reasoning string
	ToolCalls []llm.ToolCall
	Usage     llm.Usage
	// Handed back on the result, for testing that an app sends it again.
	Replay *llm.Replay
	// Returned instead of an answer, for testing how the app handles failure.
	Err error
}

type Provider struct {
	// Answered by Local, for testing slots marked LocalOnly.
	OnThisMachine bool
	// Width of the vectors Embed returns; 8 when zero.
	Dims int

	id       string
	caps     []llm.Capability
	mu       sync.Mutex
	replies  []Reply
	requests []llm.ChatRequest
}

// Chat and embed when no capabilities are given.
func New(id string, caps ...llm.Capability) *Provider {
	if len(caps) == 0 {
		caps = []llm.Capability{llm.Chat, llm.Embed}
	}
	return &Provider{id: id, caps: caps}
}

func (p *Provider) ID() string                     { return p.id }
func (p *Provider) Capabilities() []llm.Capability { return p.caps }
func (p *Provider) Local() bool                    { return p.OnThisMachine }

// Queued in order; each Chat takes the next one.
func (p *Provider) Reply(r ...Reply) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.replies = append(p.replies, r...)
	return p
}

func (p *Provider) Requests() []llm.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.ChatRequest(nil), p.requests...)
}

// An empty queue is an error, not an empty answer: a test that asks more often
// than it scripted has a bug that a silent "" would hide.
func (p *Provider) Chat(ctx context.Context, req llm.ChatRequest, onChunk func(llm.Chunk) error) (*llm.ChatResult, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	n := len(p.requests)
	if len(p.replies) == 0 {
		p.mu.Unlock()
		return nil, fmt.Errorf("llmtest %s: no reply queued for request %d", p.id, n)
	}
	r := p.replies[0]
	p.replies = p.replies[1:]
	p.mu.Unlock()
	if r.Err != nil {
		return nil, r.Err
	}

	emit := func(c llm.Chunk) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("llmtest %s: %w", p.id, err)
		}
		if onChunk == nil {
			return nil
		}
		return onChunk(c)
	}
	if r.Reasoning != "" {
		if err := emit(llm.Chunk{Kind: llm.ChunkReasoning, Text: r.Reasoning}); err != nil {
			return nil, err
		}
	}
	// Word by word, so an app's streaming path runs the way it does against a
	// server rather than on a single chunk.
	for _, w := range words(r.Text) {
		if err := emit(llm.Chunk{Kind: llm.ChunkText, Text: w}); err != nil {
			return nil, err
		}
	}
	for i := range r.ToolCalls {
		if err := emit(llm.Chunk{Kind: llm.ChunkToolCall, Call: &r.ToolCalls[i]}); err != nil {
			return nil, err
		}
	}
	finish := "stop"
	if len(r.ToolCalls) > 0 {
		finish = "tool_calls"
	}
	return &llm.ChatResult{Model: req.Model, Content: r.Text, Reasoning: r.Reasoning, ToolCalls: r.ToolCalls, FinishReason: finish, Usage: r.Usage, Replay: r.Replay}, nil
}

func words(s string) []string {
	var out []string
	for s != "" {
		i := strings.IndexByte(s, ' ')
		if i < 0 {
			return append(out, s)
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

// The same text always gets the same vector and different texts almost never
// do, which is what a test of search or dedup needs; the numbers mean nothing.
func (p *Provider) Embed(ctx context.Context, req llm.EmbedRequest) (*llm.EmbedResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("llmtest %s: %w", p.id, err)
	}
	dims := p.Dims
	if dims <= 0 {
		dims = 8
	}
	res := &llm.EmbedResult{Model: req.Model, Vectors: make([][]float32, len(req.Input))}
	for i, in := range req.Input {
		v := make([]float32, dims)
		for d := range v {
			h := fnv.New64a()
			fmt.Fprintf(h, "%d\x00%s", d, in)
			v[d] = float32(h.Sum64()%2000)/1000 - 1
		}
		res.Vectors[i] = v
	}
	return res, nil
}
