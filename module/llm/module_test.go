package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tradalab/scorix/config"
	"github.com/tradalab/scorix/fault"
	core "github.com/tradalab/scorix/llm"
	"github.com/tradalab/scorix/llm/detect"
	"github.com/tradalab/scorix/module"
	"github.com/tradalab/scorix/secrets"
)

type fakeCore struct {
	mu       sync.Mutex
	handlers map[string]func(context.Context, json.RawMessage) (any, error)
	needs    map[string]module.Permission
	events   []PullEvent
}

func (c *fakeCore) Register(name string, needs module.Permission, exec func(context.Context, json.RawMessage) (any, error)) {
	c.handlers[name] = exec
	if c.needs == nil {
		c.needs = map[string]module.Permission{}
	}
	c.needs[name] = needs
}

func (c *fakeCore) Invoke(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("not used")
}

func (c *fakeCore) Emit(_ context.Context, name string, data json.RawMessage) error {
	if name != "mod:llm:pull" {
		return fmt.Errorf("event %s", name)
	}
	var ev PullEvent
	_ = json.Unmarshal(data, &ev)
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
	return nil
}

func (c *fakeCore) pulled() []PullEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.events)
}

var testKey = []byte("0123456789abcdef0123456789abcdef")

var slots = []Slot{
	{Name: "chat", Label: "Chat", Needs: []core.Capability{core.Chat}},
	{Name: "dictation", Label: "Dictation", Needs: []core.Capability{core.Transcribe}, LocalOnly: true},
}

type rig struct {
	t    *testing.T
	mod  *Module
	core *fakeCore
	path string
}

// Loaded the way an app loads it, so every call below crosses the same JSON
// the frontend sends.
func load(t *testing.T, path string, before ...func(*Module)) *rig {
	t.Helper()
	sealer, err := secrets.NewWithKey(testKey)
	if err != nil {
		t.Fatal(err)
	}
	c := &fakeCore{handlers: map[string]func(context.Context, json.RawMessage) (any, error){}}
	mod := New(Options{Slots: slots, Secrets: sealer, Path: path, Confirm: func(context.Context, string) (bool, error) { return true, nil }})
	// Before LoadAll, the way an app registers the engines it runs itself.
	for _, fn := range before {
		fn(mod)
	}
	mgr := module.NewManager(&config.Config{Modules: map[string]any{"llm": map[string]any{"enabled": true, "pull_allow": []any{"*"}}}}, c, nil)
	mgr.Register(mod)
	if err := mgr.LoadAll(); err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, mod: mod, core: c, path: path}
}

func newRig(t *testing.T) *rig { return load(t, filepath.Join(t.TempDir(), "llm.json")) }

func (r *rig) call(name string, in, out any) error {
	r.t.Helper()
	h, ok := r.core.handlers["mod:llm:"+name]
	if !ok {
		r.t.Fatalf("no handler for %s", name)
	}
	raw, _ := json.Marshal(in)
	res, err := h(context.Background(), raw)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(res)
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			r.t.Fatal(err)
		}
	}
	return nil
}

func (r *rig) must(name string, in, out any) {
	r.t.Helper()
	if err := r.call(name, in, out); err != nil {
		r.t.Fatalf("%s: %v", name, err)
	}
}

// An OpenAI-shaped server that records the key it was sent.
func openaiServer(t *testing.T) (*httptest.Server, *string) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"data":[{"id":"gpt-small"},{"id":"gpt-large"}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &auth
}

func TestAKeyIsSealedOnDiskAndUsedOnEveryRequest(t *testing.T) {
	r := newRig(t)
	srv, auth := openaiServer(t)
	var st State
	r.must("SaveProvider", ProviderInput{ID: "work", Driver: "openai", Values: map[string]string{"base_url": srv.URL + "/v1", "tools": "true"}, Secrets: map[string]string{"api_key": "sk-secret-1"}}, &st)
	if len(st.Providers) != 1 {
		t.Fatalf("state %+v", st)
	}
	p := st.Providers[0]
	if p.ID != "work" || p.Driver != "openai" || p.Values["base_url"] != srv.URL+"/v1" || !slices.Equal(p.Secrets, []string{"api_key"}) ||
		!p.Editable || !p.Local || p.Manages || p.Error != "" || !slices.Contains(p.Capabilities, core.Tools) {
		t.Errorf("provider %+v", p)
	}
	// A second provider with its own key: each request reads its own.
	r.must("SaveProvider", ProviderInput{ID: "home", Driver: "openai", Values: map[string]string{"base_url": srv.URL}, Secrets: map[string]string{"api_key": "sk-home"}}, nil)
	var models []ModelView
	r.must("Models", "work", &models)
	if len(models) != 2 || models[0].ID != "gpt-small" || *auth != "Bearer sk-secret-1" {
		t.Errorf("models %+v, sent %q", models, *auth)
	}
	r.must("Bind", BindInput{Slot: "chat", Provider: "work", Model: "gpt-small"}, nil)
	onDisk, _ := os.ReadFile(r.path)
	if strings.Contains(string(onDisk), "sk-secret-1") || !strings.Contains(string(onDisk), "scorix:v1:") {
		t.Errorf("stored as %s", onDisk)
	}
	var wire map[string]any
	r.must("State", nil, &wire)
	if b, _ := json.Marshal(wire); strings.Contains(string(b), "sk-secret") || strings.Contains(string(b), "scorix:v1:") {
		t.Errorf("the key reached the frontend: %s", b)
	}

	// Another launch reads the same file and the same key.
	again := load(t, r.path)
	*auth = ""
	again.must("Models", "work", nil)
	if *auth != "Bearer sk-secret-1" {
		t.Errorf("after a restart the key sent was %q", *auth)
	}
	if b, ok := again.mod.Registry().Binding("chat"); !ok || b.Provider != "work" || b.Model != "gpt-small" {
		t.Errorf("after a restart the chat slot is bound to %+v", b)
	}
}

