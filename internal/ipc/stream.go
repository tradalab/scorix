package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/tradalab/scorix/webview"
)

// ErrStreamOverflow is returned by Recv when a client floods the call past
// maxQueuedFrames; the handler should return promptly to tear the call down.
var ErrStreamOverflow = errors.New("ipc: client stream backlog overflow")

// Arity is the message cardinality of an RPC, mirroring gRPC's four kinds.
type Arity int

const (
	Unary        Arity = iota // 1->1
	ServerStream              // 1->N
	ClientStream              // N->1
	BiDi                      // N<->N
)

func (a Arity) String() string {
	switch a {
	case Unary:
		return "unary"
	case ServerStream:
		return "server-stream"
	case ClientStream:
		return "client-stream"
	case BiDi:
		return "bidi"
	default:
		return "unknown"
	}
}

// RawStream is the type-erased per-call stream the runtime hands to a Handler.
// Codegen wraps it in a typed Stream[In,Out]; direct callers marshal/unmarshal
// the raw JSON themselves.
type RawStream interface {
	// Recv returns the next client->server message, blocking until one arrives.
	// It returns io.EOF after the client half-closes (end frame), ctx.Err() if the
	// call is canceled, and ErrStreamOverflow if the client outran the queue bound.
	Recv() (json.RawMessage, error)
	// Send delivers a server->client message (a msg frame).
	Send(v any) error
	Context() context.Context
	Client() (ClientID, bool)
}

// Handler is the single unified server entry point for every arity. The runtime
// terminates the call with done (nil return) or error (non-nil) once it returns.
type Handler func(ctx context.Context, s RawStream) error

// maxQueuedFrames bounds the per-call client->server backlog; on overflow the
// call is failed rather than buffered unboundedly. Guards the duplex path
// (ServerStream only ever queues the single open frame).
const maxQueuedFrames = 1024

// rawStream backs one in-flight RPC. Client frames queue in arrival order and
// drain via Recv; queueing never blocks the dispatcher read loop.
type rawStream struct {
	id   string
	name string
	ctx  context.Context
	send func(webview.Message) error

	mu       sync.Mutex
	queue    [][]byte
	closed   bool          // client sent end
	overflow bool          // queue exceeded maxQueuedFrames
	sent     int           // emitted msg frames (tracing)
	sig      chan struct{} // buffered(1): wakes a blocked Recv on push/close
}

func (s *rawStream) Sent() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent
}

func newRawStream(id, name string, ctx context.Context, send func(webview.Message) error) *rawStream {
	return &rawStream{id: id, name: name, ctx: ctx, send: send, sig: make(chan struct{}, 1)}
}

// push enqueues a client->server message; runs on the dispatcher read loop, so
// it must not block.
func (s *rawStream) push(data json.RawMessage) {
	s.mu.Lock()
	if !s.closed && !s.overflow {
		if len(s.queue) >= maxQueuedFrames {
			s.overflow = true
		} else {
			// copy: the caller may reuse the backing array after Handle returns
			b := make([]byte, len(data))
			copy(b, data)
			s.queue = append(s.queue, b)
		}
	}
	s.mu.Unlock()
	s.wake()
}

// closeSend marks the client half-closed; queued frames stay drainable, then
// Recv reports io.EOF.
func (s *rawStream) closeSend() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.wake()
}

func (s *rawStream) wake() {
	select {
	case s.sig <- struct{}{}:
	default:
	}
}

func (s *rawStream) Recv() (json.RawMessage, error) {
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			v := s.queue[0]
			s.queue = s.queue[1:]
			s.mu.Unlock()
			return v, nil
		}
		overflow, closed := s.overflow, s.closed
		s.mu.Unlock()
		if overflow {
			return nil, ErrStreamOverflow
		}
		if closed {
			return nil, io.EOF
		}
		select {
		case <-s.ctx.Done():
			return nil, s.ctx.Err()
		case <-s.sig:
		}
	}
}

func (s *rawStream) Send(v any) error {
	// Fence: finishRPC cancels ctx before the terminal frame, so a handler that
	// outlives its return can't emit a msg frame after done — it gets the error.
	if err := s.ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.sent++
	s.mu.Unlock()
	return s.send(webview.Message{ID: s.id, Kind: webview.KindRPC, Name: s.name, State: webview.StateMsg, Data: data})
}

func (s *rawStream) Context() context.Context { return s.ctx }

func (s *rawStream) Client() (ClientID, bool) { return ClientFrom(s.ctx) }
