// Package rest serves unary handlers as JSON REST routes, binding the path, query, headers
// and body of a request into one protobuf message the way the (scorix.v1.http) option says.
package rest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/tradalab/scorix/kernel"
	"github.com/tradalab/scorix/redact"
	scorixconnect "github.com/tradalab/scorix/transport/connect"
	"github.com/tradalab/scorix/transport/internal/gate"
)

// Options apply to every route of a Mux. Auth and Timeout are required.
type Options struct {
	Auth    kernel.Authenticator
	Timeout time.Duration
	// Policy answers for every route whose Rule names a permission.
	Policy kernel.Policy
	// WriteError answers a call that failed or was refused; nil writes
	// {"code": ..., "message": ...} with the HTTP status of the code.
	WriteError func(w http.ResponseWriter, r *http.Request, err error)
	// Replay keeps outcomes for every route whose Rule sets Replay; share the Connect Mux's
	// store so a retry reaches the same outcome whichever transport it takes.
	Replay kernel.ReplayStore
	// Audit records every call to a route whose Rule sets Audit.
	Audit kernel.Auditor
	// Observer sees every call, audited or not; nil sees none.
	Observer kernel.Observer
}

type Mux struct {
	opt Options
	mux *http.ServeMux
}

func NewMux(opt Options) (*Mux, error) {
	switch {
	case opt.Auth == nil:
		return nil, errors.New("transport/rest: Options.Auth is required")
	case opt.Timeout <= 0:
		return nil, errors.New("transport/rest: Options.Timeout is required")
	}
	if opt.WriteError == nil {
		opt.WriteError = writeError
	}
	return &Mux{opt: opt, mux: http.NewServeMux()}, nil
}

// Route is one REST binding, as generated from an rpc's (scorix.v1.http) option.
type Route struct {
	Method    string
	Path      string
	Procedure string
	// Body "*" decodes the request body into the message; empty decodes the query string.
	Body      string
	Anonymous bool
	// Headers maps a field name to the request header that fills it.
	Headers map[string]string
	Rule    kernel.Rule
}

// Handle serves fn at r. It panics when the same method and path are handled twice, or when
// r.Rule needs a Policy, Audit or Replay store the Mux lacks.
func Handle[Req, Res any, PReq interface {
	*Req
	proto.Message
}, PRes interface {
	*Res
	proto.Message
}](m *Mux, r Route, fn func(context.Context, *Req) (*Res, error)) {
	if r.Rule.Permission != "" && m.opt.Policy == nil {
		panic("transport/rest: " + r.Method + " " + r.Path + " needs " + r.Rule.Permission + " and the Mux has no Policy")
	}
	if r.Rule.Audit && m.opt.Audit == nil {
		panic("transport/rest: " + r.Method + " " + r.Path + " is audited and the Mux has no Audit")
	}
	if r.Rule.Replay && m.opt.Replay == nil {
		panic("transport/rest: " + r.Method + " " + r.Path + " replays an Idempotency-Key and the Mux has no Replay store")
	}
	m.mux.HandleFunc(r.Method+" "+r.Path, func(w http.ResponseWriter, hr *http.Request) {
		var err error
		ctx, done := kernel.Observe(hr.Context(), m.opt.Observer, "rest", kernel.Call{Procedure: r.Procedure, Header: hr.Header, Anonymous: r.Anonymous})
		defer scorixconnect.Settle(done, &err)
		start := time.Now()
		ctx, cancel := context.WithTimeout(ctx, r.Rule.TimeoutOr(m.opt.Timeout))
		defer cancel()
		var req PReq
		if r.Rule.Audit {
			defer func() {
				p, _ := kernel.PrincipalFrom(ctx)
				var clean proto.Message
				if req != nil {
					clean = redact.Log(req)
				}
				m.opt.Audit(ctx, kernel.Audit{Transport: "rest", Procedure: r.Procedure, Principal: p, Request: clean, Duration: time.Since(start), Err: err})
			}()
		}
		authed, err := gate.Authenticate(ctx, m.opt.Auth, kernel.Call{Procedure: r.Procedure, Header: hr.Header, Anonymous: r.Anonymous})
		if err != nil {
			m.opt.WriteError(w, hr, err)
			return
		}
		ctx = authed
		p, _ := kernel.PrincipalFrom(ctx)
		if err = r.Rule.Admit(ctx, p, m.opt.Policy); err != nil {
			m.opt.WriteError(w, hr, err)
			return
		}
		req = PReq(new(Req))
		if err = bind(req, r, hr); err != nil {
			err = connect.NewError(connect.CodeInvalidArgument, err)
			m.opt.WriteError(w, hr, err)
			return
		}
		run := fn
		if key := hr.Header.Get("Idempotency-Key"); r.Rule.Replay && key != "" {
			run = func(ctx context.Context, in *Req) (*Res, error) {
				return scorixconnect.Replay(ctx, m.opt.Replay, r.Procedure, key, in, fn)
			}
		}
		var res *Res
		res, err = run(ctx, (*Req)(req))
		if err != nil {
			m.opt.WriteError(w, hr, err)
			return
		}
		body, err := Marshal(PRes(res))
		if err != nil {
			m.opt.WriteError(w, hr, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(body)
	})
}

func (m *Mux) ServeHTTP(w http.ResponseWriter, r *http.Request) { m.mux.ServeHTTP(w, r) }

func bind(msg proto.Message, r Route, hr *http.Request) error {
	if r.Body == "*" {
		raw, err := gate.ReadBody(hr.Body)
		if err != nil {
			return err
		}
		if len(raw) > 0 {
			if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, msg); err != nil {
				return err
			}
		}
	}
	fields := msg.ProtoReflect().Descriptor().Fields()
	if r.Body == "" {
		query := hr.URL.Query()
		for key, values := range query {
			f := fields.ByJSONName(key)
			if f == nil {
				f = fields.ByName(protoreflect.Name(key))
				if f != nil && query.Has(f.JSONName()) {
					continue
				}
			}
			if f == nil {
				continue
			}
			if err := set(msg, f, values); err != nil {
				return err
			}
		}
	}
	for name, header := range r.Headers {
		f := fields.ByName(protoreflect.Name(name))
		msg.ProtoReflect().Clear(f)
		if v := hr.Header.Get(header); v != "" {
			if err := set(msg, f, []string{v}); err != nil {
				return err
			}
		}
	}
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if v := hr.PathValue(string(f.Name())); v != "" {
			if err := set(msg, f, []string{v}); err != nil {
				return err
			}
		}
	}
	return nil
}

