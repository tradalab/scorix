// Package ipc dispatches the wire envelope between a frontend transport and a
// Registry. The wire protocol (arity model, open/data/end/cancel ↔ msg/done/error
// frames) is documented inline in stream.go and on the dispatch methods below.
package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/webview"
)

const DefaultMaxConcurrent = 64

// DefaultMaxStreams caps concurrent server-stream/duplex calls per client; each
// is long-lived and costs a goroutine + rawStream, so unbounded opens are a DoS.
const DefaultMaxStreams = 256

var traceIPC = os.Getenv("SCORIX_IPC_TRACE") != ""

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type Dispatcher struct {
	reg       *Registry
	send      func([]byte) error
	sem       chan struct{} // bounds concurrent unary handlers
	streamSem chan struct{} // bounds concurrent server-stream/duplex calls

	mu      sync.Mutex
	pending map[string]context.CancelFunc
	calls   map[string]*rawStream // active rpc calls, keyed by message id
	client  ClientID
	bound   bool
	closing bool // gates new in-flight work: no wg.Add after Close's wg.Wait

	wg sync.WaitGroup // in-flight handlers; awaited by Close
}

func NewDispatcher(reg *Registry, send func([]byte) error) *Dispatcher {
	return &Dispatcher{
		reg:       reg,
		send:      send,
		sem:       make(chan struct{}, DefaultMaxConcurrent),
		streamSem: make(chan struct{}, DefaultMaxStreams),
		pending:   map[string]context.CancelFunc{},
		calls:     map[string]*rawStream{},
	}
}

// begin reserves a wg slot under d.mu, refusing once closing — so no wg.Add can
// race Close's wg.Wait at a zero counter.
func (d *Dispatcher) begin() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closing {
		return false
	}
	d.wg.Add(1)
	return true
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
	d.closing = true
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
	case msg.Kind == webview.KindRPC:
		d.handleRPC(msg)
	case msg.State == "cancel":
		d.cancel(msg.ID)
	case msg.Kind == webview.KindEvent:
		d.dispatchEvent(msg)
	default:
		d.dispatchCommand(msg)
	}
}

func (d *Dispatcher) handleRPC(msg webview.Message) {
	switch msg.State {
	case webview.StateOpen:
		d.openRPC(msg)
	case webview.StateData:
		d.feedRPC(msg.ID, msg.Data, false)
	case webview.StateEnd:
		d.feedRPC(msg.ID, msg.Data, true)
	case webview.StateCancel:
		d.cancelRPC(msg.ID)
	}
}

func (d *Dispatcher) openRPC(msg webview.Message) {
	e, ok := d.reg.rpc(msg.Name)
	if !ok {
		d.emit(webview.Message{ID: msg.ID, Kind: webview.KindRPC, Name: msg.Name, State: webview.StateError, Error: "no handler: " + msg.Name})
		return
	}

	ctx, cancel := context.WithCancel(d.handlerCtx())
	rs := newRawStream(msg.ID, msg.Name, ctx, d.emitErr)
	d.mu.Lock()
	if d.closing { // no wg.Add after Close's wg.Wait
		d.mu.Unlock()
		cancel()
		return
	}
	d.pending[msg.ID] = cancel
	d.calls[msg.ID] = rs
	d.wg.Add(1)
	d.mu.Unlock()
	if len(msg.Data) > 0 { // open frame may carry the first client message
		rs.push(msg.Data)
	}

	if traceIPC {
		logger.Info("ipc action", "kind", "rpc", "name", msg.Name, "id", msg.ID, "arity", e.arity.String(), "phase", "start")
	}
	start := time.Now()

	go func() {
		defer d.wg.Done()
		// Unary on the shared sem (ctx-aware acquire so a cancel during a full
		// queue stays serviceable); streams on a separate budget so they can't
		// starve unary clicks, rejecting rather than queueing past the cap.
		if e.arity == Unary {
			select {
			case d.sem <- struct{}{}:
			case <-ctx.Done():
				d.finishRPC(msg.ID, msg.Name, ctx.Err())
				return
			}
			defer func() { <-d.sem }()
		} else {
			select {
			case d.streamSem <- struct{}{}:
			default:
				d.finishRPC(msg.ID, msg.Name, fmt.Errorf("too many concurrent streams (max %d)", DefaultMaxStreams))
				return
			}
			defer func() { <-d.streamSem }()
		}

		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("handler panicked: %v", r)
				}
			}()
			err = e.fn(ctx, rs)
		}()
		if traceIPC {
			phase := "done"
			if err != nil {
				phase = "error"
			}
			logger.Info("ipc action", "kind", "rpc", "name", msg.Name, "id", msg.ID, "phase", phase, "sent", rs.Sent(), "dur", time.Since(start).String(), "err", errStr(err))
		}
		d.finishRPC(msg.ID, msg.Name, err)
	}()
}

