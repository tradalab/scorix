package connect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/tradalab/scorix/observe"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/kernel"
)

const echo = "/test.Echo/Say"

type ctxKey struct{}

var trustAll = kernel.AuthenticatorFunc(func(ctx context.Context, call kernel.Call) (context.Context, error) {
	ctx = context.WithValue(ctx, ctxKey{}, call.Header.Get("X-Who"))
	return kernel.WithPrincipal(ctx, kernel.Principal{Kind: kernel.User, ID: call.Header.Get("X-Who")}), nil
})

func mux(t *testing.T, opt Options) *Mux {
	t.Helper()
	if opt.Timeout == 0 {
		opt.Timeout = 5 * time.Second
	}
	m, err := NewMux(opt)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func listen(t *testing.T, opt Options, fn func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error)) string {
	t.Helper()
	m := mux(t, opt)
	Unary(m, echo, kernel.Rule{}, fn)
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return srv.URL
}

func serve(t *testing.T, opt Options, fn func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error)) *connect.Client[wrapperspb.StringValue, wrapperspb.StringValue] {
	t.Helper()
	return connect.NewClient[wrapperspb.StringValue, wrapperspb.StringValue](http.DefaultClient, listen(t, opt, fn)+echo, connect.WithProtoJSON())
}

func call(c *connect.Client[wrapperspb.StringValue, wrapperspb.StringValue], who, text string) (string, error) {
	req := connect.NewRequest(wrapperspb.String(text))
	req.Header().Set("X-Who", who)
	res, err := c.CallUnary(context.Background(), req)
	if err != nil {
		return "", err
	}
	return res.Msg.GetValue(), nil
}

func TestTheHandlerRunsWithTheContextTheAuthenticatorReturned(t *testing.T) {
	c := serve(t, Options{Auth: trustAll}, func(ctx context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		p, _ := kernel.PrincipalFrom(ctx)
		return wrapperspb.String(ctx.Value(ctxKey{}).(string) + "/" + p.ID + ": " + in.GetValue()), nil
	})
	got, err := call(c, "ana", "hi")
	if err != nil || got != "ana/ana: hi" {
		t.Fatalf("Say = %q, %v, want the authenticator's context in the handler", got, err)
	}
}

func ok(_ context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	return in, nil
}

func TestNewMuxRefusesAMissingAuthenticatorOrTimeout(t *testing.T) {
	for name, opt := range map[string]Options{
		"Options.Auth":    {Timeout: time.Second},
		"Options.Timeout": {Auth: trustAll},
	} {
		if _, err := NewMux(opt); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("NewMux without %s = %v, want it named", name, err)
		}
	}
}

