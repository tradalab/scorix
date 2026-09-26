package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/tradalab/scorix/config"
	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/module"
	"github.com/tradalab/scorix/webview"
)

type gatedModule struct{ capability string }

func (m *gatedModule) Name() string       { return "gated" }
func (m *gatedModule) Version() string    { return "0.0.1" }
func (m *gatedModule) Capability() string { return m.capability }
func (m *gatedModule) OnLoad(ctx *module.Context) error {
	module.Expose(m, "Ping", ctx.IPC)
	return nil
}
func (m *gatedModule) OnStart() error  { return nil }
func (m *gatedModule) OnStop() error   { return nil }
func (m *gatedModule) OnUnload() error { return nil }
func (m *gatedModule) Ping(_ context.Context) (string, error) {
	return "pong", nil
}

func invokeGatedPing(t *testing.T, a *App) webview.Message {
	t.Helper()
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()
	defer a.stopModules()

	c := dialWS(t, ts)
	defer c.Close()
	wsSend(t, c, webview.Message{ID: "1", Kind: "command", Name: "mod:gated:Ping", State: "start"})
	return wsRecv(t, c)
}

func newGatedApp(t *testing.T, sec *config.SandboxConfig) *App {
	t.Helper()
	a, err := New(Options{URL: "scorix://app/index.html", Security: sec})
	if err != nil {
		t.Fatal(err)
	}
	a.Serve("scorix", fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})
	a.Module(&gatedModule{capability: "x"})
	return a
}

func TestCapabilityGate(t *testing.T) {
	t.Run("allowed", func(t *testing.T) {
		a := newGatedApp(t, &config.SandboxConfig{Allowlist: config.Allowlist{"x": true}})
		r := invokeGatedPing(t, a)
		if r.State != "done" {
			t.Fatalf("state=%q err=%q, want done", r.State, r.Error)
		}
		var got string
		_ = json.Unmarshal(r.Data, &got)
		if got != "pong" {
			t.Fatalf("payload=%q", got)
		}
	})
	t.Run("denied", func(t *testing.T) {
		a := newGatedApp(t, &config.SandboxConfig{Allowlist: config.Allowlist{"x": false}})
		r := invokeGatedPing(t, a)
		if r.State != "error" || !strings.Contains(r.Error, `permission "x" denied`) {
			t.Fatalf("state=%q err=%q, want capability denied", r.State, r.Error)
		}
		if r.ErrorCode != fault.CodeDenied {
			t.Fatalf("denied code = %q, want %q", r.ErrorCode, fault.CodeDenied)
		}
	})
	t.Run("absent key denies (fail closed)", func(t *testing.T) {
		a := newGatedApp(t, &config.SandboxConfig{Allowlist: config.Allowlist{}})
		r := invokeGatedPing(t, a)
		if r.State != "error" || !strings.Contains(r.Error, `permission "x" denied`) {
			t.Fatalf("state=%q err=%q, want capability denied", r.State, r.Error)
		}
	})
	t.Run("nil Security allows (back-compat)", func(t *testing.T) {
		a := newGatedApp(t, nil)
		r := invokeGatedPing(t, a)
		if r.State != "done" {
			t.Fatalf("state=%q err=%q, want done", r.State, r.Error)
		}
	})
}

func TestStructuredErrorOnWire(t *testing.T) {
	a := newTestApp(t)
	a.Command("boom-coded", func(context.Context, json.RawMessage, ipc.Stream) (any, error) {
		return nil, fault.Errorf("quota_exceeded", "over the limit").With("limit", 5)
	})
	a.Command("boom-plain", func(context.Context, json.RawMessage, ipc.Stream) (any, error) {
		return nil, errors.New("plain failure")
	})
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()
	c := dialWS(t, ts)
	defer c.Close()

	wsSend(t, c, webview.Message{ID: "1", Kind: "command", Name: "boom-coded", State: "start"})
	r := wsRecv(t, c)
	if r.State != "error" || r.Error != "over the limit" || r.ErrorCode != "quota_exceeded" {
		t.Fatalf("coded: state=%q error=%q code=%q", r.State, r.Error, r.ErrorCode)
	}
	var details map[string]any
	if err := json.Unmarshal(r.ErrorData, &details); err != nil || details["limit"] != float64(5) {
		t.Fatalf("coded details = %s (err %v)", r.ErrorData, err)
	}

	wsSend(t, c, webview.Message{ID: "2", Kind: "command", Name: "boom-plain", State: "start"})
	r = wsRecv(t, c)
	if r.State != "error" || r.Error != "plain failure" || r.ErrorCode != "" || r.ErrorData != nil {
		t.Fatalf("plain: state=%q error=%q code=%q data=%s", r.State, r.Error, r.ErrorCode, r.ErrorData)
	}

	wsSend(t, c, webview.Message{ID: "3", Kind: "command", Name: "nope", State: "start"})
	r = wsRecv(t, c)
	if r.ErrorCode != fault.CodeNotFound {
		t.Fatalf("no-handler code = %q, want %q", r.ErrorCode, fault.CodeNotFound)
	}
}

// Reading what is configured and spending the disk are not one grant.
type splitModule struct{}