func (d *Dispatcher) feedRPC(id string, data json.RawMessage, end bool) {
	d.mu.Lock()
	rs := d.calls[id]
	d.mu.Unlock()
	if rs == nil {
		// Call already gone server-side: signal closed so a duplex consumer awaiting
		// frames doesn't hang. (JS drops terminal frames for ids it finished.)
		d.emit(webview.Message{ID: id, Kind: webview.KindRPC, State: webview.StateError, Error: "stream closed"})
		return
	}
	if len(data) > 0 {
		rs.push(data)
	}
	if end {
		rs.closeSend()
	}
}

// finishRPC emits the terminal frame unless the call was already canceled (calls
// entry gone => suppress, client is gone).
func (d *Dispatcher) finishRPC(id, name string, err error) {
	d.mu.Lock()
	_, live := d.calls[id]
	cancel := d.pending[id]
	delete(d.calls, id)
	delete(d.pending, id)
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if !live {
		return
	}
	reply := webview.Message{ID: id, Kind: webview.KindRPC, Name: name, State: webview.StateDone}
	if err != nil {
		reply.State = webview.StateError
		reply.Error = err.Error()
	}
	d.emit(reply)
}

func (d *Dispatcher) cancelRPC(id string) {
	d.mu.Lock()
	cancel := d.pending[id]
	delete(d.calls, id)
	delete(d.pending, id)
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (d *Dispatcher) dispatchEvent(msg webview.Message) {
	if fn, ok := d.reg.event(msg.Name); ok {
		if !d.begin() {
			return
		}
		go func() {
			defer d.wg.Done()
			defer func() { _ = recover() }()
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
	if d.closing { // no wg.Add after Close's wg.Wait
		d.mu.Unlock()
		cancel()
		return
	}
	d.pending[msg.ID] = cancel
	d.wg.Add(1)
	d.mu.Unlock()

	if traceIPC {
		logger.Info("ipc action", "kind", "command", "name", msg.Name, "id", msg.ID, "phase", "start")
	}
	start := time.Now()

	go func() {
		defer d.wg.Done()
		cleanup := func() {
			cancel()
			d.mu.Lock()
			delete(d.pending, msg.ID)
			d.mu.Unlock()
		}
		// ctx-aware acquire so a cancel while the sem is full doesn't park this
		// goroutine unobservably — else Close's drain stalls under load.
		select {
		case d.sem <- struct{}{}:
		case <-ctx.Done():
			cleanup()
			return
		}
		defer func() {
			<-d.sem
			cleanup()
		}()

		s := &stream{d: d, id: msg.ID, name: msg.Name}
		var res any
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					res, err = nil, fmt.Errorf("handler panicked: %v", r)
				}
			}()
			res, err = fn(ctx, msg.Data, s)
		}()
		if traceIPC {
			phase := "done"
			if err != nil {
				phase = "error"
			}
			logger.Info("ipc action", "kind", "command", "name", msg.Name, "id", msg.ID, "phase", phase, "dur", time.Since(start).String(), "err", errStr(err))
		}

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
	// also drop the calls entry: a kindless legacy cancel for an rpc id would
	// otherwise leave it live and finishRPC would emit a stray terminal.
	delete(d.calls, id)
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (d *Dispatcher) emit(msg webview.Message) { _ = d.emitErr(msg) }

// emitErr returns the transport write error so a stream looping to a vanished
// client learns the connection is gone instead of spinning on ctx alone.
func (d *Dispatcher) emitErr(msg webview.Message) error {
	out, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if traceIPC {
		logger.Info("ipc ->", "kind", msg.Kind, "name", msg.Name, "id", msg.ID, "state", msg.State, "bytes", len(out))
	}
	return d.send(out)
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
	// Serialize PostMessage: concurrent Sends from in-flight streams must not
	// assume the platform webview marshals writes.
	var wmu sync.Mutex
	send := func(b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return v.PostMessage(b)
	}
	d := NewDispatcher(reg, send)
	v.OnMessage(d.Handle)
	return &NativeBridge{d}
}
