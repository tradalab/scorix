package llm

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// A slot is a place in the app that needs a model - "chat", "summarize",
// "search" - named by the app, because which slots exist is the app's feature
// set, not the framework's.
type Binding struct {
	Provider string
	Model    string
	// For slots that read what must not leave the machine. A LAN host is not
	// local: the data would still cross the network.
	LocalOnly bool
}

type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	order     []string
	bindings  map[string]Binding
}

func NewRegistry() *Registry {
	return &Registry{providers: map[string]Provider{}, bindings: map[string]Binding{}}
}

// Refuses a second provider under the same ID instead of replacing it: two
// providers colliding on an ID is a bug that replacing would hide. Changing a
// provider's settings is Unregister, then Register.
func (r *Registry) Register(p Provider) error {
	id := p.ID()
	if id == "" {
		return fmt.Errorf("llm: provider with an empty ID")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.providers[id]; dup {
		return fmt.Errorf("llm: provider %q is already registered", id)
	}
	r.providers[id] = p
	r.order = append(r.order, id)
	return nil
}

// Bindings that name the provider stay: resolving them reports
// ErrUnknownProvider, which is what the app should tell its user.
//
// A provider holding resources of its own is closed on the way out, outside the
// lock. This is the only door out - Register refuses a duplicate rather than
// replacing one - so without it a settings screen saved twice leaves the first
// provider's transport holding idle sockets with nothing pointing at them.
func (r *Registry) Unregister(id string) {
	r.mu.Lock()
	p, ok := r.providers[id]
	if ok {
		delete(r.providers, id)
		for i, have := range r.order {
			if have == id {
				r.order = append(r.order[:i], r.order[i+1:]...)
				break
			}
		}
	}
	r.mu.Unlock()
	if c, ok := p.(io.Closer); ok {
		_ = c.Close()
	}
}

func (r *Registry) Provider(id string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[id]
	return p, ok
}

// In registration order, so a settings screen lists providers the way the app
// added them.
func (r *Registry) Providers() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.providers[id])
	}
	return out
}

// Checked against the registered providers so a typo in a stored setting fails
// here, at startup, rather than on the first request.
func (r *Registry) Bind(slot string, b Binding) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.providers[b.Provider]
	if !ok {
		return fmt.Errorf("bind %q to %q: %w", slot, b.Provider, ErrUnknownProvider)
	}
	if b.LocalOnly && !IsLocal(p) {
		return fmt.Errorf("bind %q to %q: %w", slot, b.Provider, ErrNotLocal)
	}
	r.bindings[slot] = b
	return nil
}

// A slot with no binding reports ErrNoBinding, which is how a settings screen
// that cleared it says "choose a model" rather than using the last one.
func (r *Registry) Unbind(slot string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.bindings, slot)
}

func (r *Registry) Binding(slot string) (Binding, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.bindings[slot]
	return b, ok
}

func (r *Registry) resolve(slot string, need Capability) (Provider, Binding, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.bindings[slot]
	if !ok {
		return nil, Binding{}, fmt.Errorf("slot %q: %w", slot, ErrNoBinding)
	}
	p, ok := r.providers[b.Provider]
	if !ok {
		return nil, Binding{}, fmt.Errorf("slot %q: provider %q: %w", slot, b.Provider, ErrUnknownProvider)
	}
	if !Has(p, need) {
		return nil, Binding{}, fmt.Errorf("slot %q: provider %q cannot %s: %w", slot, b.Provider, need, ErrUnsupported)
	}
	// Checked again here, not only at Bind: the provider under this ID can be
	// unregistered and replaced by a remote one after the slot was bound.
	if b.LocalOnly && !IsLocal(p) {
		return nil, Binding{}, fmt.Errorf("slot %q: provider %q: %w", slot, b.Provider, ErrNotLocal)
	}
	return p, b, nil
}

func (r *Registry) Chat(ctx context.Context, slot string, req ChatRequest, onChunk func(Chunk) error) (*ChatResult, error) {
	p, b, err := r.resolve(slot, Chat)
	if err != nil {
		return nil, err
	}
	// Refused here rather than by the server: a provider that cannot see images
	// often answers anyway, describing a picture it never received.
	for _, need := range needs(req) {
		if !Has(p, need.cap) {
			return nil, fmt.Errorf("slot %q: provider %q cannot %s: %w", slot, b.Provider, need.what, ErrUnsupported)
		}
	}
	c, ok := p.(Chatter)
	if !ok {
		return nil, fmt.Errorf("slot %q: provider %q declares chat but does not implement it: %w", slot, b.Provider, ErrUnsupported)
	}
	if req.Model == "" {
		req.Model = b.Model
	}
	return c.Chat(ctx, req, onChunk)
}

func (r *Registry) Transcribe(ctx context.Context, slot string, req TranscribeRequest) (*TranscribeResult, error) {
	p, b, err := r.resolve(slot, Transcribe)
	if err != nil {
		return nil, err
	}
	tr, ok := p.(Transcriber)
	if !ok {
		return nil, fmt.Errorf("slot %q: provider %q declares transcribe but does not implement it: %w", slot, b.Provider, ErrUnsupported)
	}
	if req.Model == "" {
		req.Model = b.Model
	}
	return tr.Transcribe(ctx, req)
}

func (r *Registry) Embed(ctx context.Context, slot string, req EmbedRequest) (*EmbedResult, error) {
	p, b, err := r.resolve(slot, Embed)
	if err != nil {
		return nil, err
	}
	e, ok := p.(Embedder)
	if !ok {
		return nil, fmt.Errorf("slot %q: provider %q declares embed but does not implement it: %w", slot, b.Provider, ErrUnsupported)
	}
	if req.Model == "" {
		req.Model = b.Model
	}
	return e.Embed(ctx, req)
}

type need struct {
	cap  Capability
	what string
}

func needs(req ChatRequest) []need {
	var images, audio bool
	for _, m := range req.Messages {
		images = images || len(m.Images) > 0
		audio = audio || len(m.Audio) > 0
	}
	var out []need
	if images {
		out = append(out, need{Vision, "see images"})
	}
	if audio {
		out = append(out, need{Audio, "hear audio"})
	}
	if len(req.Tools) > 0 {
		out = append(out, need{Tools, "call tools"})
	}
	return out
}
