package llm

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type fakeProvider struct {
	id   string
	caps []Capability
	// Guarded: the registry is read from many goroutines at once, and a fake
	// that is not safe itself reports its own race as the registry's.
	mu    sync.Mutex
	calls int
	got   ChatRequest
}

func (f *fakeProvider) ID() string                 { return f.id }
func (f *fakeProvider) Capabilities() []Capability { return f.caps }

func (f *fakeProvider) seen(req ChatRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.got = req
}

func (f *fakeProvider) Chat(_ context.Context, req ChatRequest, onChunk func(Chunk) error) (*ChatResult, error) {
	f.seen(req)
	if onChunk != nil {
		if err := onChunk(Chunk{Kind: ChunkText, Text: "hi"}); err != nil {
			return nil, err
		}
	}
	return &ChatResult{Model: req.Model, Content: "hi"}, nil
}

func (f *fakeProvider) Embed(_ context.Context, req EmbedRequest) (*EmbedResult, error) {
	f.seen(ChatRequest{Model: req.Model})
	return &EmbedResult{Model: req.Model, Vectors: make([][]float32, len(req.Input))}, nil
}

type localProvider struct {
	fakeProvider
	local bool
}

func (l *localProvider) Local() bool { return l.local }

// Declares chat without implementing it: a provider that lies about itself.
type declaresOnly struct{ id string }

func (d declaresOnly) ID() string                 { return d.id }
func (d declaresOnly) Capabilities() []Capability { return []Capability{Chat, Embed} }

func mustRegister(t *testing.T, r *Registry, p Provider) {
	t.Helper()
	if err := r.Register(p); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterRefusesACollidingOrEmptyID(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, &fakeProvider{id: "local", caps: []Capability{Chat}})
	if err := r.Register(&fakeProvider{id: "local"}); err == nil {
		t.Error("a second provider under the same ID replaced the first in silence")
	}
	if err := r.Register(&fakeProvider{id: ""}); err == nil {
		t.Error("a provider with no ID was registered")
	}
}

func TestBindRefusesAProviderThatIsNotThere(t *testing.T) {
	r := NewRegistry()
	if err := r.Bind("chat", Binding{Provider: "typo"}); !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("binding to an unregistered provider = %v, want ErrUnknownProvider", err)
	}
}

