// Package ipc dispatches the wire envelope between a frontend transport and a Registry; see docs/IPC_SPEC.md.
package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/webview"
)

const DefaultMaxConcurrent = 64

// traceIPC (SCORIX_IPC_TRACE=1) logs every envelope through the dispatcher.
var traceIPC = os.Getenv("SCORIX_IPC_TRACE") != ""

type Dispatcher struct {
	reg  *Registry
	send func([]byte)
	sem  chan struct{}

	mu      sync.Mutex
	pending map[string]context.CancelFunc
	client  ClientID
	bound   bool

	wg sync.WaitGroup // in-flight handlers; awaited by Close
}

func NewDispatcher(reg *Registry, send func([]byte)) *Dispatcher {
	return &Dispatcher{
		reg:     reg,
		send:    send,
		sem:     make(chan struct{}, DefaultMaxConcurrent),
		pending: map[string]context.CancelFunc{},
	}
}

// BindClient must precede message delivery.
func (d *Dispatcher) BindClient(id ClientID) {
	d.mu.Lock()
	d.client = id
	d.bound = true
	d.mu.Unlock()
}

func (d *Dispatcher) handlerCtx() context.Context {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.bound {
		return context.Background()
	}
	return WithClient(context.Background(), d.client)
}

// Close cancels in-flight commands and awaits their handlers within ctx's
// deadline; returns ctx.Err() on timeout.
func (d *Dispatcher) Close(ctx context.Context) error {
	d.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(d.pending))
	for _, c := range d.pending {
		cancels = append(cancels, c)
	}
	d.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Dispatcher) Handle(raw []byte) {
	var msg webview.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}
	if traceIPC {
		logger.Info("ipc <-", "kind", msg.Kind, "name", msg.Name, "id", msg.ID, "state", msg.State, "bytes", len(raw))
	}
	switch {
	case msg.State == "cancel":
		d.cancel(msg.ID)
	case msg.Kind == "event":
		d.dispatchEvent(msg)
	default:
		d.dispatchCommand(msg)
	}
}

func (d *Dispatcher) dispatchEvent(msg webview.Message) {
	if fn, ok := d.reg.event(msg.Name); ok {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			defer func() { _ = recover() }() // isolate handler panic
			fn(d.handlerCtx(), msg.Data)
		}()
	}
}

func (d *Dispatcher) dispatchCommand(msg webview.Message) {
	fn, ok := d.reg.command(msg.Name)
	if !ok {
		d.emit(webview.Message{ID: msg.ID, Kind: "command", Name: msg.Name, State: "error", Error: "no handler: " + msg.Name})
		return
	}

	ctx, cancel := context.WithCancel(d.handlerCtx())
	d.mu.Lock()
	d.pending[msg.ID] = cancel
	d.mu.Unlock()

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		// semaphore acquired here, not in the read loop, so cancels stay serviceable at capacity
		d.sem <- struct{}{}
		defer func() {
			<-d.sem
			cancel()
			d.mu.Lock()
			delete(d.pending, msg.ID)
			d.mu.Unlock()
		}()

		s := &stream{d: d, id: msg.ID, name: msg.Name}
		var res any
		var err error
		// handler panic → error reply
		func() {
			defer func() {
				if r := recover(); r != nil {
					res, err = nil, fmt.Errorf("handler panicked: %v", r)
				}
			}()
			res, err = fn(ctx, msg.Data, s)
		}()

		reply := webview.Message{ID: msg.ID, Kind: "command", Name: msg.Name, State: "done"}
		switch {
		case err != nil:
			reply.State = "error"
			reply.Error = err.Error()
		case res != nil:
			if data, mErr := json.Marshal(res); mErr == nil {
				reply.Data = data
			}
		}
		d.emit(reply)
	}()
}

func (d *Dispatcher) cancel(id string) {
	d.mu.Lock()
	cancel := d.pending[id]
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (d *Dispatcher) emit(msg webview.Message) {
	if out, err := json.Marshal(msg); err == nil {
		if traceIPC {
			logger.Info("ipc ->", "kind", msg.Kind, "name", msg.Name, "id", msg.ID, "state", msg.State, "bytes", len(out))
		}
		d.send(out)
	}
}

type stream struct {
	d    *Dispatcher
	id   string
	name string
}

func (s *stream) Chunk(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.d.emit(webview.Message{ID: s.id, Kind: "command", Name: s.name, State: "chunk", Data: data})
	return nil
}

type NativeBridge struct{ *Dispatcher }

func NewNativeBridge(v webview.View, reg *Registry) *NativeBridge {
	d := NewDispatcher(reg, func(b []byte) { _ = v.PostMessage(b) })
	v.OnMessage(d.Handle)
	return &NativeBridge{d}
}