func (m *splitModule) Name() string       { return "demo" }
func (m *splitModule) Version() string    { return "0.0.1" }
func (m *splitModule) Capability() string { return "demo" }
func (m *splitModule) Permissions() module.PermissionSet {
	return module.PermissionSet{
		Atoms: []module.Permission{"demo:read", "demo:write"},
		Sets: map[module.Permission][]module.Permission{
			"demo":          {"demo:read", "demo:write"},
			"demo:readonly": {"demo:read"},
		},
	}
}
func (m *splitModule) OnLoad(ctx *module.Context) error {
	module.Expose(m, "Read", ctx.IPC, module.Needs("demo:read"))
	module.Expose(m, "Write", ctx.IPC, module.Needs("demo:write"))
	return nil
}
func (m *splitModule) OnStart() error                          { return nil }
func (m *splitModule) OnStop() error                           { return nil }
func (m *splitModule) OnUnload() error                         { return nil }
func (m *splitModule) Read(_ context.Context) (string, error)  { return "read", nil }
func (m *splitModule) Write(_ context.Context) (string, error) { return "write", nil }

func invokeDemo(t *testing.T, a *App, method string) webview.Message {
	t.Helper()
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()
	c := dialWS(t, ts)
	defer c.Close()
	wsSend(t, c, webview.Message{ID: "1", Kind: "command", Name: "mod:demo:" + method, State: "start"})
	return wsRecv(t, c)
}

// The whole resolution table, through the socket the frontend uses.
func TestPermissionGate(t *testing.T) {
	for _, tc := range []struct {
		name        string
		allow       config.Allowlist
		read, write bool
	}{
		// The name every manifest written before permissions existed uses. It
		// has to keep meaning the whole module, or this change breaks them all.
		{"the old whole-module grant", config.Allowlist{"demo": true}, true, true},
		{"a set that is only part of it", config.Allowlist{"demo:readonly": true}, true, false},
		// An exact entry decides, so a set can be granted and one atom taken
		// back out of it.
		{"the set, minus one atom", config.Allowlist{"demo": true, "demo:write": false}, true, false},
		// And an atom on its own grants itself and nothing more.
		{"one atom alone", config.Allowlist{"demo:write": true}, false, true},
		{"nothing written", config.Allowlist{}, false, false},
		// A granted name no module declares grants nothing - it cannot expand.
		{"a name nobody declares", config.Allowlist{"demo:admin": true}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for method, want := range map[string]bool{"Read": tc.read, "Write": tc.write} {
				a, err := New(Options{URL: "scorix://app/index.html", Security: &config.SandboxConfig{Allowlist: tc.allow}})
				if err != nil {
					t.Fatal(err)
				}
				a.Serve("scorix", fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})
				a.Module(&splitModule{})
				r := invokeDemo(t, a, method)
				a.stopModules()
				if got := r.State == "done"; got != want {
					t.Errorf("%s: state=%q err=%q, want allowed=%v", method, r.State, r.Error, want)
				}
				if r.State == "error" && r.ErrorCode != fault.CodeDenied {
					t.Errorf("%s: code=%q", method, r.ErrorCode)
				}
			}
		})
	}
}

// A typo nothing downstream catches: the app grants the set it knows about
// and this handler stays denied.
func TestExposeRefusesAPermissionTheModuleDoesNotDeclare(t *testing.T) {
	a, err := New(Options{URL: "scorix://app/index.html"})
	if err != nil {
		t.Fatal(err)
	}
	a.Module(&typoModule{})
	// Expose panics; safeOnLoad turns a module panic into a load error, which
	// is how an app finds out without the process dying.
	err = a.mods.LoadAll()
	if err == nil || !strings.Contains(err.Error(), "does not declare") {
		t.Errorf("LoadAll = %v, want a refusal naming the undeclared permission", err)
	}
}

type typoModule struct{ splitModule }

func (m *typoModule) OnLoad(ctx *module.Context) error {
	module.Expose(m, "Read", ctx.IPC, module.Needs("demo:raed"))
	return nil
}

// The same mistake one level up: a set naming an atom that does not exist.
func TestASetThatNamesAnUndeclaredAtomIsRefused(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("a set naming an undeclared atom was accepted")
		}
	}()
	a, err := New(Options{URL: "scorix://app/index.html"})
	if err != nil {
		t.Fatal(err)
	}
	a.Module(&badSetModule{})
}

type badSetModule struct{ splitModule }

func (m *badSetModule) Permissions() module.PermissionSet {
	return module.PermissionSet{
		Atoms: []module.Permission{"demo:read"},
		Sets:  map[module.Permission][]module.Permission{"demo": {"demo:read", "demo:write"}},
	}
}

// ipc.Registry.Command locks, so a module may register a handler from any
// goroutine at any time; -race is the assertion.
func TestRegisteringAHandlerWhileRequestsAreGated(t *testing.T) {
	a, err := New(Options{URL: "scorix://app/index.html", Security: &config.SandboxConfig{Allowlist: config.Allowlist{"demo": true}}})
	if err != nil {
		t.Fatal(err)
	}
	a.Module(&splitModule{})
	core := &moduleCore{reg: a.reg, app: a}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			core.Register(fmt.Sprintf("mod:demo:Late%d", i), "demo:read", func(context.Context, json.RawMessage) (any, error) {
				return "ok", nil
			})
		}()
		go func() {
			defer wg.Done()
			if !a.permitted("demo:read") {
				t.Error("a granted permission came back denied")
			}
		}()
	}
	wg.Wait()
	a.auditAllowlist()
}
