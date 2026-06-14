package ipc_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/webview"
)

func newDispatcher(reg *ipc.Registry) (*ipc.Dispatcher, *[][]byte) {
	var sent [][]byte
	d := ipc.NewDispatcher(reg, func(b []byte) { sent = append(sent, b) })
	return d, &sent
}

func handle(d *ipc.Dispatcher, m webview.Message) {
	raw, _ := json.Marshal(m)
	d.Handle(raw)
}

func TestClientFromCommandContext(t *testing.T) {
	reg := ipc.NewRegistry()
	got := make(chan ipc.ClientID, 1)
	reg.Command("who", func(ctx context.Context, _ json.RawMessage, _ ipc.Stream) (any, error) {
		id, ok := ipc.ClientFrom(ctx)
		if !ok {
			t.Error("ClientFrom: not bound in command ctx")
		}
		got <- id
		return nil, nil
	})
	d, _ := newDispatcher(reg)
	d.BindClient(42)

	handle(d, webview.Message{ID: "1", Kind: "command", Name: "who", State: "start"})

	select {
	case id := <-got:
		if id != 42 {
			t.Fatalf("client id=%d, want 42", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("command handler not called")
	}
}

func TestClientFromEventContext(t *testing.T) {
	reg := ipc.NewRegistry()
	got := make(chan ipc.ClientID, 1)
	reg.Event("ping", func(ctx context.Context, _ json.RawMessage) {
		id, _ := ipc.ClientFrom(ctx)
		got <- id
	})
	d, _ := newDispatcher(reg)
	d.BindClient(7)

	handle(d, webview.Message{ID: "1", Kind: "event", Name: "ping"})

	select {
	case id := <-got:
		if id != 7 {
			t.Fatalf("client id=%d, want 7", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event handler not called")
	}
}

func TestClientFromUnbound(t *testing.T) {
	reg := ipc.NewRegistry()
	ok := make(chan bool, 1)
	reg.Command("who", func(ctx context.Context, _ json.RawMessage, _ ipc.Stream) (any, error) {
		_, bound := ipc.ClientFrom(ctx)
		ok <- bound
		return nil, nil
	})
	d, _ := newDispatcher(reg)

	handle(d, webview.Message{ID: "1", Kind: "command", Name: "who", State: "start"})

	select {
	case bound := <-ok:
		if bound {
			t.Fatal("ClientFrom reported bound on an unbound dispatcher")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler not called")
	}
}

func TestCloseCancelsAndDrains(t *testing.T) {
	reg := ipc.NewRegistry()
	started := make(chan struct{})
	finished := make(chan struct{})
	reg.Command("block", func(ctx context.Context, _ json.RawMessage, _ ipc.Stream) (any, error) {
		close(started)
		<-ctx.Done()
		close(finished)
		return nil, ctx.Err()
	})
	d, _ := newDispatcher(reg)

	handle(d, webview.Message{ID: "1", Kind: "command", Name: "block", State: "start"})
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := d.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("Close returned before the handler goroutine finished")
	}
}

func TestCloseDeadlineOnStuckHandler(t *testing.T) {
	reg := ipc.NewRegistry()
	started := make(chan struct{})
	release := make(chan struct{})
	reg.Command("stuck", func(_ context.Context, _ json.RawMessage, _ ipc.Stream) (any, error) {
		close(started)
		<-release // ignores ctx on purpose
		return nil, nil
	})
	d, _ := newDispatcher(reg)

	handle(d, webview.Message{ID: "1", Kind: "command", Name: "stuck", State: "start"})
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := d.Close(ctx); err == nil {
		t.Fatal("Close: want deadline error for a stuck handler, got nil")
	}
	close(release)
}
