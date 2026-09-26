package llm

import (
	"context"
	"strings"
	"testing"
)

type recordingDriver struct {
	name   string
	fields []Field
	got    ProviderConfig
}

func (d *recordingDriver) Name() string    { return d.name }
func (d *recordingDriver) Fields() []Field { return d.fields }
func (d *recordingDriver) Open(cfg ProviderConfig) (Provider, error) {
	d.got = cfg
	return &fakeProvider{id: cfg.ID}, nil
}

func TestOpenFillsDefaultsAndRefusesAMissingRequiredField(t *testing.T) {
	d := &recordingDriver{name: "test-open", fields: []Field{
		{Key: "base_url", Kind: FieldURL, Required: true},
		{Key: "mode", Kind: FieldText, Default: "fast"},
		{Key: "api_key", Kind: FieldSecret, Required: true},
	}}
	RegisterDriver(d)

	if _, err := Open("test-open", ProviderConfig{ID: "x"}); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Errorf("a missing required field = %v", err)
	}

	stored := map[string]string{"base_url": "http://127.0.0.1:1/v1"}
	p, err := Open("test-open", ProviderConfig{ID: "x", Values: stored})
	if err != nil {
		t.Fatalf("a secret cannot be checked without reading it, so it must not block Open: %v", err)
	}
	if p.ID() != "x" || d.got.Values["mode"] != "fast" {
		t.Errorf("provider %q, values %v", p.ID(), d.got.Values)
	}
	if _, touched := stored["mode"]; touched {
		t.Error("filling defaults wrote into the caller's stored settings")
	}
}

func TestOpenNamesADriverThatIsNotThere(t *testing.T) {
	if _, err := Open("no-such-driver", ProviderConfig{ID: "x"}); err == nil || !strings.Contains(err.Error(), "no-such-driver") {
		t.Errorf("err = %v", err)
	}
}

func TestADriverRegisteredTwicePanics(t *testing.T) {
	RegisterDriver(&recordingDriver{name: "test-dup"})
	defer func() {
		if recover() == nil {
			t.Error("a second driver under the same name was accepted")
		}
	}()
	RegisterDriver(&recordingDriver{name: "test-dup"})
}

func TestDriversAreListedInAStableOrder(t *testing.T) {
	RegisterDriver(&recordingDriver{name: "test-order-b"})
	RegisterDriver(&recordingDriver{name: "test-order-a"})
	var names []string
	for _, d := range Drivers() {
		if strings.HasPrefix(d.Name(), "test-order-") {
			names = append(names, d.Name())
		}
	}
	if strings.Join(names, ",") != "test-order-a,test-order-b" {
		t.Errorf("order = %v", names)
	}
}

// Secret reads go through the config's function, never a plain value, so a
// sealed key can change without the provider being rebuilt.
func TestSecretsReachTheDriverAsAFunction(t *testing.T) {
	d := &recordingDriver{name: "test-secret", fields: []Field{{Key: "api_key", Kind: FieldSecret}}}
	RegisterDriver(d)
	key := "v1"
	_, err := Open("test-secret", ProviderConfig{ID: "x", Secret: func(context.Context, string) (string, error) { return key, nil }})
	if err != nil {
		t.Fatal(err)
	}
	key = "v2"
	got, _ := d.got.Secret(context.Background(), "api_key")
	if got != "v2" {
		t.Errorf("secret read %q after it changed to v2", got)
	}
	if _, leaked := d.got.Values["api_key"]; leaked {
		t.Error("the secret arrived as a plain value")
	}
}
