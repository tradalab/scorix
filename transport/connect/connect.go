// Package connect serves handlers over Connect, gRPC and gRPC-Web from one route,
// so a browser, a Connect client and an existing gRPC client reach the same handler.
package connect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/kernel"
	"github.com/tradalab/scorix/redact"
	"github.com/tradalab/scorix/transport/internal/gate"
)

// Options apply to every handler of a Mux. Auth and Timeout are required; NewMux fails without them.
type Options struct {
	Auth kernel.Authenticator
	// Timeout bounds each call a Rule sets no timeout for; a shorter caller deadline wins.
	Timeout time.Duration
	// Policy answers for every procedure whose Rule names a permission.
	Policy kernel.Policy
	// Wire turns errors that are neither a *connect.Error nor a *fault.Error into what the
	// caller receives; nil, or a code of 0, leaves the error as connect's CodeUnknown.
	Wire func(error) *connect.Error
	// Replay keeps outcomes for every procedure whose Rule sets Replay.
	Replay kernel.ReplayStore
	// Audit records every call to a procedure whose Rule sets Audit.
	Audit kernel.Auditor
	// Observer sees every call, audited or not; nil sees none.
	Observer kernel.Observer
	// ReadMaxBytes caps each request message; a larger one fails with CodeResourceExhausted.
	// Zero means the 4 MiB cap the REST and MCP transports use.
	ReadMaxBytes int
}

type Mux struct {
	opt    Options
	routes map[string]http.Handler
}

func NewMux(opt Options) (*Mux, error) {
	switch {
	case opt.Auth == nil:
		return nil, errors.New("transport/connect: Options.Auth is required")
	case opt.Timeout <= 0:
		return nil, errors.New("transport/connect: Options.Timeout is required")
	}
	if opt.ReadMaxBytes == 0 {
		opt.ReadMaxBytes = gate.MaxBody
	}
	return &Mux{opt: opt, routes: map[string]http.Handler{}}, nil
}

// Unary registers fn under procedure ("/pkg.Service/Method") with its contract rule. It panics
// when procedure is registered twice, or rule needs a Policy, Audit or Replay store the Mux lacks.
func Unary[Req, Res any](m *Mux, procedure string, rule kernel.Rule, fn func(context.Context, *Req) (*Res, error)) {
	m.claim(procedure, rule)
	m.routes[procedure] = connect.NewUnaryHandler(procedure, func(ctx context.Context, req *connect.Request[Req]) (*connect.Response[Res], error) {
		var out *Res
		err := m.serve(ctx, procedure, rule, req.Header(), req.Msg, func(ctx context.Context) (err error) {
			key := req.Header().Get("Idempotency-Key")
			if !rule.Replay || key == "" {
				out, err = fn(ctx, req.Msg)
				return err
			}
			out, err = Replay(ctx, m.opt.Replay, procedure, key, req.Msg, func(ctx context.Context, in *Req) (*Res, error) {
				res, err := fn(ctx, in)
				if err != nil {
					return nil, m.wire(err)
				}
				return res, nil
			})
			return err
		})
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(out), nil
	}, m.limit())
}

// ServerStream registers fn, which answers one request with any number of messages through
// send, under the same gates and rule as Unary. The rule's timeout bounds the whole stream;
// send fails once the caller has gone, and fn's error ends the stream with its code.
func ServerStream[Req, Res any](m *Mux, procedure string, rule kernel.Rule, fn func(ctx context.Context, req *Req, send func(*Res) error) error) {
	m.claim(procedure, rule)
	m.routes[procedure] = connect.NewServerStreamHandler(procedure, func(ctx context.Context, req *connect.Request[Req], st *connect.ServerStream[Res]) error {
		return m.serve(ctx, procedure, rule, req.Header(), req.Msg, func(ctx context.Context) error {
			return fn(ctx, req.Msg, st.Send)
		})
	}, m.limit())
}

// ClientStream registers fn, which reads any number of messages through recv and answers once,
// under the same gates and rule as Unary. recv returns io.EOF after the caller's last message.
func ClientStream[Req, Res any](m *Mux, procedure string, rule kernel.Rule, fn func(ctx context.Context, recv func() (*Req, error)) (*Res, error)) {
	m.claim(procedure, rule)
	m.routes[procedure] = connect.NewClientStreamHandler(procedure, func(ctx context.Context, st *connect.ClientStream[Req]) (*connect.Response[Res], error) {
		var out *Res
		err := m.serve(ctx, procedure, rule, st.RequestHeader(), nil, func(ctx context.Context) (err error) {
			out, err = fn(ctx, func() (*Req, error) {
				if st.Receive() {
					return st.Msg(), nil
				}
				if err := st.Err(); err != nil {
					return nil, err
				}
				return nil, io.EOF
			})
			return err
		})
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(out), nil
	}, m.limit())
}