func TestAKeyLeftOutIsKeptAndAnEmptyOneRemoved(t *testing.T) {
	r := newRig(t)
	srv, auth := openaiServer(t)
	in := ProviderInput{ID: "work", Driver: "openai", Values: map[string]string{"base_url": srv.URL}, Secrets: map[string]string{"api_key": "sk-1"}}
	r.must("SaveProvider", in, nil)
	in.Secrets = nil
	in.Values["vision"] = "true"
	var st State
	r.must("SaveProvider", in, &st)
	r.must("Models", "work", nil)
	if *auth != "Bearer sk-1" || !slices.Contains(st.Providers[0].Capabilities, core.Vision) {
		t.Errorf("an edit that did not touch the key sent %q; caps %v", *auth, st.Providers[0].Capabilities)
	}
	in.Secrets = map[string]string{"api_key": ""}
	r.must("SaveProvider", in, &st)
	r.must("Models", "work", nil)
	if *auth != "" || len(st.Providers[0].Secrets) != 0 {
		t.Errorf("a cleared key still sent %q; state %v", *auth, st.Providers[0].Secrets)
	}
}

func TestSaveRefusesWhatWouldNotOpen(t *testing.T) {
	r := newRig(t)
	srv, _ := openaiServer(t)
	r.must("SaveProvider", ProviderInput{ID: "work", Driver: "openai", Values: map[string]string{"base_url": srv.URL}}, nil)
	before, _ := os.ReadFile(r.path)
	for name, in := range map[string]ProviderInput{
		"no base URL":    {ID: "other", Driver: "openai"},
		"a bad ID":       {ID: "Work Laptop", Driver: "openai", Values: map[string]string{"base_url": srv.URL}},
		"no such driver": {ID: "x", Driver: "gemini"},
		"a driver swap":  {ID: "work", Driver: "ollama", Values: map[string]string{"base_url": srv.URL}},
		"a broken edit":  {ID: "work", Driver: "openai", Values: map[string]string{"base_url": "not a url"}},
	} {
		if err := r.call("SaveProvider", in, nil); err == nil {
			t.Errorf("%s was saved", name)
		}
	}
	after, _ := os.ReadFile(r.path)
	var st State
	r.must("State", nil, &st)
	if string(before) != string(after) || len(st.Providers) != 1 || st.Providers[0].Values["base_url"] != srv.URL {
		t.Errorf("a refused save changed things: %s", after)
	}
	// The provider as it was still serves after a refused edit.
	if err := r.call("Models", "work", nil); err != nil {
		t.Errorf("after a refused edit: %v", err)
	}
}

func TestASlotTakesOnlyAProviderThatCanServeIt(t *testing.T) {
	r := newRig(t)
	local, _ := openaiServer(t)
	r.must("SaveProvider", ProviderInput{ID: "near", Driver: "openai", Values: map[string]string{"base_url": local.URL, "transcribe": "true"}}, nil)
	r.must("SaveProvider", ProviderInput{ID: "far", Driver: "openai", Values: map[string]string{"base_url": "https://api.example.com/v1", "transcribe": "true"}}, nil)
	r.must("SaveProvider", ProviderInput{ID: "mute", Driver: "openai", Values: map[string]string{"base_url": local.URL}}, nil)

	var st State
	r.must("Bind", BindInput{Slot: "chat", Provider: "far", Model: "gpt-small"}, &st)
	r.must("Bind", BindInput{Slot: "dictation", Provider: "near", Model: "whisper-1"}, &st)
	if st.Slots[0].Provider != "far" || st.Slots[0].Model != "gpt-small" || st.Slots[1].Provider != "near" || st.Slots[1].Error != "" {
		t.Errorf("slots %+v", st.Slots)
	}
	if !st.Slots[1].LocalOnly || st.Slots[1].Label != "Dictation" || !slices.Equal(st.Slots[1].Needs, []core.Capability{core.Transcribe}) {
		t.Errorf("slot as declared %+v", st.Slots[1])
	}
	// Apps show these to the user as they are, so each names its subject once.
	for name, c := range map[string]struct {
		in   BindInput
		says string
	}{
		"a remote provider for a local slot": {BindInput{Slot: "dictation", Provider: "far", Model: "whisper-1"}, `llm: slot "dictation": "far" does not run on this machine`},
		"a provider without the capability":  {BindInput{Slot: "dictation", Provider: "mute", Model: "whisper-1"}, `llm: slot "dictation": "mute" cannot transcribe`},
		"a slot the app does not have":       {BindInput{Slot: "summary", Provider: "near"}, `llm: the app has no slot "summary"`},
		"a provider that is not there":       {BindInput{Slot: "chat", Provider: "gone"}, `llm: unknown provider: "gone"`},
	} {
		if err := r.call("Bind", c.in, nil); err == nil || !strings.HasPrefix(err.Error(), c.says) {
			t.Errorf("bound %s: %v", name, err)
		}
	}
	if b, _ := r.mod.Registry().Binding("dictation"); b.Provider != "near" || !b.LocalOnly {
		t.Errorf("registry binding %+v", b)
	}
	var after State
	r.must("Unbind", "chat", &after)
	if after.Slots[0].Provider != "" {
		t.Errorf("unbound slot %+v", after.Slots[0])
	}
	if _, err := r.mod.Registry().Chat(context.Background(), "chat", core.ChatRequest{}, nil); !errors.Is(err, core.ErrNoBinding) {
		t.Errorf("chat through an unbound slot: %v", err)
	}
}

