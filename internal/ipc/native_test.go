package ipc_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/tradalab/scorix/internal/driver/headless"
	"github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/webview"
	"github.com/tradalab/scorix/window"
)

func newBridge(t *testing.T, reg *ipc.Registry) window.Window {
	t.Helper()
	rt, err := headless.New().NewRuntime(window.RuntimeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	w, err := rt.Windows().New(window.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	ipc.NewNativeBridge(w.View(), reg)
	return w
}

func send(w window.Window, m webview.Message) {
	raw, _ := json.Marshal(m)
	headless.Inject(w, raw)
}

func waitReplies(t *testing.T, w window.Window, n int) []webview.Message {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw := headless.Sent(w)
		if len(raw) >= n {
			out := make([]webview.Message, len(raw))
			for i, r := range raw {
				_ = json.Unmarshal(r, &out[i])
			}
			return out
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout: got %d replies, want %d", len(headless.Sent(w)), n)
	return nil
}

func TestCommandRoundTrip(t *testing.T) {
	reg := ipc.NewRegistry()
	reg.Command("echo", func(_ context.Context, data json.RawMessage, _ ipc.Stream) (any, error) {
		var s string
		_ = json.Unmarshal(data, &s)
		return "echo:" + s, nil
	})
	w := newBridge(t, reg)

	send(w, webview.Message{ID: "1", Kind: "command", Name: "echo", State: "start", Data: json.RawMessage(`"hi"`)})

	replies := waitReplies(t, w, 1)
	if replies[0].State != "done" {
		t.Fatalf("state=%q err=%q", replies[0].State, replies[0].Error)
	}
	var got string
	_ = json.Unmarshal(replies[0].Data, &got)
	if got != "echo:hi" {
		t.Fatalf("payload=%q, want echo:hi", got)
	}
}

func TestUnknownCommandErrors(t *testing.T) {
	w := newBridge(t, ipc.NewRegistry())
	send(w, webview.Message{ID: "1", Kind: "command", Name: "nope", State: "start"})

	replies := waitReplies(t, w, 1)
	if replies[0].State != "error" || replies[0].Error == "" {
		t.Fatalf("want error reply, got state=%q err=%q", replies[0].State, replies[0].Error)
	}
}

func TestEventNoReply(t *testing.T) {
	reg := ipc.NewRegistry()
	got := make(chan string, 1)
	reg.Event("ping", func(_ context.Context, data json.RawMessage) {
		var s string
		_ = json.Unmarshal(data, &s)
		got <- s
	})
	w := newBridge(t, reg)

	send(w, webview.Message{ID: "1", Kind: "event", Name: "ping", Data: json.RawMessage(`"pong"`)})

	select {
	case v := <-got:
		if v != "pong" {
			t.Fatalf("event payload=%q", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event handler not called")
	}
	if s := headless.Sent(w); len(s) != 0 {
		t.Fatalf("event must not reply, got %d messages", len(s))
	}
}

func TestStreaming(t *testing.T) {
	reg := ipc.NewRegistry()
	reg.Command("count", func(_ context.Context, _ json.RawMessage, s ipc.Stream) (any, error) {
		for i := 0; i < 3; i++ {
			if err := s.Chunk(i); err != nil {
				return nil, err
			}
		}
		return "done", nil
	})
	w := newBridge(t, reg)

	send(w, webview.Message{ID: "7", Kind: "command", Name: "count", State: "start"})

	replies := waitReplies(t, w, 4) // 3 chunks + 1 terminal
	for i := 0; i < 3; i++ {
		if replies[i].State != "chunk" {
			t.Fatalf("reply[%d].State=%q, want chunk", i, replies[i].State)
		}
	}
	if replies[3].State != "done" {
		t.Fatalf("terminal State=%q, want done", replies[3].State)
	}
}

func TestCancellation(t *testing.T) {
	reg := ipc.NewRegistry()
	started := make(chan struct{})
	reg.Command("block", func(ctx context.Context, _ json.RawMessage, _ ipc.Stream) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	w := newBridge(t, reg)

	send(w, webview.Message{ID: "9", Kind: "command", Name: "block", State: "start"})
	<-started // ensure the handler is running and pending is registered
	send(w, webview.Message{ID: "9", State: "cancel"})

	replies := waitReplies(t, w, 1)
	if replies[0].State != "error" || replies[0].Error != context.Canceled.Error() {
		t.Fatalf("want canceled error, got state=%q err=%q", replies[0].State, replies[0].Error)
	}
}
