package llm

import (
	"context"
	"errors"
	"testing"
)

func TestALocalOnlySlotRefusesARemoteProviderAtBind(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, &localProvider{fakeProvider: fakeProvider{id: "cloud", caps: []Capability{Chat}}, local: false})
	mustRegister(t, r, &localProvider{fakeProvider: fakeProvider{id: "here", caps: []Capability{Chat}}, local: true})

	if err := r.Bind("vault", Binding{Provider: "cloud", LocalOnly: true}); !errors.Is(err, ErrNotLocal) {
		t.Errorf("a local-only slot bound to a remote provider: %v", err)
	}
	if err := r.Bind("vault", Binding{Provider: "here", LocalOnly: true}); err != nil {
		t.Errorf("a local-only slot refused a local provider: %v", err)
	}
	if err := r.Bind("chat", Binding{Provider: "cloud"}); err != nil {
		t.Errorf("an ordinary slot refused a remote provider: %v", err)
	}
}

// A provider that never said where it runs is the one a local-only slot cannot
// trust: counting it as local would let data leave on a guess.
func TestAProviderThatDoesNotSayWhereItRunsIsNotLocal(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, &fakeProvider{id: "silent", caps: []Capability{Chat}})
	if err := r.Bind("vault", Binding{Provider: "silent", LocalOnly: true}); !errors.Is(err, ErrNotLocal) {
		t.Errorf("a provider with no Local() passed as local: %v", err)
	}
}

// Bind checked the provider that was there then. The ID can be unregistered and
// registered again as something remote, and the slot must notice at call time.
func TestALocalOnlySlotRechecksAtCallTime(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, &localProvider{fakeProvider: fakeProvider{id: "p", caps: []Capability{Chat}}, local: true})
	if err := r.Bind("vault", Binding{Provider: "p", LocalOnly: true}); err != nil {
		t.Fatal(err)
	}
	r.Unregister("p")
	remote := &localProvider{fakeProvider: fakeProvider{id: "p", caps: []Capability{Chat}}, local: false}
	mustRegister(t, r, remote)

	if _, err := r.Chat(context.Background(), "vault", ChatRequest{}, nil); !errors.Is(err, ErrNotLocal) {
		t.Errorf("a local-only slot sent to a provider that turned remote: %v", err)
	}
	if remote.calls != 0 {
		t.Error("the remote provider was called")
	}
}