// A binding whose provider is gone or broken stays, and says so, rather than
// quietly becoming no binding at all.
func TestABrokenBindingSaysWhy(t *testing.T) {
	r := newRig(t)
	srv, _ := openaiServer(t)
	r.must("SaveProvider", ProviderInput{ID: "work", Driver: "openai", Values: map[string]string{"base_url": srv.URL}}, nil)
	r.must("SaveProvider", ProviderInput{ID: "spare", Driver: "openai", Values: map[string]string{"base_url": srv.URL, "transcribe": "true"}}, nil)
	r.must("Bind", BindInput{Slot: "chat", Provider: "work", Model: "m"}, nil)
	r.must("Bind", BindInput{Slot: "dictation", Provider: "spare", Model: "m"}, nil)
	var st State
	r.must("DeleteProvider", "work", &st)
	if len(st.Providers) != 1 || st.Slots[0].Provider != "work" || !strings.Contains(st.Slots[0].Error, "not there") {
		t.Errorf("after delete %+v", st)
	}

	// Edited on disk into something that no longer opens.
	b, _ := os.ReadFile(r.path)
	os.WriteFile(r.path, []byte(strings.Replace(string(b), srv.URL, "", 1)), 0o600)
	again := load(t, r.path)
	var restarted State
	again.must("State", nil, &restarted)
	if restarted.Providers[0].ID != "spare" || restarted.Providers[0].Error == "" || !strings.Contains(restarted.Slots[1].Error, "does not open") {
		t.Errorf("after restart %+v", restarted)
	}
	if !strings.Contains(restarted.Slots[0].Error, "not there") {
		t.Errorf("chat slot %+v", restarted.Slots[0])
	}

	// A saved binding whose provider lost a capability the slot needs.
	var edited State
	again.must("SaveProvider", ProviderInput{ID: "spare", Driver: "openai", Values: map[string]string{"base_url": srv.URL}}, &edited)
	if !strings.Contains(edited.Slots[1].Error, "cannot transcribe") || edited.Providers[0].Error != "" {
		t.Errorf("after fixing the provider: slot %+v, provider %+v", edited.Slots[1], edited.Providers[0])
	}
	// And one that now runs elsewhere, under a slot whose data must stay here.
	var moved State
	again.must("SaveProvider", ProviderInput{ID: "spare", Driver: "openai", Values: map[string]string{"base_url": "https://api.example.com/v1", "transcribe": "true"}}, &moved)
	if !strings.Contains(moved.Slots[1].Error, "does not run on this machine") {
		t.Errorf("dictation slot %+v", moved.Slots[1])
	}
}

type appProvider struct{ id string }

func (a appProvider) ID() string                      { return a.id }
func (a appProvider) Capabilities() []core.Capability { return []core.Capability{core.Chat} }
func (a appProvider) Local() bool                     { return true }
func (a appProvider) Chat(_ context.Context, req core.ChatRequest, _ func(core.Chunk) error) (*core.ChatResult, error) {
	return &core.ChatResult{Model: req.Model, Content: "from " + a.id}, nil
}

// A llama-server the app started itself: listed, bound, not editable.
func TestAProviderTheAppRunsTakesItsSavedBinding(t *testing.T) {
	r := newRig(t)
	if err := r.mod.Register(appProvider{"llama"}); err != nil {
		t.Fatal(err)
	}
	r.must("Bind", BindInput{Slot: "chat", Provider: "llama", Model: "qwen"}, nil)

	// Next launch: the binding is loaded before the app has started llama.
	again := load(t, r.path)
	if err := again.mod.Register(appProvider{"llama"}); err != nil {
		t.Fatal(err)
	}
	res, err := again.mod.Registry().Chat(context.Background(), "chat", core.ChatRequest{}, nil)
	if err != nil || res.Content != "from llama" || res.Model != "qwen" {
		t.Errorf("%+v %v", res, err)
	}
	var st State
	again.must("State", nil, &st)
	if len(st.Providers) != 1 || st.Providers[0].Editable || !st.Providers[0].Local || st.Slots[0].Error != "" {
		t.Errorf("state %+v", st)
	}
	if err := again.call("SaveProvider", ProviderInput{ID: "llama", Driver: "openai", Values: map[string]string{"base_url": "http://127.0.0.1:1"}}, nil); err == nil {
		t.Error("the app's own provider was overwritten from settings")
	}
	if err := again.call("DeleteProvider", "llama", nil); err == nil {
		t.Error("the app's own provider was deleted from settings")
	}
	srv, _ := openaiServer(t)
	again.must("SaveProvider", ProviderInput{ID: "work", Driver: "openai", Values: map[string]string{"base_url": srv.URL}}, nil)
	if err := again.mod.Register(appProvider{"work"}); err == nil {
		t.Error("the app took over a provider the user added")
	}
	again.mod.Unregister("work")
	again.mod.Unregister("llama")
	var after State
	again.must("State", nil, &after)
	if len(after.Providers) != 1 || after.Providers[0].ID != "work" {
		t.Errorf("after unregister %+v", after.Providers)
	}
	if _, ok := again.mod.Registry().Provider("work"); !ok {
		t.Error("the app unregistered a provider the user added")
	}
}

// Ollama's pull: one JSON line per step, the last saying success. A model comes
// in layers, and each one starts its own counter at zero.
func ollamaServer(t *testing.T, hold chan struct{}) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/pull":
			for i := 0; i <= 4; i++ {
				fmt.Fprintf(w, `{"status":"pulling abc","digest":"sha256:abc","total":400,"completed":%d}`+"\n", i*100)
				w.(http.Flusher).Flush()
				if hold != nil && i == 1 {
					// Bounded, so a pull that should have been refused fails the
					// test in seconds instead of holding it to the go test timeout.
					select {
					case <-hold:
					case <-r.Context().Done():
						return
					case <-time.After(10 * time.Second):
					}
				}
			}
			for i := 0; i <= 2; i++ {
				fmt.Fprintf(w, `{"status":"pulling def","digest":"sha256:def","total":250,"completed":%d}`+"\n", i*125)
				w.(http.Flusher).Flush()
			}
			fmt.Fprintln(w, `{"status":"success"}`)
		case "/api/show":
			_, _ = io.WriteString(w, `{"capabilities":["completion","vision"],"details":{"family":"qwen3"},"model_info":{"general.architecture":"qwen3","qwen3.context_length":4096}}`)
		case "/api/delete":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["model"] != "qwen3:0.6b" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"error":"model not found"}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPullReportsProgressAndEnds(t *testing.T) {
	r := newRig(t)
	srv := ollamaServer(t, nil)
	var st State
	r.must("SaveProvider", ProviderInput{ID: "ollama", Driver: "ollama", Values: map[string]string{"base_url": srv.URL}}, &st)
	if !st.Providers[0].Manages {
		t.Errorf("ollama does not manage models: %+v", st.Providers[0])
	}
	var done bool
	r.must("Pull", ModelInput{Provider: "ollama", Model: "qwen3:0.6b"}, &done)
	evs := r.core.pulled()
	if !done || len(evs) == 0 {
		t.Fatalf("done %v, events %+v", done, evs)
	}
	last := evs[len(evs)-1]
	if last.Provider != "ollama" || last.Model != "qwen3:0.6b" || last.Status != "success" {
		t.Errorf("last event %+v", last)
	}
	// Throttled, but the completed step is never the one dropped.
	if !slices.ContainsFunc(evs, func(e PullEvent) bool { return e.Done == 400 && e.Total == 400 }) {
		t.Errorf("events %+v", evs)
	}
	r.must("DeleteModel", ModelInput{Provider: "ollama", Model: "qwen3:0.6b"}, &done)
	if err := r.call("DeleteModel", ModelInput{Provider: "ollama", Model: "other"}, nil); err == nil {
		t.Error("deleting a model the runtime does not have succeeded")
	}
}

