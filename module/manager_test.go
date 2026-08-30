package module

import (
	"strings"
	"testing"

	"github.com/tradalab/scorix/config"
)

type plainModule struct{}

func (plainModule) Name() string          { return "plain" }
func (plainModule) Version() string       { return "0.0.1" }
func (plainModule) OnLoad(*Context) error { return nil }
func (plainModule) OnStart() error        { return nil }
func (plainModule) OnStop() error         { return nil }
func (plainModule) OnUnload() error       { return nil }

type capableModule struct{ plainModule }

func (capableModule) Name() string       { return "capable" }
func (capableModule) Capability() string { return "x" }

func managerCfg(strict bool) *config.Config {
	cfg := &config.Config{Modules: map[string]any{
		"plain":   map[string]any{"enabled": true},
		"capable": map[string]any{"enabled": true},
	}}
	cfg.Security.StrictModules = strict
	return cfg
}

func TestLoadUndeclaredModuleLenient(t *testing.T) {
	m := NewManager(managerCfg(false), nil, nil)
	m.Register(plainModule{})
	m.Register(capableModule{})
	if err := m.LoadAll(); err != nil {
		t.Fatalf("lenient LoadAll: %v", err)
	}
	if m.State("plain") != StateLoaded || m.State("capable") != StateLoaded {
		t.Fatalf("states: plain=%v capable=%v", m.State("plain"), m.State("capable"))
	}
}

func TestLoadUndeclaredModuleStrict(t *testing.T) {
	m := NewManager(managerCfg(true), nil, nil)
	m.Register(plainModule{})
	err := m.LoadAll()
	if err == nil || !strings.Contains(err.Error(), "strict_modules") {
		t.Fatalf("strict LoadAll err = %v, want strict_modules refusal", err)
	}

	m2 := NewManager(managerCfg(true), nil, nil)
	m2.Register(capableModule{})
	if err := m2.LoadAll(); err != nil {
		t.Fatalf("strict LoadAll with Capable module: %v", err)
	}
}
