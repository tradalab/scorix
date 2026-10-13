package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"sync"
)

type Stream interface {
	Chunk(v any) error
}

// ctx is canceled when the client sends a cancel message for this request id.
type CmdFunc func(ctx context.Context, data json.RawMessage, s Stream) (any, error)

type EvtFunc func(ctx context.Context, data json.RawMessage)

type rpcEntry struct {
	arity Arity
	fn    Handler
}

type Registry struct {
	mu   sync.RWMutex
	cmds map[string]CmdFunc
	evts map[string]EvtFunc
	rpcs map[string]rpcEntry

	guards map[string]func(CmdFunc) CmdFunc
}

func NewRegistry() *Registry {
	return &Registry{
		cmds: map[string]CmdFunc{},
		evts: map[string]EvtFunc{},
		rpcs: map[string]rpcEntry{},
	}
}

func (r *Registry) Command(name string, fn CmdFunc) {
	r.mu.Lock()
	if g := r.guards[name]; g != nil {
		fn = g(fn)
	}
	r.cmds[name] = fn
	r.mu.Unlock()
}

func (r *Registry) Event(name string, fn EvtFunc) {
	r.mu.Lock()
	r.evts[name] = fn
	r.mu.Unlock()
}

func (r *Registry) RPC(name string, arity Arity, fn Handler) {
	r.mu.Lock()
	r.rpcs[name] = rpcEntry{arity: arity, fn: fn}
	r.mu.Unlock()
}

func (r *Registry) rpc(name string) (rpcEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.rpcs[name]
	return e, ok
}

// Invoke already refuses an unknown name, so this exists for the other half: a
// caller that has to find out what it may ask for, rather than guess and read
// the refusal.
func (r *Registry) Names() []string {
	r.mu.RLock()
	names := make([]string, 0, len(r.cmds))
	for name := range r.cmds {
		names = append(names, name)
	}
	r.mu.RUnlock()
	sort.Strings(names)
	return names
}

// Has covers rpcs too, unlike Names.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, cmd := r.cmds[name]
	_, rpc := r.rpcs[name]
	return cmd || rpc
}

// Guard also wraps every later registration of name: a guard that a later
// Command replaced, or that came first, would guard nothing.
func (r *Registry) Guard(name string, wrap func(CmdFunc) CmdFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.guards == nil {
		r.guards = map[string]func(CmdFunc) CmdFunc{}
	}
	if r.guards[name] != nil {
		return
	}
	r.guards[name] = wrap
	if fn, ok := r.cmds[name]; ok {
		r.cmds[name] = wrap(fn)
	}
}

func (r *Registry) command(name string) (CmdFunc, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fn, ok := r.cmds[name]
	return fn, ok
}

// Go->Go RPC. Streaming is not available on this path.
func (r *Registry) Invoke(ctx context.Context, name string, data json.RawMessage) (json.RawMessage, error) {
	fn, ok := r.command(name)
	if !ok {
		return nil, fmt.Errorf("no handler: %s", name)
	}
	res, err := fn(ctx, data, noopStream{})
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, nil
	}
	return json.Marshal(res)
}

type noopStream struct{}

func (noopStream) Chunk(any) error { return nil }

func (r *Registry) event(name string) (EvtFunc, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fn, ok := r.evts[name]
	return fn, ok
}

// InvokeStream is for a caller with no dispatcher (the command line); ok is false
// when name is not a server-stream rpc.
func (r *Registry) InvokeStream(ctx context.Context, name string, req json.RawMessage, send func(v any) error) (ok bool, err error) {
	e, found := r.rpc(name)
	if !found || e.arity != ServerStream {
		return false, nil
	}
	return true, e.fn(ctx, &callStream{ctx: ctx, req: req, send: send})
}

type callStream struct {
	ctx  context.Context
	req  json.RawMessage
	sent bool
	send func(v any) error
}

func (s *callStream) Recv() (json.RawMessage, error) {
	if s.sent {
		return nil, io.EOF
	}
	s.sent = true
	return s.req, nil
}

func (s *callStream) Send(v any) error         { return s.send(v) }
func (s *callStream) Context() context.Context { return s.ctx }
func (s *callStream) Client() (ClientID, bool) { return 0, false }
