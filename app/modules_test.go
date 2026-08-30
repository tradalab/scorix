package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
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
		if r.State != "error" || !strings.Contains(r.Error, `capability "x" denied`) {
			t.Fatalf("state=%q err=%q, want capability denied", r.State, r.Error)
		}
		if r.ErrorCode != fault.CodeDenied {
			t.Fatalf("denied code = %q, want %q", r.ErrorCode, fault.CodeDenied)
		}
	})
	t.Run("absent key denies (fail closed)", func(t *testing.T) {
		a := newGatedApp(t, &config.SandboxConfig{Allowlist: config.Allowlist{}})
		r := invokeGatedPing(t, a)
		if r.State != "error" || !strings.Contains(r.Error, `capability "x" denied`) {
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