func TestAPullCanBeStoppedAndOnlyRunsOnce(t *testing.T) {
	r := newRig(t)
	hold := make(chan struct{})
	srv := ollamaServer(t, hold)
	r.must("SaveProvider", ProviderInput{ID: "ollama", Driver: "ollama", Values: map[string]string{"base_url": srv.URL}}, nil)
	in := ModelInput{Provider: "ollama", Model: "qwen3:0.6b"}
	type result struct {
		done bool
		err  error
	}
	got := make(chan result, 1)
	go func() {
		var done bool
		err := r.call("Pull", in, &done)
		got <- result{done, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(r.core.pulled()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := r.call("Pull", in, nil); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("a second pull of the same model: %v", err)
	}
	var stopped bool
	r.must("CancelPull", in, &stopped)
	select {
	case res := <-got:
		if res.done || res.err != nil || !stopped {
			t.Errorf("stopped pull returned %+v, cancel said %v", res, stopped)
		}
	case <-time.After(5 * time.Second):
		close(hold)
		t.Fatal("the pull did not stop")
	}
	if r.call("CancelPull", in, &stopped); stopped {
		t.Error("cancelled a pull that had already ended")
	}
}

// Guessing the answer from a model's name is how a text model gets sent a
// picture, and how a model that can see loses the attach button.
func TestDescribeAnswersForTheProviderNamed(t *testing.T) {
	r := newRig(t)
	srv := ollamaServer(t, nil)
	r.must("SaveProvider", ProviderInput{ID: "ollama", Driver: "ollama", Values: map[string]string{"base_url": srv.URL}}, nil)
	var d core.ModelDetail
	r.must("Describe", ModelInput{Provider: "ollama", Model: "qwen3:0.6b"}, &d)
	if d.ContextLength != 4096 || !slices.Contains(d.Capabilities, core.Vision) {
		t.Errorf("detail = %+v", d)
	}
	if err := r.call("Describe", ModelInput{Provider: "gone", Model: "m"}, nil); !errors.Is(err, core.ErrUnknownProvider) {
		t.Errorf("from nothing: %v", err)
	}
	// A provider with no per-model endpoint answers with what it says it can
	// do: an error would be read as "cannot", and the button would go.
	if err := r.mod.Register(appProvider{"plain"}); err != nil {
		t.Fatal(err)
	}
	var plain core.ModelDetail
	r.must("Describe", ModelInput{Provider: "plain", Model: "m"}, &plain)
	if plain.ID != "m" || !slices.Equal(plain.Capabilities, []core.Capability{core.Chat}) {
		t.Errorf("from a provider that does not describe: %+v", plain)
	}
	if plain.ContextLength != 0 {
		t.Errorf("invented a window: %d", plain.ContextLength)
	}
}

func TestAProviderWithoutModelManagementSaysSo(t *testing.T) {
	r := newRig(t)
	srv, _ := openaiServer(t)
	r.must("SaveProvider", ProviderInput{ID: "work", Driver: "openai", Values: map[string]string{"base_url": srv.URL}}, nil)
	if err := r.call("Pull", ModelInput{Provider: "work", Model: "m"}, nil); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("pull: %v", err)
	}
	if err := r.call("Pull", ModelInput{Provider: "gone", Model: "m"}, nil); !errors.Is(err, core.ErrUnknownProvider) || !strings.HasPrefix(err.Error(), `llm: unknown provider: "gone"`) {
		t.Errorf("pull from nothing: %v", err)
	}
	if err := r.call("Models", "gone", nil); !errors.Is(err, core.ErrUnknownProvider) || !strings.HasPrefix(err.Error(), `llm: unknown provider: "gone"`) {
		t.Errorf("models from nothing: %v", err)
	}
	// A provider that keeps no models of its own says so, rather than answering
	// an empty list that reads as "none installed".
	if err := r.mod.Register(appProvider{"nomodels"}); err != nil {
		t.Fatal(err)
	}
	if err := r.call("Models", "nomodels", nil); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("models from a provider that does not list them: %v", err)
	}
}

func TestDriversListTheirFields(t *testing.T) {
	r := newRig(t)
	var ds []DriverInfo
	r.must("Drivers", nil, &ds)
	i := slices.IndexFunc(ds, func(d DriverInfo) bool { return d.Name == "openai" })
	for _, name := range []string{"ollama", "anthropic"} {
		if i < 0 || !slices.ContainsFunc(ds, func(d DriverInfo) bool { return d.Name == name }) {
			t.Fatalf("no %s among drivers %+v", name, ds)
		}
	}
	key := slices.IndexFunc(ds[i].Fields, func(f FieldInfo) bool { return f.Key == "api_key" })
	url := slices.IndexFunc(ds[i].Fields, func(f FieldInfo) bool { return f.Key == "base_url" })
	if key < 0 || ds[i].Fields[key].Kind != "secret" || url < 0 || !ds[i].Fields[url].Required || ds[i].Fields[url].Kind != "url" {
		t.Errorf("openai fields %+v", ds[i].Fields)
	}
}