func TestACallThatOutlivesTheTimeoutIsCutOff(t *testing.T) {
	c := serve(t, Options{Auth: trustAll, Timeout: 50 * time.Millisecond}, func(ctx context.Context, _ *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	start := time.Now()
	_, err := call(c, "ana", "hi")
	if connect.CodeOf(err) != connect.CodeDeadlineExceeded || time.Since(start) > 2*time.Second {
		t.Fatalf("err = %v after %v, want deadline_exceeded soon after 50ms", err, time.Since(start))
	}
}

func TestTheCallersShorterDeadlineWins(t *testing.T) {
	base := listen(t, Options{Auth: trustAll, Timeout: time.Minute}, func(ctx context.Context, _ *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		d, _ := ctx.Deadline()
		return wrapperspb.String(time.Until(d).String()), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	left, err := Call[wrapperspb.StringValue, wrapperspb.StringValue](ctx, Dial(base, ConnOptions{}), echo, wrapperspb.String("x"))
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := time.ParseDuration(left.GetValue()); d <= 0 || d > 2*time.Second {
		t.Fatalf("the handler had %s left, want the caller's 2s deadline", left.GetValue())
	}
}

func TestCallSendsTheHeaderHookAndReturnsTheCalleesCode(t *testing.T) {
	base := listen(t, Options{Auth: trustAll}, func(ctx context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		if in.GetValue() == "missing" {
			return nil, fault.New(fault.CodeNotFound, "no such thing")
		}
		p, _ := kernel.PrincipalFrom(ctx)
		return wrapperspb.String(p.ID), nil
	})
	conn := Dial(base+"/", ConnOptions{Header: func(_ context.Context, h http.Header) { h.Set("X-Who", "bob") }})
	for range 2 {
		got, err := Call[wrapperspb.StringValue, wrapperspb.StringValue](context.Background(), conn, echo, wrapperspb.String("x"))
		if err != nil || got.GetValue() != "bob" {
			t.Fatalf("Call = %v, %v, want the principal the header named", got, err)
		}
	}
	_, err := Call[wrapperspb.StringValue, wrapperspb.StringValue](context.Background(), conn, echo, wrapperspb.String("missing"))
	if connect.CodeOf(err) != connect.CodeNotFound || !strings.Contains(err.Error(), "no such thing") {
		t.Fatalf("err = %v, want the callee's not_found and message", err)
	}
}

func TestAnAuthenticatorThatNamesNoPrincipalIsRefused(t *testing.T) {
	silent := kernel.AuthenticatorFunc(func(ctx context.Context, _ kernel.Call) (context.Context, error) { return ctx, nil })
	_, err := call(serve(t, Options{Auth: silent}, ok), "ana", "hi")
	if connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), "without naming a principal") {
		t.Fatalf("err = %v, want the call refused for want of a principal", err)
	}
}

func TestAnAuthenticatorRefusalReachesTheCallerThroughTheCodeHook(t *testing.T) {
	refused := errors.New("bad token")
	deny := kernel.AuthenticatorFunc(func(context.Context, kernel.Call) (context.Context, error) { return nil, refused })
	hook := func(err error) *connect.Error {
		if errors.Is(err, refused) {
			return connect.NewError(connect.CodeUnauthenticated, err)
		}
		return nil
	}
	_, err := call(serve(t, Options{Auth: deny, Wire: hook}, ok), "ana", "hi")
	if connect.CodeOf(err) != connect.CodeUnauthenticated || !strings.Contains(err.Error(), "bad token") {
		t.Fatalf("err = %v, want the refusal as unauthenticated", err)
	}
}

func TestHandlerErrorsKeepTheirCode(t *testing.T) {
	foreign := errors.New("foreign")
	hook := func(err error) *connect.Error {
		if errors.Is(err, foreign) {
			return connect.NewError(connect.CodeAlreadyExists, errors.New("taken"))
		}
		return nil
	}
	for _, c := range []struct {
		err  error
		want connect.Code
	}{
		{fault.New(fault.CodeDenied, "no"), connect.CodePermissionDenied},
		{fault.New(fault.CodeNotFound, "no"), connect.CodeNotFound},
		{fault.New(fault.CodeOverloaded, "no"), connect.CodeResourceExhausted},
		{connect.NewError(connect.CodeAborted, errors.New("no")), connect.CodeAborted},
		{foreign, connect.CodeAlreadyExists},
		{errors.New("plain"), connect.CodeUnknown},
	} {
		_, err := call(serve(t, Options{Auth: trustAll, Wire: hook}, func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
			return nil, c.err
		}), "ana", "hi")
		if got := connect.CodeOf(err); got != c.want {
			t.Errorf("%v reached the caller as %v, want %v", c.err, got, c.want)
		}
		if c.err == foreign && !strings.HasSuffix(err.Error(), ": taken") {
			t.Errorf("the hook's message was replaced: %v", err)
		}
	}
}

func TestAGreedyCodeHookCannotRecodeAConnectError(t *testing.T) {
	greedy := func(err error) *connect.Error { return connect.NewError(connect.CodeInternal, err) }
	_, err := call(serve(t, Options{Auth: trustAll, Wire: greedy}, func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return nil, connect.NewError(connect.CodeAborted, errors.New("no"))
	}), "ana", "hi")
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("err = %v, want the handler's own aborted", err)
	}
}

func TestAPathNoProcedureClaimsIsNotFound(t *testing.T) {
	m := mux(t, Options{Auth: trustAll})
	Unary(m, echo, kernel.Rule{}, ok)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/test.Echo/Other", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestAProcedureRegisteredTwicePanics(t *testing.T) {
	m := mux(t, Options{Auth: trustAll})
	Unary(m, echo, kernel.Rule{}, ok)
	defer func() {
		if recover() == nil {
			t.Fatal("a second registration of the same procedure was accepted")
		}
	}()
	Unary(m, echo, kernel.Rule{}, ok)
}

func TestTheHandlersLogNamesTheProcedureAndThePrincipal(t *testing.T) {
	var buf bytes.Buffer
	log, _ := observe.NewLogger(&buf, "info")
	auth := kernel.AuthenticatorFunc(func(ctx context.Context, _ kernel.Call) (context.Context, error) {
		return kernel.WithPrincipal(observe.WithLogger(ctx, log), kernel.Principal{Kind: kernel.User, ID: "ana"}), nil
	})
	c := serve(t, Options{Auth: auth}, func(ctx context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		observe.Log(ctx).Info("handled")
		return in, nil
	})
	if _, err := call(c, "ana", "hi"); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); !strings.Contains(out, `"procedure":"/test.Echo/Say"`) || !strings.Contains(out, `"principal":"user:ana"`) {
		t.Fatalf("log = %s", out)
	}
}