// BidiStream registers fn, which reads through recv and writes through send in any order, under
// the same gates and rule as Unary. It needs HTTP/2 end to end; recv returns io.EOF after the
// caller's last message, and neither may be used once fn has returned.
func BidiStream[Req, Res any](m *Mux, procedure string, rule kernel.Rule, fn func(ctx context.Context, recv func() (*Req, error), send func(*Res) error) error) {
	m.claim(procedure, rule)
	m.routes[procedure] = connect.NewBidiStreamHandler(procedure, func(ctx context.Context, st *connect.BidiStream[Req, Res]) error {
		return m.serve(ctx, procedure, rule, st.RequestHeader(), nil, func(ctx context.Context) error {
			return fn(ctx, st.Receive, st.Send)
		})
	}, m.limit())
}

func (m *Mux) serve(ctx context.Context, procedure string, rule kernel.Rule, h http.Header, req any, run func(context.Context) error) (err error) {
	ctx, done := kernel.Observe(ctx, m.opt.Observer, "connect", kernel.Call{Procedure: procedure, Header: h})
	defer Settle(done, &err)
	start := time.Now()
	authed, cancel, err := m.enter(ctx, procedure, rule, h)
	defer cancel()
	if rule.Audit {
		defer Settle(func(err error) { m.audit(authed, ctx, procedure, start, req, err) }, &err)
	}
	if err != nil {
		return m.wire(err)
	}
	if err := run(authed); err != nil {
		return m.wire(err)
	}
	return nil
}

// Settle, deferred as Settle(done, &err), hands done the call's error, or a CodeInternal error
// when the call panics, and then lets the panic go on.
func Settle(done func(error), err *error) {
	if p := recover(); p != nil {
		done(connect.NewError(connect.CodeInternal, fmt.Errorf("the handler panicked: %v", p)))
		panic(p)
	}
	done(*err)
}

func (m *Mux) limit() connect.HandlerOption { return connect.WithReadMaxBytes(m.opt.ReadMaxBytes) }

func (m *Mux) claim(procedure string, rule kernel.Rule) {
	if _, dup := m.routes[procedure]; dup {
		panic("transport/connect: " + procedure + " registered twice")
	}
	if rule.Permission != "" && m.opt.Policy == nil {
		panic("transport/connect: " + procedure + " needs " + rule.Permission + " and the Mux has no Policy")
	}
	if rule.Audit && m.opt.Audit == nil {
		panic("transport/connect: " + procedure + " is audited and the Mux has no Audit")
	}
	if rule.Replay && m.opt.Replay == nil {
		panic("transport/connect: " + procedure + " replays an Idempotency-Key and the Mux has no Replay store")
	}
}

func (m *Mux) audit(authed, ctx context.Context, procedure string, start time.Time, req any, err error) {
	if authed == nil {
		authed = ctx
	}
	p, _ := kernel.PrincipalFrom(authed)
	var clean proto.Message
	if msg, ok := req.(proto.Message); ok {
		clean = redact.Log(msg)
	}
	m.opt.Audit(authed, kernel.Audit{Transport: "connect", Procedure: procedure, Principal: p, Request: clean, Duration: time.Since(start), Err: err})
}

func (m *Mux) enter(ctx context.Context, procedure string, rule kernel.Rule, h http.Header) (context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(ctx, rule.TimeoutOr(m.opt.Timeout))
	ctx, err := gate.Authenticate(ctx, m.opt.Auth, kernel.Call{Procedure: procedure, Header: h})
	if err != nil {
		return nil, cancel, err
	}
	p, _ := kernel.PrincipalFrom(ctx)
	if err := rule.Admit(ctx, p, m.opt.Policy); err != nil {
		return ctx, cancel, err
	}
	return ctx, cancel, nil
}

var faultCodes = map[string]connect.Code{
	fault.CodeInternal:    connect.CodeInternal,
	fault.CodeNotFound:    connect.CodeNotFound,
	fault.CodeDenied:      connect.CodePermissionDenied,
	fault.CodeCanceled:    connect.CodeCanceled,
	fault.CodeOverloaded:  connect.CodeResourceExhausted,
	fault.CodeUnavailable: connect.CodeUnavailable,
	fault.CodeInvalid:     connect.CodeInvalidArgument,
}

// Code is the code err reaches a caller with: a *connect.Error keeps its own, a *fault.Error
// maps onto the closest, a context error keeps its meaning, and anything else is CodeUnknown.
func Code(err error) connect.Code {
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		return ce.Code()
	}
	if code, ok := faultCodes[fault.CodeOf(err)]; ok {
		return code
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return connect.CodeDeadlineExceeded
	case errors.Is(err, context.Canceled):
		return connect.CodeCanceled
	}
	return connect.CodeUnknown
}

func (m *Mux) wire(err error) error {
	if _, ok := errors.AsType[*connect.Error](err); ok {
		return err
	}
	if code, ok := faultCodes[fault.CodeOf(err)]; ok {
		return connect.NewError(code, err)
	}
	if m.opt.Wire != nil {
		if ce := m.opt.Wire(err); ce != nil && ce.Code() != 0 {
			return ce
		}
	}
	return err
}

func (m *Mux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h, ok := m.routes[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.ServeHTTP(w, r)
}
