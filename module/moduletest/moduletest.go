// Package moduletest loads a module the way an app does, through module.Manager and JSON,
// so a test exercises its config decoding and the permission each handler declares.
package moduletest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"testing"

	"github.com/tradalab/scorix/config"
	"github.com/tradalab/scorix/module"
)

const AppName = "scorix-moduletest"

type handler struct {
	needs module.Permission
	exec  func(context.Context, json.RawMessage) (any, error)
}

type core struct{ handlers map[string]handler }

func (c *core) Register(name string, needs module.Permission, exec func(context.Context, json.RawMessage) (any, error)) {
	c.handlers[name] = handler{needs: needs, exec: exec}
}

func (c *core) Invoke(ctx context.Context, name string, data json.RawMessage) (json.RawMessage, error) {
	h, ok := c.handlers[name]
	if !ok {
		return nil, fmt.Errorf("moduletest: no handler %s", name)
	}
	out, err := h.exec(ctx, data)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func (c *core) Emit(context.Context, string, json.RawMessage) error { return nil }

// Rig is one loaded module. Its HOME, XDG_DATA_HOME and APPDATA point into the test's temp dir.
type Rig struct {
	t    testing.TB
	mod  module.Module
	core *core
}

// Load fails the test when LoadAll does; cfg is the module's section of scorix.yaml.
// It sets HOME and friends with t.Setenv, so the test must not call t.Parallel.
func Load(t testing.TB, m module.Module, cfg map[string]any) *Rig {
	t.Helper()
	r, err := TryLoad(t, m, cfg)
	if err != nil {
		t.Fatalf("moduletest: load %s: %v", m.Name(), err)
	}
	return r
}

// TryLoad returns the LoadAll error instead of failing, for tests about refused configs.
func TryLoad(t testing.TB, m module.Module, cfg map[string]any) (*Rig, error) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("APPDATA", filepath.Join(home, "data"))
	section := map[string]any{"enabled": true}
	maps.Copy(section, cfg)
	c := &core{handlers: map[string]handler{}}
	appCfg := &config.Config{Modules: map[string]any{m.Name(): section}}
	appCfg.App.Name = AppName
	mgr := module.NewManager(appCfg, c, nil)
	mgr.Register(m)
	return &Rig{t: t, mod: m, core: c}, mgr.LoadAll()
}

func (r *Rig) topic(method string) string { return "mod:" + r.mod.Name() + ":" + method }

// Call sends in as the page would, marshalled to JSON, and returns the marshalled result.
func (r *Rig) Call(method string, in any) (json.RawMessage, error) {
	r.t.Helper()
	h, ok := r.core.handlers[r.topic(method)]
	if !ok {
		r.t.Fatalf("moduletest: %s registers no %s", r.mod.Name(), method)
	}
	var raw json.RawMessage
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			r.t.Fatal(err)
		}
		raw = b
	}
	out, err := h.exec(context.Background(), raw)
	if err != nil {
		return nil, err
	}
	b, merr := json.Marshal(out)
	if merr != nil {
		return nil, errors.Join(errors.New("moduletest: result does not marshal"), merr)
	}
	return b, nil
}

// Needs is the permission the handler declared when it was exposed.
func (r *Rig) Needs(method string) module.Permission {
	r.t.Helper()
	h, ok := r.core.handlers[r.topic(method)]
	if !ok {
		r.t.Fatalf("moduletest: %s registers no %s", r.mod.Name(), method)
	}
	return h.needs
}

// DataDir is the directory $DATA expands to for this load.
func (r *Rig) DataDir() string { return module.DataDir(AppName) }