func TestDetectOffersWhatIsRunning(t *testing.T) {
	r := newRig(t)
	r.mod.detected = func(context.Context) []detect.Found {
		return []detect.Found{{Name: "ollama", Driver: "ollama", BaseURL: "http://127.0.0.1:11434", Version: "0.33.3"}}
	}
	var found []Found
	r.must("Detect", nil, &found)
	if len(found) != 1 || found[0] != (Found{Name: "ollama", Driver: "ollama", BaseURL: "http://127.0.0.1:11434", Version: "0.33.3"}) {
		t.Errorf("%+v", found)
	}
}

// Refusing to start over a settings file would take the whole app down; the
// file is kept aside for whoever wants what was in it.
func TestAnUnreadableFileIsSetAsideNotFatal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "llm.json")
	// Valid up to the bindings, so a decoder that stops there has already
	// filled in a provider.
	half := `{"providers":[{"id":"half","driver":"openai","values":{"base_url":"http://127.0.0.1:1"}}],"bindings":5}`
	os.WriteFile(path, []byte(half), 0o600)
	r := load(t, path)
	var st State
	r.must("State", nil, &st)
	aside, _ := filepath.Glob(path + ".corrupt-*")
	if len(st.Providers) != 0 || len(aside) != 1 {
		t.Errorf("state %+v, set aside %v", st, aside)
	}
	if b, _ := os.ReadFile(aside[0]); string(b) != half {
		t.Errorf("kept %q", b)
	}
}

func TestTheFileLivesInTheAppsDataDir(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("the data dir follows the home directory there")
	}
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_DATA_HOME", dir)
	sealer, _ := secrets.NewWithKey(testKey)
	c := &fakeCore{handlers: map[string]func(context.Context, json.RawMessage) (any, error){}}
	mod := New(Options{Slots: slots, Secrets: sealer})
	cfg := &config.Config{Modules: map[string]any{"llm": map[string]any{"enabled": true}}}
	cfg.App.Name = "demo"
	mgr := module.NewManager(cfg, c, nil)
	mgr.Register(mod)
	if err := mgr.LoadAll(); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.Unbind("chat"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "demo", "llm.json")); err != nil {
		t.Error(err)
	}
}

// A change the file refused must not stay live: the screen said it was not
// saved.
func TestAChangeTheFileRefusedDoesNotStayLive(t *testing.T) {
	srv, _ := openaiServer(t)
	r := newRig(t)
	r.must("SaveProvider", ProviderInput{ID: "work", Driver: "openai", Values: map[string]string{"base_url": srv.URL}}, nil)

	// A directory that cannot be made, because a file of that name is there.
	wall := filepath.Join(t.TempDir(), "wall")
	if err := os.WriteFile(wall, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	good := r.mod.path
	r.mod.path = filepath.Join(wall, "llm.json")

	if err := r.call("SaveProvider", ProviderInput{ID: "work", Driver: "openai",
		Values: map[string]string{"base_url": "https://elsewhere.example.com/v1"}}, nil); err == nil {
		t.Fatal("a save that could not be written reported success")
	}
	if err := r.call("Bind", BindInput{Slot: "chat", Provider: "work", Model: "m"}, nil); err == nil {
		t.Fatal("a bind that could not be written reported success")
	}
	if err := r.call("DeleteProvider", "work", nil); err == nil {
		t.Fatal("a delete that could not be written reported success")
	}
	var st State
	r.must("State", nil, &st)
	if len(st.Providers) != 1 || st.Providers[0].Values["base_url"] != srv.URL {
		t.Errorf("providers = %+v", st.Providers)
	}
	if st.Slots[0].Provider != "" || st.Slots[0].Model != "" {
		t.Errorf("slot = %+v", st.Slots[0])
	}
	// The registry has to say the same thing, or the next chat goes to the
	// provider the file no longer has.
	if b, ok := r.mod.Registry().Binding("chat"); ok {
		t.Errorf("the registry kept a binding the file refused: %+v", b)
	}
	if _, err := r.mod.Registry().Chat(context.Background(), "chat", core.ChatRequest{}, nil); !errors.Is(err, core.ErrNoBinding) {
		t.Errorf("chat after a refused bind: %v", err)
	}

	r.mod.path = good
	r.must("Bind", BindInput{Slot: "chat", Provider: "work", Model: "m"}, nil)
	b, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "elsewhere.example.com") {
		t.Errorf("a refused edit reached the file: %s", b)
	}
	again := load(t, good)
	var restarted State
	again.must("State", nil, &restarted)
	if len(restarted.Providers) != 1 || restarted.Providers[0].Values["base_url"] != srv.URL {
		t.Errorf("after a restart: %+v", restarted.Providers)
	}
}

// A model comes in layers, each counting from zero: without the layer the
// bar runs to the end and drops back.
func TestPullEventsSayWhichLayerTheyAreAbout(t *testing.T) {
	r := newRig(t)
	srv := ollamaServer(t, nil)
	r.must("SaveProvider", ProviderInput{ID: "ollama", Driver: "ollama", Values: map[string]string{"base_url": srv.URL}}, nil)
	var done bool
	r.must("Pull", ModelInput{Provider: "ollama", Model: "qwen3:0.6b"}, &done)
	seen := map[string]int64{}
	for _, e := range r.core.pulled() {
		if e.Digest != "" {
			seen[e.Digest] = max(seen[e.Digest], e.Total)
		}
	}
	if len(seen) != 2 || seen["sha256:abc"] != 400 || seen["sha256:def"] != 250 {
		t.Errorf("layers = %+v, events = %+v", seen, r.core.pulled())
	}
}

// ProviderConfig hands a driver its Secret reader, and validating the key
// while opening is the obvious thing to do with it - under mu.
type eagerDriver struct{}

type eagerProvider struct {
	id  string
	key string
}

func (p eagerProvider) ID() string                    { return p.id }
func (eagerProvider) Capabilities() []core.Capability { return []core.Capability{core.Chat} }
func (eagerDriver) Name() string                      { return "eager" }
func (eagerDriver) Fields() []core.Field {
	return []core.Field{{Key: "api_key", Label: "Key", Kind: core.FieldSecret, Required: true}}
}