var editorsOnly = kernel.PolicyFunc(func(_ context.Context, p kernel.Principal, perm string) error {
	if p.ID != "ed" {
		return errors.New(p.ID + " is not an editor")
	}
	return nil
})

func TestARulesPermissionIsCheckedForEveryCall(t *testing.T) {
	m := mux(t, Options{Auth: trustAll, Policy: editorsOnly})
	Unary(m, echo, kernel.Rule{Permission: "things.write"}, ok)
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	c := connect.NewClient[wrapperspb.StringValue, wrapperspb.StringValue](http.DefaultClient, srv.URL+echo, connect.WithProtoJSON())
	if _, err := call(c, "ed", "hi"); err != nil {
		t.Fatalf("the editor was refused: %v", err)
	}
	if _, err := call(c, "ana", "hi"); connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "needs things.write") {
		t.Fatalf("err = %v, want permission_denied naming the permission", err)
	}
}

func TestARulesTimeoutReplacesTheMuxes(t *testing.T) {
	m := mux(t, Options{Auth: trustAll, Timeout: time.Minute})
	Unary(m, echo, kernel.Rule{Timeout: 50 * time.Millisecond}, func(ctx context.Context, _ *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	c := connect.NewClient[wrapperspb.StringValue, wrapperspb.StringValue](http.DefaultClient, srv.URL+echo, connect.WithProtoJSON())
	start := time.Now()
	if _, err := call(c, "ana", "hi"); connect.CodeOf(err) != connect.CodeDeadlineExceeded || time.Since(start) > 2*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestAPermissionWithNoPolicyPanicsAtRegistration(t *testing.T) {
	m := mux(t, Options{Auth: trustAll})
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "has no Policy") {
			t.Fatalf("recover = %v", r)
		}
	}()
	Unary(m, echo, kernel.Rule{Permission: "x"}, ok)
}

func TestAContextErrorFromAHandlerKeepsItsMeaning(t *testing.T) {
	for err, want := range map[error]connect.Code{
		fmt.Errorf("query: %w", context.DeadlineExceeded): connect.CodeDeadlineExceeded,
		fmt.Errorf("query: %w", context.Canceled):         connect.CodeCanceled,
		errors.New("boom"): connect.CodeUnknown,
	} {
		if got := Code(err); got != want {
			t.Errorf("Code(%v) = %v, want %v", err, got, want)
		}
	}
}

func TestAMessageOverTheReadCapIsRefusedByEveryHandlerKind(t *testing.T) {
	m := mux(t, Options{Auth: trustAll, ReadMaxBytes: 64})
	Unary(m, echo, kernel.Rule{}, ok)
	ServerStream(m, count, kernel.Rule{}, letters)
	ClientStream(m, join, kernel.Rule{}, joining)
	BidiStream(m, chat, kernel.Rule{}, upper)
	conn := dialed(tlsServer(t, m), false)
	big := strings.Repeat("x", 200)
	ctx := context.Background()
	_, unary := Call[msg, msg](ctx, conn, echo, wrapperspb.String(big))
	server := Stream(ctx, conn, count, wrapperspb.String(big), func(*msg) error { return nil })
	_, client := CallClientStream[msg, msg](ctx, conn, join, feed(big))
	bidi := CallBidiStream(ctx, conn, chat, feed(big), func(*msg) error { return nil })
	for name, err := range map[string]error{"unary": unary, "server": server, "client": client, "bidi": bidi} {
		if connect.CodeOf(err) != connect.CodeResourceExhausted {
			t.Errorf("%s: %v, want resource_exhausted", name, err)
		}
	}
	if _, err := Call[msg, msg](ctx, conn, echo, wrapperspb.String("small")); err != nil {
		t.Fatalf("a small message: %v", err)
	}
}

func TestTheReadCapDefaultsToFourMebibytes(t *testing.T) {
	c := serve(t, Options{Auth: trustAll}, ok)
	if _, err := call(c, "ana", strings.Repeat("x", 3<<20)); err != nil {
		t.Fatalf("3 MiB: %v", err)
	}
	if _, err := call(c, "ana", strings.Repeat("x", 5<<20)); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("5 MiB: %v, want resource_exhausted", err)
	}
}