func TestChatNeedsABoundSlotWithTheCapability(t *testing.T) {
	r := NewRegistry()
	embedOnly := &fakeProvider{id: "vec", caps: []Capability{Embed}}
	mustRegister(t, r, embedOnly)
	mustRegister(t, r, declaresOnly{id: "liar"})

	if _, err := r.Chat(context.Background(), "chat", ChatRequest{}, nil); !errors.Is(err, ErrNoBinding) {
		t.Errorf("chat on an unbound slot = %v, want ErrNoBinding", err)
	}

	if err := r.Bind("chat", Binding{Provider: "vec"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Chat(context.Background(), "chat", ChatRequest{}, nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("chat on an embed-only provider = %v, want ErrUnsupported", err)
	}
	if embedOnly.calls != 0 {
		t.Error("the provider was called for a capability it does not have")
	}

	if err := r.Bind("chat", Binding{Provider: "liar"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Chat(context.Background(), "chat", ChatRequest{}, nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("chat on a provider that declares but does not implement it = %v, want ErrUnsupported", err)
	}
}

type declaresTranscribe struct{}

func (declaresTranscribe) ID() string                 { return "stt" }
func (declaresTranscribe) Capabilities() []Capability { return []Capability{Transcribe} }

func TestTranscribeNeedsAProviderThatImplementsIt(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, declaresTranscribe{})
	if err := r.Bind("dictation", Binding{Provider: "stt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Transcribe(context.Background(), "dictation", TranscribeRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a provider that only declares transcription: %v", err)
	}
}

// A model without vision often answers anyway, describing a picture it never got.
func TestImagesNeverReachAProviderWithoutVision(t *testing.T) {
	r := NewRegistry()
	blind := &fakeProvider{id: "blind", caps: []Capability{Chat}}
	mustRegister(t, r, blind)
	if err := r.Bind("chat", Binding{Provider: "blind"}); err != nil {
		t.Fatal(err)
	}
	req := ChatRequest{Messages: []Message{{Role: User, Content: "what is this", Images: []Image{{MIME: "image/png", Data: []byte{1}}}}}}
	if _, err := r.Chat(context.Background(), "chat", req, nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("an image to a blind provider = %v, want ErrUnsupported", err)
	}
	if blind.calls != 0 {
		t.Error("the image reached a provider that cannot see it")
	}
}

// Same failure as images: a model without the input answers anyway. One that
// cannot call tools ignores them and replies in prose the app then acts on.
func TestAudioAndToolsNeedAProviderThatDeclaresThem(t *testing.T) {
	heard := Message{Role: User, Audio: []AudioClip{{MIME: "audio/wav", Data: []byte{1}}}}
	tool := []Tool{{Name: "lookup", Parameters: []byte(`{"type":"object"}`)}}
	for _, tc := range []struct {
		name string
		caps []Capability
		req  ChatRequest
		ok   bool
	}{
		{"audio, deaf", []Capability{Chat, Vision, Tools}, ChatRequest{Messages: []Message{heard}}, false},
		{"audio, hears", []Capability{Chat, Audio}, ChatRequest{Messages: []Message{heard}}, true},
		{"tools, cannot call", []Capability{Chat, Vision, Audio}, ChatRequest{Tools: tool}, false},
		{"tools, can call", []Capability{Chat, Tools}, ChatRequest{Tools: tool}, true},
	} {
		r := NewRegistry()
		p := &fakeProvider{id: "p", caps: tc.caps}
		mustRegister(t, r, p)
		if err := r.Bind("chat", Binding{Provider: "p"}); err != nil {
			t.Fatal(err)
		}
		_, err := r.Chat(context.Background(), "chat", tc.req, nil)
		if tc.ok != (err == nil) || (!tc.ok && !errors.Is(err, ErrUnsupported)) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
		if !tc.ok && p.calls != 0 {
			t.Errorf("%s: the provider was called", tc.name)
		}
	}
}

func TestTheSlotsModelFillsOnlyAnEmptyRequestModel(t *testing.T) {
	r := NewRegistry()
	p := &fakeProvider{id: "local", caps: []Capability{Chat}}
	mustRegister(t, r, p)
	if err := r.Bind("summarize", Binding{Provider: "local", Model: "small"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Chat(context.Background(), "summarize", ChatRequest{}, nil); err != nil {
		t.Fatal(err)
	}
	if p.got.Model != "small" {
		t.Errorf("empty request model became %q, want the slot's %q", p.got.Model, "small")
	}
	if _, err := r.Chat(context.Background(), "summarize", ChatRequest{Model: "big"}, nil); err != nil {
		t.Fatal(err)
	}
	if p.got.Model != "big" {
		t.Errorf("an explicit model was overridden to %q", p.got.Model)
	}
}

func TestUnregisterLeavesTheBindingReportingWhy(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, &fakeProvider{id: "a", caps: []Capability{Embed}})
	mustRegister(t, r, &fakeProvider{id: "b", caps: []Capability{Embed}})
	if err := r.Bind("search", Binding{Provider: "a"}); err != nil {
		t.Fatal(err)
	}
	r.Unregister("a")
	if _, err := r.Embed(context.Background(), "search", EmbedRequest{Input: []string{"x"}}); !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("embed through a slot whose provider was removed = %v, want ErrUnknownProvider", err)
	}
	if ps := r.Providers(); len(ps) != 1 || ps[0].ID() != "b" {
		t.Errorf("providers after unregister = %v", ps)
	}
}

func TestAnUnboundSlotAsksForAModel(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, &fakeProvider{id: "a", caps: []Capability{Embed}})
	if err := r.Bind("search", Binding{Provider: "a"}); err != nil {
		t.Fatal(err)
	}
	r.Unbind("search")
	r.Unbind("never-bound")
	if _, err := r.Embed(context.Background(), "search", EmbedRequest{Input: []string{"x"}}); !errors.Is(err, ErrNoBinding) {
		t.Errorf("embed through an unbound slot = %v, want ErrNoBinding", err)
	}
	if _, ok := r.Binding("search"); ok {
		t.Error("the binding is still there")
	}
}

func TestProvidersKeepRegistrationOrder(t *testing.T) {
	r := NewRegistry()
	for _, id := range []string{"openai", "local", "groq"} {
		mustRegister(t, r, &fakeProvider{id: id})
	}
	var got []string
	for _, p := range r.Providers() {
		got = append(got, p.ID())
	}
	if len(got) != 3 || got[0] != "openai" || got[1] != "local" || got[2] != "groq" {
		t.Errorf("order = %v", got)
	}
}

// The registry is read on every request and written from a settings screen.
func TestRegistryIsSafeUnderConcurrentUse(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, &fakeProvider{id: "local", caps: []Capability{Chat}})
	if err := r.Bind("chat", Binding{Provider: "local"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = r.Bind("chat", Binding{Provider: "local", Model: "m"})
			_ = r.Register(&fakeProvider{id: "x"})
			r.Unregister("x")
		}()
		go func() {
			defer wg.Done()
			_, _ = r.Binding("chat")
			_ = r.Providers()
			// The read a request actually takes. Without it the test exercises
			// every path but the one the lock exists for, and go test without
			// -race would be green either way, so the suite runs with it.
			_, _ = r.Chat(context.Background(), "chat", ChatRequest{}, nil)
			_, _ = r.Embed(context.Background(), "chat", EmbedRequest{})
		}()
	}
	wg.Wait()
}

type closingProvider struct {
	fakeProvider
	closed int
}

func (c *closingProvider) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed++
	return nil
}

// Register refuses a duplicate, so changing a provider's settings goes out
// through Unregister: if that does not close the old one, its transport keeps
// its idle sockets for the whole idle timeout with nothing pointing at it.
func TestUnregisterClosesTheProviderItDrops(t *testing.T) {
	r := NewRegistry()
	p := &closingProvider{fakeProvider: fakeProvider{id: "work", caps: []Capability{Chat}}}
	if err := r.Register(p); err != nil {
		t.Fatal(err)
	}
	r.Unregister("work")
	p.mu.Lock()
	n := p.closed
	p.mu.Unlock()
	if n != 1 {
		t.Errorf("closed %d times, want 1", n)
	}
	// Nothing to drop is not something to close.
	r.Unregister("work")
	p.mu.Lock()
	n = p.closed
	p.mu.Unlock()
	if n != 1 {
		t.Errorf("closed %d times after a second Unregister", n)
	}
	// A provider with nothing to close still unregisters.
	plain := &fakeProvider{id: "plain", caps: []Capability{Chat}}
	if err := r.Register(plain); err != nil {
		t.Fatal(err)
	}
	r.Unregister("plain")
	if _, ok := r.Provider("plain"); ok {
		t.Error("still registered")
	}
}