func (eagerDriver) Open(cfg core.ProviderConfig) (core.Provider, error) {
	key, err := cfg.Secret(context.Background(), "api_key")
	if err != nil {
		return nil, err
	}
	return eagerProvider{id: cfg.ID, key: key}, nil
}

func init() { core.RegisterDriver(eagerDriver{}) }

func TestADriverMayReadItsKeyWhileOpening(t *testing.T) {
	r := newRig(t)
	within := func(what string, fn func() error) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- fn() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: %v", what, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never returned: opening a provider took the module's lock a second time", what)
		}
	}
	within("SaveProvider", func() error {
		return r.call("SaveProvider", ProviderInput{ID: "eager", Driver: "eager", Secrets: map[string]string{"api_key": "sk-eager"}}, nil)
	})
	p, ok := r.mod.Registry().Provider("eager")
	if !ok || p.(eagerProvider).key != "sk-eager" {
		t.Fatalf("provider %+v %v", p, ok)
	}
	var again *rig
	within("a restart", func() error {
		again = load(t, r.path)
		return nil
	})
	p, ok = again.mod.Registry().Provider("eager")
	if !ok || p.(eagerProvider).key != "sk-eager" {
		t.Errorf("after a restart: %+v %v", p, ok)
	}
}

// secrets takes a value that was never sealed, so a key hand-edited into
// llm.json works and reads the same on screen. Sealed on the next save.
func TestAKeyFoundInTheClearIsSealedOnTheNextSave(t *testing.T) {
	srv, auth := openaiServer(t)
	path := filepath.Join(t.TempDir(), "llm.json")
	file := `{"providers":[{"id":"work","driver":"openai","values":{"base_url":"` + srv.URL + `"},` +
		`"secrets":{"api_key":"sk-in-the-clear"}}],"bindings":{}}`
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	r := load(t, path)
	var models []ModelView
	r.must("Models", "work", &models)
	if *auth != "Bearer sk-in-the-clear" {
		t.Errorf("the key stopped working: %q", *auth)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "sk-in-the-clear") {
		t.Errorf("startup rewrote the file: %s", b)
	}

	// The user edits something else about the same provider.
	r.must("SaveProvider", ProviderInput{ID: "work", Driver: "openai",
		Values: map[string]string{"base_url": srv.URL, "tools": "true"}}, nil)
	b, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "sk-in-the-clear") {
		t.Errorf("the key is still in the clear on disk: %s", b)
	}
	r.must("Models", "work", &models)
	if *auth != "Bearer sk-in-the-clear" {
		t.Errorf("sealing changed the key: %q", *auth)
	}
}

// The app registers its own engines before the module loads, so Register's
// duplicate check runs against a file nobody has read yet.
func TestASavedProviderDoesNotTakeOverTheAppsOwnID(t *testing.T) {
	srv, _ := openaiServer(t)
	path := filepath.Join(t.TempDir(), "llm.json")
	file := `{"providers":[{"id":"llama","driver":"openai","values":{"base_url":"` + srv.URL + `"}}],` +
		`"bindings":{"chat":{"provider":"llama","model":"qwen"}}}`
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	r := load(t, path, func(m *Module) {
		if err := m.Register(appProvider{"llama"}); err != nil {
			t.Fatal(err)
		}
	})
	var st State
	r.must("State", nil, &st)
	var seen int
	for _, p := range st.Providers {
		if p.ID == "llama" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("llama is listed %d times: %+v", seen, st.Providers)
	}
	res, err := r.mod.Registry().Chat(context.Background(), "chat", core.ChatRequest{}, nil)
	if err != nil || res.Content != "from llama" {
		t.Errorf("the saved record took the app's place: %+v %v", res, err)
	}
}

// Undoing a refused save has to put the registry back too, and rebind can
// only add.
func TestARefusedSaveLeavesNoBindingBehind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "llm.json")
	// A provider that does not open, with a slot already pointing at it: the
	// registry starts with no binding at all.
	file := `{"providers":[{"id":"work","driver":"openai","values":{"base_url":"not a url"}}],` +
		`"bindings":{"chat":{"provider":"work","model":"m"}}}`
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	r := load(t, path)
	if _, ok := r.mod.Registry().Binding("chat"); ok {
		t.Fatal("a provider that does not open was bound anyway")
	}

	wall := filepath.Join(t.TempDir(), "wall")
	if err := os.WriteFile(wall, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.mod.path = filepath.Join(wall, "llm.json")
	srv, _ := openaiServer(t)
	if err := r.call("SaveProvider", ProviderInput{ID: "work", Driver: "openai",
		Values: map[string]string{"base_url": srv.URL}}, nil); err == nil {
		t.Fatal("a save that could not be written reported success")
	}
	if b, ok := r.mod.Registry().Binding("chat"); ok {
		t.Errorf("the registry kept a binding the file refused: %+v", b)
	}
	if _, err := r.mod.Registry().Chat(context.Background(), "chat", core.ChatRequest{}, nil); !errors.Is(err, core.ErrNoBinding) {
		t.Errorf("chat after a refused save: %v", err)
	}
}

// The app stops the engine it was running, so the record saved under that
// name becomes the answer.
func TestTheUsersRecordTakesOverWhenTheAppLetsTheIDGo(t *testing.T) {
	srv, _ := openaiServer(t)
	path := filepath.Join(t.TempDir(), "llm.json")
	file := `{"providers":[{"id":"llama","driver":"openai","values":{"base_url":"` + srv.URL + `"}}],` +
		`"bindings":{"chat":{"provider":"llama","model":"m"}}}`
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	r := load(t, path, func(m *Module) {
		if err := m.Register(appProvider{"llama"}); err != nil {
			t.Fatal(err)
		}
	})
	res, err := r.mod.Registry().Chat(context.Background(), "chat", core.ChatRequest{}, nil)
	if err != nil || res.Content != "from llama" {
		t.Fatalf("while the app owns the name: %+v %v", res, err)
	}

	r.mod.Unregister("llama")
	var st State
	r.must("State", nil, &st)
	if len(st.Providers) != 1 || !st.Providers[0].Editable || st.Providers[0].Error != "" {
		t.Errorf("after the app let it go: %+v", st.Providers)
	}
	var models []ModelView
	if err := r.call("Models", "llama", &models); err != nil {
		t.Errorf("the saved record never opened: %v", err)
	}
	if st.Slots[0].Error != "" {
		t.Errorf("slot = %+v", st.Slots[0])
	}
}

