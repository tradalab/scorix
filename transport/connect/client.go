package connect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/tradalab/scorix/transport/internal/gate"
)

type ConnOptions struct {
	// GRPC speaks gRPC, which a zrpc or grpc-go server understands; false speaks Connect.
	GRPC bool
	// Header adds what the callee reads off a call, such as a service token, before it is sent.
	Header func(ctx context.Context, h http.Header)
	// HTTPClient nil picks one that speaks cleartext HTTP/2 for an http:// base with GRPC.
	HTTPClient connect.HTTPClient
	// ReadMaxBytes caps each reply message; a larger one fails with CodeResourceExhausted. Zero or less
	// means 4 MiB, the cap a Mux reads requests with; set a larger value to raise it, nothing lifts it.
	ReadMaxBytes int
	// Timeout bounds every unary call whose method declares no timeout of its own; the call ends
	// at the earlier of it and the caller's deadline. Zero adds no bound. Streams ignore it.
	Timeout time.Duration
}

type methodBound struct{}

// MethodTimeout bounds a call by its method's own timeout, which then replaces the Conn's default;
// the caller's earlier deadline still wins. Generated clients call it for a timeout trait.
func MethodTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithValue(ctx, methodBound{}, true), d)
}

// Conn reaches one service at base, for example "http://shorten-rpc:8070". It is safe for
// concurrent use; Call errors are *connect.Error carrying the callee's code and message.
type Conn struct {
	base    string
	opt     ConnOptions
	clients sync.Map
	now     func() time.Time
	roll    func() float64
}

type procedure[Req, Res any] struct {
	client  *connect.Client[Req, Res]
	breaker *breaker
}

func Dial(base string, opt ConnOptions) *Conn {
	if opt.Header == nil {
		opt.Header = func(context.Context, http.Header) {}
	}
	if opt.ReadMaxBytes <= 0 {
		opt.ReadMaxBytes = gate.MaxBody
	}
	if opt.HTTPClient == nil {
		opt.HTTPClient = http.DefaultClient
		if opt.GRPC && strings.HasPrefix(base, "http://") {
			var p http.Protocols
			p.SetUnencryptedHTTP2(true)
			opt.HTTPClient = &http.Client{Transport: &http.Transport{Protocols: &p}}
		}
	}
	return &Conn{base: strings.TrimSuffix(base, "/"), opt: opt, now: time.Now, roll: rand.Float64}
}

func open[Req, Res any](c *Conn, name string) (procedure[Req, Res], error) {
	cached, ok := c.clients.Load(name)
	if !ok {
		opts := []connect.ClientOption{connect.WithReadMaxBytes(c.opt.ReadMaxBytes)}
		if c.opt.GRPC {
			opts = append(opts, connect.WithGRPC())
		}
		cached, _ = c.clients.LoadOrStore(name, procedure[Req, Res]{
			client:  connect.NewClient[Req, Res](c.opt.HTTPClient, c.base+name, opts...),
			breaker: &breaker{now: c.now, roll: c.roll},
		})
	}
	p := cached.(procedure[Req, Res])
	if !p.breaker.allow() {
		return p, connect.NewError(connect.CodeUnavailable, fmt.Errorf("%s%s is failing, so this call was not sent", c.base, name))
	}
	return p, nil
}

// Call sends req to procedure and waits for the reply; the ctx deadline travels with it.
// While the callee keeps failing with unavailable, internal or a like code, a share of calls to
// that procedure fails fast with unavailable; business refusals and the caller's own deadline never count.
func Call[Req, Res any](ctx context.Context, c *Conn, name string, req *Req) (*Res, error) {
	p, err := open[Req, Res](c, name)
	if err != nil {
		return nil, err
	}
	if ctx.Value(methodBound{}) == nil && c.opt.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.opt.Timeout)
		defer cancel()
	}
	r := connect.NewRequest(req)
	c.opt.Header(ctx, r.Header())
	res, err := p.client.CallUnary(ctx, r)
	p.breaker.settle(ctx, err)
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// Stream sends req to a server-stream procedure and calls each for every message in order.
// It returns the stream's own error, or each's first error after telling the callee to stop;
// the breaker counts a stream as Call counts a call.
func Stream[Req, Res any](ctx context.Context, c *Conn, name string, req *Req, each func(*Res) error) error {
	p, err := open[Req, Res](c, name)
	if err != nil {
		return err
	}
	r := connect.NewRequest(req)
	c.opt.Header(ctx, r.Header())
	st, err := p.client.CallServerStream(ctx, r)
	if err != nil {
		p.breaker.settle(ctx, err)
		return err
	}
	defer st.Close()
	for st.Receive() {
		if err := each(st.Msg()); err != nil {
			p.breaker.record(nil)
			return err
		}
	}
	p.breaker.settle(ctx, st.Err())
	return st.Err()
}

// CallClientStream sends every message recv yields, up to its io.EOF, to a client-stream
// procedure and returns the single reply. A recv error other than io.EOF aborts the call and is
// returned as is.
func CallClientStream[Req, Res any](ctx context.Context, c *Conn, name string, recv func() (*Req, error)) (*Res, error) {
	p, err := open[Req, Res](c, name)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st := p.client.CallClientStream(ctx)
	c.opt.Header(ctx, st.RequestHeader())
	for {
		msg, err := recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			cancel()
			p.breaker.record(nil)
			return nil, err
		}
		if st.Send(msg) != nil {
			break
		}
	}
	res, err := st.CloseAndReceive()
	p.breaker.settle(ctx, err)
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// CallBidiStream feeds what recv yields to a bidi procedure while handing each reply to send.
// recv runs on its own goroutine and may be called once more after CallBidiStream returns, so
// it should return when ctx is done. The first error of the callee, recv or send is returned.
func CallBidiStream[Req, Res any](ctx context.Context, c *Conn, name string, recv func() (*Req, error), send func(*Res) error) error {
	p, err := open[Req, Res](c, name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st := p.client.CallBidiStream(ctx)
	c.opt.Header(ctx, st.RequestHeader())
	st.Send(nil)
	fed := make(chan error, 1)
	go func() {
		for {
			msg, err := recv()
			if errors.Is(err, io.EOF) {
				st.CloseRequest()
				return
			}
			if err != nil {
				fed <- err
				cancel()
				return
			}
			if st.Send(msg) != nil {
				return
			}
		}
	}()
	for {
		var msg *Res
		if msg, err = st.Receive(); err != nil {
			break
		}
		if err = send(msg); err != nil {
			cancel()
			st.CloseResponse()
			p.breaker.record(nil)
			return err
		}
	}
	st.CloseResponse()
	select {
	case ferr := <-fed:
		p.breaker.record(nil)
		return ferr
	default:
	}
	if errors.Is(err, io.EOF) {
		err = nil
	}
	p.breaker.settle(ctx, err)
	return err
}
