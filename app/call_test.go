package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tradalab/scorix/fault"
	ipc "github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/webview"
)

func startCallEcho(t *testing.T, a *App, ts *httptest.Server, respond func(webview.Message) *webview.Message) ClientID {
	t.Helper()
	idCh := make(chan ClientID, 1)
	a.reg.Command("test:who", func(ctx context.Context, _ json.RawMessage, _ ipc.Stream) (any, error) {
		id, _ := ClientFrom(ctx)
		select {
		case idCh <- id:
		default:
		}
		return nil, nil
	})

	c := dialWS(t, ts)
	t.Cleanup(func() { c.Close() })
	wsSend(t, c, webview.Message{ID: "w", Kind: "command", Name: "test:who", State: "start"})

	go func() {
		for {
			_, raw, err := c.ReadMessage()
			if err != nil {
				return
			}
			var m webview.Message
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			if m.Kind == "call" {
				if reply := respond(m); reply != nil {
					raw, _ := json.Marshal(*reply)
					_ = c.WriteMessage(1, raw)
				}
			}
		}
	}()

	select {
	case id := <-idCh:
		return id
	case <-time.After(2 * time.Second):
		t.Fatal("client id never resolved")
		return 0
	}
}

func TestCallRoundTrip(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()

	client := startCallEcho(t, a, ts, func(m webview.Message) *webview.Message {
		var in map[string]int
		_ = json.Unmarshal(m.Data, &in)
		out, _ := json.Marshal(map[string]int{"doubled": in["n"] * 2})
		return &webview.Message{ID: m.ID, Kind: "callreply", Name: m.Name, State: "done", Data: out}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := a.Call(ctx, client, "math:double", map[string]int{"n": 21})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	_ = json.Unmarshal(res, &got)
	if got["doubled"] != 42 {
		t.Fatalf("result = %s", res)
	}
}

func TestCallErrorReply(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()

	client := startCallEcho(t, a, ts, func(m webview.Message) *webview.Message {
		return &webview.Message{ID: m.ID, Kind: "callreply", Name: m.Name, State: "error", Error: "scorix: no resolver for math:double"}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := a.Call(ctx, client, "math:double", nil)
	if err == nil || fault.CodeOf(err) != "call_failed" {
		t.Fatalf("err = %v, want call_failed", err)
	}
}

func TestCallTimeoutAndDisconnected(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()

	client := startCallEcho(t, a, ts, func(webview.Message) *webview.Message { return nil })

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := a.Call(ctx, client, "never:answers", nil); fault.CodeOf(err) != fault.CodeCanceled {
		t.Fatalf("timeout err = %v, want canceled", err)
	}

	if _, err := a.Call(context.Background(), ClientID(9999), "x", nil); fault.CodeOf(err) != fault.CodeUnavailable {
		t.Fatalf("disconnected err = %v, want unavailable", err)
	}
}

func TestCallSpoofedReplyIgnored(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()

	callID := make(chan string, 1)
	client := startCallEcho(t, a, ts, func(m webview.Message) *webview.Message {
		select {
		case callID <- m.ID:
		default:
		}
		return nil // the addressed client never answers
	})

	spoofer := dialWS(t, ts)
	defer spoofer.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		res, err := a.Call(ctx, client, "secure:op", nil)
		if err == nil {
			t.Errorf("spoofed reply accepted: %s", res)
		}
		if fault.CodeOf(err) != fault.CodeCanceled {
			t.Errorf("err = %v, want canceled (spoof must be ignored)", err)
		}
	}()

	select {
	case id := <-callID:
		forged, _ := json.Marshal(map[string]string{"stolen": "data"})
		wsSend(t, spoofer, webview.Message{ID: id, Kind: "callreply", State: "done", Data: forged})
	case <-time.After(2 * time.Second):
		t.Fatal("call frame never reached the client")
	}
	<-done
}

func TestCallFailsFastOnDisconnect(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()

	c := dialWS(t, ts)
	idCh := make(chan ClientID, 1)
	a.reg.Command("test:who2", func(ctx context.Context, _ json.RawMessage, _ ipc.Stream) (any, error) {
		id, _ := ClientFrom(ctx)
		select {
		case idCh <- id:
		default:
		}
		return nil, nil
	})
	wsSend(t, c, webview.Message{ID: "w", Kind: "command", Name: "test:who2", State: "start"})
	var client ClientID
	select {
	case client = <-idCh:
	case <-time.After(2 * time.Second):
		t.Fatal("client id never resolved")
	}

	errCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := a.Call(ctx, client, "never:answers", nil)
		errCh <- err
	}()
	time.Sleep(100 * time.Millisecond)
	c.Close() // hang up mid-call

	select {
	case err := <-errCh:
		if fault.CodeOf(err) != fault.CodeUnavailable {
			t.Fatalf("err = %v, want unavailable", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call did not fail fast after the client disconnected")
	}
}
