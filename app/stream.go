package app

import (
	"context"
	"encoding/json"
	"io"

	ipc "github.com/tradalab/scorix/internal/ipc"
)

// Arity is the message cardinality of an RPC (the four gRPC kinds).
type Arity = ipc.Arity

const (
	Unary        = ipc.Unary
	ServerStream = ipc.ServerStream
	ClientStream = ipc.ClientStream
	BiDi         = ipc.BiDi
)

// Stream is the typed per-call channel for a streaming handler. In/Out are the
// client->server and server->client types; valid ops depend on arity.
type Stream[In any, Out any] struct {
	raw ipc.RawStream
}

// Recv returns the next client message; io.EOF once the client half-closes.
func (s *Stream[In, Out]) Recv() (*In, error) {
	raw, err := s.raw.Recv()
	if err != nil {
		return nil, err
	}
	var v In
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
	}
	return &v, nil
}

func (s *Stream[In, Out]) Send(v *Out) error        { return s.raw.Send(v) }
func (s *Stream[In, Out]) Context() context.Context { return s.raw.Context() }
func (s *Stream[In, Out]) Client() (ClientID, bool) { return s.raw.Client() }

// Sink is the Send-only server->client half of a ServerStream, so the compiler
// (not a runtime check) prevents misusing Recv.
type Sink[Out any] struct {
	raw ipc.RawStream
}

// Send pushes one message. On the web (WebSocket) transport it returns the write
// error once the client has gone, so a producer can stop feeding a dead consumer;
// native PostMessage is fire-and-forget — prefer Context() cancellation there.
func (s Sink[Out]) Send(v *Out) error        { return s.raw.Send(v) }
func (s Sink[Out]) Context() context.Context { return s.raw.Context() }
func (s Sink[Out]) Client() (ClientID, bool) { return s.raw.Client() }

// RPC registers a raw v2 handler. Codegen calls the typed helpers instead.
func (a *App) RPC(name string, arity Arity, fn ipc.Handler) { a.reg.RPC(name, arity, fn) }

// RegisterUnary adapts func(ctx,*Req)(*Res,error) onto the streaming runtime.
func RegisterUnary[Req any, Res any](a *App, name string, fn func(context.Context, *Req) (*Res, error)) {
	a.reg.RPC(name, ipc.Unary, func(ctx context.Context, raw ipc.RawStream) error {
		s := &Stream[Req, Res]{raw: raw}
		req, err := s.Recv()
		if err != nil && err != io.EOF {
			return err
		}
		res, err := fn(ctx, req)
		if err != nil {
			return err
		}
		if res != nil {
			return s.Send(res)
		}
		return nil
	})
}

// RegisterServerStream registers a 1->N handler (one request, then Sink pushes).
// The only streaming arity codegen emits; the open frame carries the request.
func RegisterServerStream[Req any, Out any](a *App, name string, fn func(context.Context, *Req, Sink[Out]) error) {
	a.reg.RPC(name, ipc.ServerStream, func(ctx context.Context, raw ipc.RawStream) error {
		raw0, err := raw.Recv()
		if err != nil && err != io.EOF {
			return err
		}
		var req Req
		if len(raw0) > 0 && string(raw0) != "null" {
			if err := json.Unmarshal(raw0, &req); err != nil {
				return err
			}
		}
		return fn(ctx, &req, Sink[Out]{raw: raw})
	})
}

// RegisterDuplex wires the N<->N arity by hand (NOT emitted by codegen); use only
// when a real duplex consumer justifies the open/data/end client framing.
func RegisterDuplex[In any, Out any](a *App, name string, fn func(context.Context, *Stream[In, Out]) error) {
	a.reg.RPC(name, ipc.BiDi, func(ctx context.Context, raw ipc.RawStream) error {
		return fn(ctx, &Stream[In, Out]{raw: raw})
	})
}