// A file from a newer build is read but never written: saving over it would
// drop the providers and keys it added.
func TestAFileFromANewerVersionIsLeftAlone(t *testing.T) {
	srv, _ := openaiServer(t)
	path := filepath.Join(t.TempDir(), "llm.json")
	file := `{"version":99,"providers":[{"id":"work","driver":"openai","values":{"base_url":"` + srv.URL + `"}}],"bindings":{}}`
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	r := load(t, path)

	// Reading still works, so the app is usable.
	var st State
	r.must("State", nil, &st)
	if len(st.Providers) != 1 || st.Providers[0].ID != "work" {
		t.Errorf("providers = %+v", st.Providers)
	}
	// And the screen is told, once, instead of finding out one refusal per
	// click on a form where everything still looks editable.
	if !st.ReadOnly {
		t.Error("State did not say the settings are read-only")
	}
	var models []ModelView
	r.must("Models", "work", &models)

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"Bind", func() error { return r.call("Bind", BindInput{Slot: "chat", Provider: "work", Model: "m"}, nil) }},
		{"DeleteProvider", func() error { return r.call("DeleteProvider", "work", nil) }},
	} {
		if err := tc.call(); err == nil || !strings.Contains(err.Error(), "newer version") {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != file {
		t.Errorf("the file changed:\n%s", after)
	}
}

// Written before the field existed: the same shape, so it is read as is and
// gets the number the next time it is saved.
func TestAFileWithNoVersionIsTodaysShape(t *testing.T) {
	srv, _ := openaiServer(t)
	path := filepath.Join(t.TempDir(), "llm.json")
	file := `{"providers":[{"id":"work","driver":"openai","values":{"base_url":"` + srv.URL + `"}}],"bindings":{}}`
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	r := load(t, path)
	r.must("Bind", BindInput{Slot: "chat", Provider: "work", Model: "m"}, nil)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"version": 1`) {
		t.Errorf("no version written: %s", b)
	}
}

// Models are gigabytes each: ten at once is ten part files growing on one
// link and a disk that fills with none of them finished.
func TestOnlySoManyDownloadsRunAtOnce(t *testing.T) {
	hold := make(chan struct{})
	r := newRig(t)
	srv := ollamaServer(t, hold)
	r.must("SaveProvider", ProviderInput{ID: "ollama", Driver: "ollama", Values: map[string]string{"base_url": srv.URL}}, nil)

	started := make(chan error, maxPulls)
	for i := range maxPulls {
		go func() {
			started <- r.call("Pull", ModelInput{Provider: "ollama", Model: fmt.Sprintf("m%d", i)}, nil)
		}()
	}
	// Both are inside the server's hold by the time the cap is tested.
	waitFor(t, func() bool { return r.mod.running() == maxPulls })

	err := r.call("Pull", ModelInput{Provider: "ollama", Model: "one-too-many"}, nil)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("the cap let one more through: %v", err)
	}
	close(hold)
	for range maxPulls {
		if err := <-started; err != nil {
			t.Errorf("a download inside the cap failed: %v", err)
		}
	}
	// With room again, the next one is taken.
	if err := r.call("Pull", ModelInput{Provider: "ollama", Model: "later"}, nil); err != nil {
		t.Errorf("after the others finished: %v", err)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A save killed between the temp file and the rename leaves a full copy of
// the sealed keys behind.
func TestAbandonedTempFilesAreSwept(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sub      string
		settings bool
	}{
		{"with settings already saved", "data", true},
		// The most likely save to be interrupted is the first: the user has
		// just pasted an API key and llm.json has never existed.
		{"before the first save ever finished", "data", false},
		// Glob calls this a syntax error and matched nothing, so the sealed
		// keys stayed on disk with nothing to see.
		{"in a directory whose name is a glob pattern", "pro[be", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), tc.sub)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "llm.json")
			if tc.settings {
				if err := os.WriteFile(path, []byte(`{"version":1,"providers":[],"bindings":{}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// Named the way save() names it, rather than by hand: a sweep and a
			// save that spell the temporary file differently leak every one.
			old, fresh := abandonedTemp(t, path), abandonedTemp(t, path)
			long := time.Now().Add(-2 * time.Hour)
			if err := os.Chtimes(old, long, long); err != nil {
				t.Fatal(err)
			}

			load(t, path)

			if _, err := os.Stat(old); !os.IsNotExist(err) {
				t.Errorf("the abandoned temp file is still there: %v", err)
			}
			if _, err := os.Stat(fresh); err != nil {
				t.Errorf("a save in flight was taken out from under it: %v", err)
			}
		})
	}
}

