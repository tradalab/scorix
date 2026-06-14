package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

type Stream interface {
	Chunk(v any) error
}

// ctx is canceled when the client sends a cancel message for this request id.
type CmdFunc func(ctx context.Context, data json.RawMessage, s Stream) (any, error)

type EvtFunc func(ctx context.Context, data json.RawMessage)

type Registry struct {
	mu   sync.RWMutex
	cmds map[string]CmdFunc
	evts map[string]EvtFunc
}

func NewRegistry() *Registry {
	return &Registry{
		cmds: map[string]CmdFunc{},
		evts: map[string]EvtFunc{},
	}
}

func (r *Registry) Command(name string, fn CmdFunc) {
	r.mu.Lock()
	r.cmds[name] = fn
	r.mu.Unlock()
}

func (r *Registry) Event(name string, fn EvtFunc) {
	r.mu.Lock()
	r.evts[name] = fn
	r.mu.Unlock()
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
