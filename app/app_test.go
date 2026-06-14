package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/module/store"
	"github.com/tradalab/scorix/webview"
)

func dialWS(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/ipc", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	return c
}

func wsSend(t *testing.T, c *websocket.Conn, m webview.Message) {
	t.Helper()
	raw, _ := json.Marshal(m)
	if err := c.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatal(err)
	}
}

func wsRecv(t *testing.T, c *websocket.Conn) webview.Message {
	t.Helper()
	_, raw, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var m webview.Message
	_ = json.Unmarshal(raw, &m)
	return m
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	a, err := New(Options{URL: "scorix://app/index.html"})
	if err != nil {
		t.Fatal(err)
	}
	a.Serve("scorix", fstest.MapFS{
		"index.html": {Data: []byte("<html><head><title>x</title></head><body>hi</body></html>")},
		"app.js":     {Data: []byte("console.log(1)")},
	})
	a.Command("echo", func(_ context.Context, data json.RawMessage, _ ipc.Stream) (any, error) {
		var s string
		_ = json.Unmarshal(data, &s)
		return "echo:" + s, nil
	})
	a.Command("count", func(_ context.Context, _ json.RawMessage, s ipc.Stream) (any, error) {
		for i := 1; i <= 3; i++ {
			_ = s.Chunk(i)
		}
		return "done", nil
	})
	return a
}

func TestWebAssetsInjectBridge(t *testing.T) {
	ts := httptest.NewServer(newTestApp(t).Handler())
	defer ts.Close()

	res, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body := readAll(t, res.Body)
	if !strings.Contains(body, "hi") {
		t.Fatal("served HTML missing page body")
	}
	if !strings.Contains(body, "window.scorix") {
		t.Fatal("bridge JS not injected into HTML")
	}

	res2, err := ts.Client().Get(ts.URL + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if js := readAll(t, res2.Body); js != "console.log(1)" {
		t.Fatalf("app.js = %q", js)
	}
}

func TestWebIPCRoundTrip(t *testing.T) {
	ts := httptest.NewServer(newTestApp(t).Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ipc"
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))

	send := func(m webview.Message) {
		raw, _ := json.Marshal(m)
		if err := c.WriteMessage(websocket.TextMessage, raw); err != nil {
			t.Fatal(err)
		}
	}
	recv := func() webview.Message {
		_, raw, err := c.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var m webview.Message
		_ = json.Unmarshal(raw, &m)
		return m
	}

	send(webview.Message{ID: "1", Kind: "command", Name: "echo", State: "start", Data: json.RawMessage(`"hi"`)})
	reply := recv()
	if reply.State != "done" {
		t.Fatalf("state=%q err=%q", reply.State, reply.Error)
	}
	var got string
	_ = json.Unmarshal(reply.Data, &got)
	if got != "echo:hi" {
		t.Fatalf("payload=%q", got)
	}

	send(webview.Message{ID: "2", Kind: "command", Name: "count", State: "start"})
	for i := 0; i < 3; i++ {
		if m := recv(); m.State != "chunk" {
			t.Fatalf("chunk %d state=%q", i, m.State)
		}
	}
	if m := recv(); m.State != "done" {
		t.Fatalf("terminal state=%q", m.State)
	}
}

// TestWebTokenGate: with Options.WebToken set, assets and /ipc require the
// token; presenting it via ?token= sets the session cookie.
func TestWebTokenGate(t *testing.T) {
	a, err := New(Options{URL: "scorix://app/index.html", WebToken: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	a.Serve("scorix", fstest.MapFS{
		"index.html": {Data: []byte("<html><head></head><body>hi</body></html>")},
	})
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()

	// 1. No token → 401 for assets and for the /ipc upgrade.
	res, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("asset without token: status=%d, want 401", res.StatusCode)
	}
	if _, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/ipc", nil); err == nil {
		t.Fatal("ws without token must fail")
	}

	// 2. Wrong token → still 401.
	res2, _ := ts.Client().Get(ts.URL + "/?token=wrong")
	res2.Body.Close()
	if res2.StatusCode != 401 {
		t.Fatalf("asset with wrong token: status=%d, want 401", res2.StatusCode)
	}

	// 3. Correct token → 200 + session cookie.
	res3, err := ts.Client().Get(ts.URL + "/?token=s3cret")
	if err != nil {
		t.Fatal(err)
	}
	res3.Body.Close()
	if res3.StatusCode != 200 {
		t.Fatalf("asset with token: status=%d, want 200", res3.StatusCode)
	}
	var cookie string
	for _, c := range res3.Cookies() {
		if c.Name == webTokenCookie {
			cookie = c.Value
		}
	}
	if cookie != "s3cret" {
		t.Fatalf("session cookie not set, got %q", cookie)
	}

	// 4. /ipc with cookie (and with Bearer header) → upgrade succeeds.
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ipc"
	hdr := map[string][]string{"Cookie": {webTokenCookie + "=s3cret"}}
	c1, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("ws with cookie: %v", err)
	}
	c1.Close()
	c2, _, err := websocket.DefaultDialer.Dial(wsURL, map[string][]string{"Authorization": {"Bearer s3cret"}})
	if err != nil {
		t.Fatalf("ws with bearer: %v", err)
	}
	c2.Close()
}