func set(msg proto.Message, f protoreflect.FieldDescriptor, values []string) error {
	m := msg.ProtoReflect()
	if f.Cardinality() == protoreflect.Repeated {
		list := m.Mutable(f).List()
		for _, s := range values {
			v, err := scalar(f, s)
			if err != nil {
				return err
			}
			list.Append(v)
		}
		return nil
	}
	v, err := scalar(f, values[0])
	if err != nil {
		return err
	}
	m.Set(f, v)
	return nil
}

func scalar(f protoreflect.FieldDescriptor, s string) (protoreflect.Value, error) {
	bad := func() (protoreflect.Value, error) {
		return protoreflect.Value{}, fmt.Errorf("%s: %q is not a %s", f.JSONName(), s, f.Kind())
	}
	switch f.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(s), nil
	case protoreflect.BoolKind:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return bad()
		}
		return protoreflect.ValueOfBool(b), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return bad()
		}
		return protoreflect.ValueOfInt32(int32(n)), nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return bad()
		}
		return protoreflect.ValueOfInt64(n), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		n, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return bad()
		}
		return protoreflect.ValueOfUint32(uint32(n)), nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return bad()
		}
		return protoreflect.ValueOfUint64(n), nil
	case protoreflect.FloatKind:
		n, err := strconv.ParseFloat(s, 32)
		if err != nil {
			return bad()
		}
		return protoreflect.ValueOfFloat32(float32(n)), nil
	case protoreflect.DoubleKind:
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return bad()
		}
		return protoreflect.ValueOfFloat64(n), nil
	case protoreflect.BytesKind:
		s = strings.TrimRight(strings.ReplaceAll(s, " ", "+"), "=")
		enc := base64.RawStdEncoding
		if strings.ContainsAny(s, "-_") {
			enc = base64.RawURLEncoding
		}
		b, err := enc.DecodeString(s)
		if err != nil {
			return bad()
		}
		return protoreflect.ValueOfBytes(b), nil
	case protoreflect.EnumKind:
		if v := f.Enum().Values().ByName(protoreflect.Name(s)); v != nil {
			return protoreflect.ValueOfEnum(v.Number()), nil
		}
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return bad()
		}
		return protoreflect.ValueOfEnum(protoreflect.EnumNumber(n)), nil
	}
	return protoreflect.Value{}, fmt.Errorf("%s: a %s is not read from a path, query or header", f.JSONName(), f.Kind())
}

var statusOf = map[connect.Code]int{
	connect.CodeCanceled:           499,
	connect.CodeUnknown:            http.StatusInternalServerError,
	connect.CodeInvalidArgument:    http.StatusBadRequest,
	connect.CodeDeadlineExceeded:   http.StatusGatewayTimeout,
	connect.CodeNotFound:           http.StatusNotFound,
	connect.CodeAlreadyExists:      http.StatusConflict,
	connect.CodePermissionDenied:   http.StatusForbidden,
	connect.CodeResourceExhausted:  http.StatusTooManyRequests,
	connect.CodeFailedPrecondition: http.StatusBadRequest,
	connect.CodeAborted:            http.StatusConflict,
	connect.CodeOutOfRange:         http.StatusBadRequest,
	connect.CodeUnimplemented:      http.StatusNotImplemented,
	connect.CodeInternal:           http.StatusInternalServerError,
	connect.CodeUnavailable:        http.StatusServiceUnavailable,
	connect.CodeDataLoss:           http.StatusInternalServerError,
	connect.CodeUnauthenticated:    http.StatusUnauthorized,
}

// Status is the HTTP status a call failing with err answers: the one err names through an
// HTTPStatus() int method, else the one its code maps to.
func Status(err error) int {
	if s, ok := errors.AsType[interface {
		error
		HTTPStatus() int
	}](err); ok {
		return s.HTTPStatus()
	}
	return statusOf[scorixconnect.Code(err)]
}

// Message is err's text without the code prefix a *connect.Error prints.
func Message(err error) string { return gate.Message(err) }

func writeError(w http.ResponseWriter, _ *http.Request, err error) {
	body, _ := json.Marshal(map[string]string{"code": scorixconnect.Code(err).String(), "message": Message(err)})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(Status(err))
	w.Write(body)
}