func abandonedTemp(t *testing.T, path string) string {
	t.Helper()
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*"+tempSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("sk-sealed"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

// An app that only lets its pages USE models must be able to say so without
// handing over the disk.
func TestEachHandlerAsksForTheRightPermission(t *testing.T) {
	r := newRig(t)
	want := map[string]module.Permission{
		"Drivers": PermUse, "State": PermUse, "Models": PermUse, "Describe": PermUse,
		"SaveProvider": PermProviders, "DeleteProvider": PermProviders, "Bind": PermProviders, "Unbind": PermProviders,
		// Knocking on the machine's own ports to find a runtime is how a
		// provider gets added, not part of using one.
		"Detect": PermProviders,
		"Pull":   PermModels, "CancelPull": PermModels, "DeleteModel": PermModels,
	}
	if len(r.core.needs) != len(want) {
		t.Errorf("%d handlers registered, want %d", len(r.core.needs), len(want))
	}
	for name, perm := range want {
		if got := r.core.needs["mod:llm:"+name]; got != perm {
			t.Errorf("%s needs %q, want %q", name, got, perm)
		}
	}
	// And every one of those is a permission the module declares, or the app
	// would grant a set and the handler would stay shut.
	set := r.mod.Permissions()
	for topic, perm := range r.core.needs {
		if set.Expand(perm) == nil {
			t.Errorf("%s needs %q, which the module does not declare", topic, perm)
		}
	}
}

// Pull writes gigabytes under a name the page chose, which a boolean
// permission cannot bound.
func TestPullStaysInsideWhatTheManifestAllows(t *testing.T) {
	for _, tc := range []struct {
		name    string
		allow   []any
		model   string
		allowed bool
	}{
		{"exactly what was named", []any{"ollama/qwen3:8b"}, "qwen3:8b", true},
		{"a provider, any model", []any{"ollama/*"}, "anything:70b", true},
		{"a repo prefix", []any{"ollama/hf.co/bartowski/*"}, "hf.co/bartowski/Qwen_Qwen3-8B-GGUF", true},
		{"outside the prefix", []any{"ollama/hf.co/bartowski/*"}, "hf.co/someone-else/Sneaky-GGUF", false},
		{"another provider", []any{"other/*"}, "qwen3:8b", false},
		{"everything, said out loud", []any{"*"}, "whatever:405b", true},
		// A pattern with something after the star: the suffix has to be tested,
		// or the glob says yes to whatever it merely begins with.
		{"a suffix that matches", []any{"ollama/*-GGUF"}, "hf.co/bartowski/Qwen-GGUF", true},
		{"a suffix that does not", []any{"ollama/*-GGUF"}, "hf.co/bartowski/Qwen-safetensors", false},
		// The default. An app that never said anything never asked for a
		// download either, and a page must not be the one to decide.
		{"nothing said at all", nil, "qwen3:8b", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := loadWithPullAllow(t, tc.allow)
			srv := ollamaServer(t, nil)
			r.must("SaveProvider", ProviderInput{ID: "ollama", Driver: "ollama", Values: map[string]string{"base_url": srv.URL}}, nil)
			var started bool
			err := r.call("Pull", ModelInput{Provider: "ollama", Model: tc.model}, &started)
			if tc.allowed {
				if err != nil {
					t.Fatalf("refused a model the manifest allows: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("pulled a model the manifest does not allow")
			}
			if !strings.Contains(err.Error(), "pull_allow") {
				t.Errorf("err = %v, want it to name the setting", err)
			}
		})
	}
}

func loadWithPullAllow(t *testing.T, allow []any) *rig {
	t.Helper()
	sealer, err := secrets.NewWithKey(testKey)
	if err != nil {
		t.Fatal(err)
	}
	c := &fakeCore{handlers: map[string]func(context.Context, json.RawMessage) (any, error){}}
	mod := New(Options{Slots: slots, Secrets: sealer, Path: filepath.Join(t.TempDir(), "llm.json"),
		Confirm: func(context.Context, string) (bool, error) { return true, nil }})
	section := map[string]any{"enabled": true}
	if allow != nil {
		section["pull_allow"] = allow
	}
	mgr := module.NewManager(&config.Config{Modules: map[string]any{"llm": section}}, c, nil)
	mgr.Register(mod)
	if err := mgr.LoadAll(); err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, mod: mod, core: c}
}

// Neither is something a page decides on its own, and a dialog that page
// draws proves nothing - so without a native one the answer is no.
func TestWhatIsDestroyedIsConfirmedByAPerson(t *testing.T) {
	var asked []string
	for _, tc := range []struct {
		name    string
		confirm func(context.Context, string) (bool, error)
		done    bool
		says    string
	}{
		{"the person says yes", func(_ context.Context, q string) (bool, error) { asked = append(asked, q); return true, nil }, true, ""},
		{"the person says no", func(context.Context, string) (bool, error) { return false, nil }, false, "cancelled"},
		{"nobody can be asked", nil, false, "Options.Confirm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sealer, err := secrets.NewWithKey(testKey)
			if err != nil {
				t.Fatal(err)
			}
			c := &fakeCore{handlers: map[string]func(context.Context, json.RawMessage) (any, error){}}
			mod := New(Options{Slots: slots, Secrets: sealer, Path: filepath.Join(t.TempDir(), "llm.json"), Confirm: tc.confirm})
			mgr := module.NewManager(&config.Config{Modules: map[string]any{"llm": map[string]any{"enabled": true}}}, c, nil)
			mgr.Register(mod)
			if err := mgr.LoadAll(); err != nil {
				t.Fatal(err)
			}
			r := &rig{t: t, mod: mod, core: c}
			srv, _ := openaiServer(t)
			r.must("SaveProvider", ProviderInput{ID: "work", Driver: "openai", Values: map[string]string{"base_url": srv.URL}}, nil)

			err = r.call("DeleteProvider", "work", nil)
			if tc.done {
				if err != nil {
					t.Fatalf("a confirmed delete failed: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("deleted without a yes")
				}
				if !strings.Contains(err.Error(), tc.says) {
					t.Errorf("err = %v, want it to mention %q", err, tc.says)
				}
				// A frontend decides from the code, not the wording: saying no is not a
				// failure.
				if tc.says == "cancelled" {
					var fe *fault.Error
					if !errors.As(err, &fe) || fe.Code != fault.CodeCanceled {
						t.Errorf("err = %#v, want fault code %q", err, fault.CodeCanceled)
					}
				}
			}
			// And the provider is still there when the answer was no.
			var st State
			r.must("State", nil, &st)
			if got := len(st.Providers) > 0; got != !tc.done {
				t.Errorf("providers left = %v", st.Providers)
			}
			// The question names what goes: "are you sure?" and nothing else is
			// a prompt people click through.
			if tc.done && (len(asked) != 1 || !strings.Contains(asked[0], `"work"`) || !strings.Contains(asked[0], "key")) {
				t.Errorf("asked %q", asked)
			}
		})
	}
}