// TestEmitToTargetsSingleClient proves a handler can push an event back to the
// exact client that invoked it (ClientFrom + EmitTo) without other connected
// clients receiving it.
func TestEmitToTargetsSingleClient(t *testing.T) {
	a := newTestApp(t)
	a.Command("notify-me", func(ctx context.Context, _ json.RawMessage, _ ipc.Stream) (any, error) {
		id, ok := ClientFrom(ctx)
		if !ok {
			t.Error("ClientFrom: not bound on web transport")
			return nil, nil
		}
		if !a.EmitTo(id, "private:ping", "just-for-you") {
			t.Error("EmitTo: client reported disconnected")
		}
		return "ok", nil
	})
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()

	cA := dialWS(t, ts)
	defer cA.Close()
	cB := dialWS(t, ts)
	defer cB.Close()

	wsSend(t, cA, webview.Message{ID: "1", Kind: "command", Name: "notify-me", State: "start"})

	// A receives the targeted event and the command reply (order not guaranteed).
	var sawEvent, sawDone bool
	for i := 0; i < 2; i++ {
		switch m := wsRecv(t, cA); {
		case m.Kind == "event" && m.Name == "private:ping":
			sawEvent = true
		case m.State == "done":
			sawDone = true
		default:
			t.Fatalf("unexpected message: kind=%q name=%q state=%q", m.Kind, m.Name, m.State)
		}
	}
	if !sawEvent || !sawDone {
		t.Fatalf("event=%v done=%v, want both", sawEvent, sawDone)
	}

	// B must receive nothing: a short read deadline should expire.
	_ = cB.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, raw, err := cB.ReadMessage(); err == nil {
		t.Fatalf("client B received foreign traffic: %s", raw)
	}
}

// TestModuleStore proves an *existing* module (store, unchanged) runs on the
// vNext stack: its mod:store:* handlers are reachable over the web transport.
func TestModuleStore(t *testing.T) {
	a, err := New(Options{URL: "scorix://app/index.html"})
	if err != nil {
		t.Fatal(err)
	}
	a.Serve("scorix", fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})
	a.SetModuleConfig("store", map[string]any{"path": filepath.Join(t.TempDir(), "s.json")})
	a.Module(store.New())

	ts := httptest.NewServer(a.Handler()) // starts modules -> registers mod:store:*
	defer ts.Close()
	defer a.stopModules()

	c := dialWS(t, ts)
	defer c.Close()

	wsSend(t, c, webview.Message{ID: "1", Kind: "command", Name: "mod:store:Set", State: "start",
		Data: json.RawMessage(`{"key":"greeting","value":"xin chao"}`)})
	if r := wsRecv(t, c); r.State != "done" {
		t.Fatalf("Set state=%q err=%q", r.State, r.Error)
	}

	wsSend(t, c, webview.Message{ID: "2", Kind: "command", Name: "mod:store:Get", State: "start",
		Data: json.RawMessage(`{"key":"greeting"}`)})
	r := wsRecv(t, c)
	if r.State != "done" {
		t.Fatalf("Get state=%q err=%q", r.State, r.Error)
	}
	var got string
	_ = json.Unmarshal(r.Data, &got)
	if got != "xin chao" {
		t.Fatalf("Get = %q, want xin chao", got)
	}
}
