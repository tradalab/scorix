package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tradalab/scorix/observe"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/kernel"
	"github.com/tradalab/scorix/transport/internal/gate"
	"github.com/tradalab/scorix/transport/internal/testpb"
)

var anyone = kernel.AuthenticatorFunc(func(ctx context.Context, _ kernel.Call) (context.Context, error) {
	return kernel.WithPrincipal(ctx, kernel.Principal{Kind: kernel.Anonymous}), nil
})

func echo(_ context.Context, in *testpb.Thing) (*testpb.Thing, error) { return in, nil }

func serve(t *testing.T, opt Options, r Route, fn func(context.Context, *testpb.Thing) (*testpb.Thing, error)) *httptest.Server {
	t.Helper()
	if opt.Auth == nil {
		opt.Auth = anyone
	}
	if opt.Timeout == 0 {
		opt.Timeout = 5 * time.Second
	}
	m, err := NewMux(opt)
	if err != nil {
		t.Fatal(err)
	}
	Handle(m, r, fn)
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, srv *httptest.Server, method, path, body string, header http.Header) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	for k, v := range header {
		req.Header[k] = v
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func thing(t *testing.T, body string) *testpb.Thing {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	out := &testpb.Thing{Id: raw["id"].(string), Page: int32(raw["page"].(float64)), UserAgent: raw["userAgent"].(string)}
	for _, tag := range raw["tags"].([]any) {
		out.Tags = append(out.Tags, tag.(string))
	}
	return out
}

func TestABodyRouteDecodesTheBodyAndThePathWins(t *testing.T) {
	srv := serve(t, Options{}, Route{Method: "POST", Path: "/things/{id}", Body: "*"}, echo)
	code, body := do(t, srv, "POST", "/things/p1", `{"id":"from-body","page":2,"tags":["a"],"unknown":true}`, nil)
	if got := thing(t, body); code != 200 || !proto.Equal(got, &testpb.Thing{Id: "p1", Page: 2, Tags: []string{"a"}}) {
		t.Fatalf("%d %s", code, body)
	}
}

func TestAQueryRouteReadsJSONAndProtoNamesAndRepeats(t *testing.T) {
	srv := serve(t, Options{}, Route{Method: "GET", Path: "/things"}, echo)
	code, body := do(t, srv, "GET", "/things?page=3&tags=a&tags=b&userAgent=ua&id=q&ignored=1", "", nil)
	if got := thing(t, body); code != 200 || !proto.Equal(got, &testpb.Thing{Id: "q", Page: 3, Tags: []string{"a", "b"}, UserAgent: "ua"}) {
		t.Fatalf("%d %s", code, body)
	}
	_, body = do(t, srv, "GET", "/things?user_agent=snake", "", nil)
	if thing(t, body).UserAgent != "snake" {
		t.Fatalf("the proto field name was not read: %s", body)
	}
}

func TestABodyRouteIgnoresTheQuery(t *testing.T) {
	srv := serve(t, Options{}, Route{Method: "POST", Path: "/things", Body: "*"}, echo)
	_, body := do(t, srv, "POST", "/things?page=9", `{"page":1}`, nil)
	if thing(t, body).Page != 1 {
		t.Fatalf("the query overrode the body: %s", body)
	}
}

func TestAHeaderFillsItsField(t *testing.T) {
	srv := serve(t, Options{}, Route{Method: "GET", Path: "/things", Headers: map[string]string{"user_agent": "User-Agent"}}, echo)
	_, body := do(t, srv, "GET", "/things", "", http.Header{"User-Agent": {"probe/1"}})
	if thing(t, body).UserAgent != "probe/1" {
		t.Fatalf("header not bound: %s", body)
	}
}

func TestAHeaderFieldComesOnlyFromItsHeaderNeverTheQueryOrBody(t *testing.T) {
	headers := map[string]string{"user_agent": "User-Agent"}
	get := serve(t, Options{}, Route{Method: "GET", Path: "/things", Headers: headers}, echo)
	post := serve(t, Options{}, Route{Method: "POST", Path: "/things", Body: "*", Headers: headers}, echo)
	noAgent := http.Header{"User-Agent": {""}}
	for name, body := range map[string]string{
		"query": func() string {
			_, b := do(t, get, "GET", "/things?userAgent=spoof&user_agent=spoof", "", noAgent)
			return b
		}(),
		"body": func() string { _, b := do(t, post, "POST", "/things", `{"userAgent":"spoof"}`, noAgent); return b }(),
		"body+head": func() string {
			_, b := do(t, post, "POST", "/things", `{"userAgent":"spoof"}`, http.Header{"User-Agent": {"real"}})
			return b
		}(),
	} {
		want := ""
		if name == "body+head" {
			want = "real"
		}
		if got := thing(t, body).UserAgent; got != want {
			t.Errorf("%s: user_agent = %q, want %q", name, got, want)
		}
	}
}

func TestInputThatDoesNotFitIsInvalidArgument(t *testing.T) {
	get := serve(t, Options{}, Route{Method: "GET", Path: "/things"}, echo)
	post := serve(t, Options{}, Route{Method: "POST", Path: "/things", Body: "*"}, echo)
	for name, c := range map[string]struct {
		srv          *httptest.Server
		method, path string
		body         string
	}{
		"int":       {get, "GET", "/things?page=abc", ""},
		"bool":      {get, "GET", "/things?active=maybe", ""},
		"message":   {get, "GET", "/things?child=x", ""},
		"json":      {post, "POST", "/things", `{"page":`},
		"oversized": {post, "POST", "/things", `{"id":"` + strings.Repeat("x", gate.MaxBody) + `"}`},
	} {
		code, body := do(t, c.srv, c.method, c.path, c.body, nil)
		if code != 400 || !strings.Contains(body, `"code":"invalid_argument"`) {
			t.Errorf("%s: %d %s, want 400 invalid_argument", name, code, body)
		}
		if name == "oversized" && !strings.Contains(body, "the request body is over") {
			t.Errorf("oversized: %s, want the size named", body)
		}
	}
}

func TestAQueryRouteIgnoresTheBodyAndTheFirstValueWins(t *testing.T) {
	srv := serve(t, Options{}, Route{Method: "GET", Path: "/things"}, echo)
	_, body := do(t, srv, "GET", "/things?page=1&page=2", `{"id":"from-body"}`, nil)
	if got := thing(t, body); got.Id != "" || got.Page != 1 {
		t.Fatalf("%s, want the body ignored and page=1", body)
	}
}

func TestTheHandlerRunsWithTheAuthenticatorsPrincipal(t *testing.T) {
	ana := kernel.AuthenticatorFunc(func(ctx context.Context, _ kernel.Call) (context.Context, error) {
		return kernel.WithPrincipal(ctx, kernel.Principal{Kind: kernel.User, ID: "ana"}), nil
	})
	srv := serve(t, Options{Auth: ana}, Route{Method: "GET", Path: "/me"}, func(ctx context.Context, _ *testpb.Thing) (*testpb.Thing, error) {
		p, _ := kernel.PrincipalFrom(ctx)
		return &testpb.Thing{Id: p.ID}, nil
	})
	req, _ := http.NewRequest("GET", srv.URL+"/me", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if thing(t, string(b)).Id != "ana" || res.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("%s %q, want ana as JSON", b, res.Header.Get("Content-Type"))
	}
}

func TestEveryScalarKindIsReadFromTheQuery(t *testing.T) {
	var got *testpb.Thing
	srv := serve(t, Options{}, Route{Method: "GET", Path: "/things"}, func(_ context.Context, in *testpb.Thing) (*testpb.Thing, error) {
		got = in
		return in, nil
	})
	code, body := do(t, srv, "GET", "/things?views=-9000000000&active=true&score=1.5&blob=aGk%3D&big=18000000000000000000", "", nil)
	want := &testpb.Thing{Views: -9000000000, Active: true, Score: 1.5, Blob: []byte("hi"), Big: 18000000000000000000}
	if code != 200 || !proto.Equal(got, want) {
		t.Fatalf("%d %s: bound %v", code, body, got)
	}
}

func TestTheResponseHasEveryFieldAndInt64AsANumber(t *testing.T) {
	got, err := Marshal(&testpb.Thing{Views: 9007199254740991, Big: 18000000000000000000, Children: []*testpb.Nested{{Name: "n", Count: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"","page":0,"views":9007199254740991,"active":false,"tags":[],"score":0,"child":{"name":"","count":0,"parent":null},"children":[{"name":"n","count":2,"parent":null}],"blob":"","userAgent":"","big":18000000000000000000,"color":"COLOR_UNSPECIFIED","counts":{},"byId":{},"nick":null,"rank":null,"best":null}`
	if string(got) != want {
		t.Fatalf("Marshal =\n%s\nwant\n%s", got, want)
	}
}

func TestAnOptionalFieldSetToItsZeroIsWrittenAndOnlyAnUnsetOneIsNull(t *testing.T) {
	nick, rank := "", int64(0)
	got, err := Marshal(&testpb.Thing{Nick: &nick, Rank: &rank, Best: &testpb.Nested{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(got), `"nick":"","rank":0,"best":{"name":"","count":0,"parent":null}}`) {
		t.Fatalf("Marshal = %s", got)
	}
}

func TestAnOptionalFieldLeftOutOrSentAsNullStaysUnset(t *testing.T) {
	var got *testpb.Thing
	srv := serve(t, Options{}, Route{Method: "POST", Path: "/things", Body: "*"}, func(_ context.Context, in *testpb.Thing) (*testpb.Thing, error) {
		got = in
		return in, nil
	})
	for body, want := range map[string]string{
		`{}`:                                        "nick=<nil> rank=<nil> best=false",
		`{"nick":null,"rank":null,"best":null}`:     "nick=<nil> rank=<nil> best=false",
		`{"nick":"","rank":"0","best":{}}`:          `nick="" rank=0 best=true`,
		`{"nick":"x","rank":7,"best":{"name":"b"}}`: `nick="x" rank=7 best=true`,
	} {
		res, err := http.Post(srv.URL+"/things", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		nick, rank := "<nil>", "<nil>"
		if got.Nick != nil {
			nick = fmt.Sprintf("%q", *got.Nick)
		}
		if got.Rank != nil {
			rank = fmt.Sprint(*got.Rank)
		}
		if s := fmt.Sprintf("nick=%s rank=%s best=%v", nick, rank, got.Best != nil); s != want {
			t.Errorf("%s bound %s, want %s", body, s, want)
		}
	}
}

func TestASetRecursiveFieldIsWrittenAndOnlyAnUnsetOneStopsTheWalk(t *testing.T) {
	got, err := Marshal(&testpb.Nested{Name: "leaf", Parent: &testpb.Nested{Name: "mid", Parent: &testpb.Nested{Name: "root"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"name":"leaf","count":0,"parent":{"name":"mid","count":0,"parent":{"name":"root","count":0,"parent":null}}}`
	if string(got) != want {
		t.Fatalf("Marshal =\n%s\nwant\n%s", got, want)
	}
}

func TestNewMuxRefusesAMissingAuthenticatorOrTimeout(t *testing.T) {
	for name, opt := range map[string]Options{"Options.Auth": {Timeout: time.Second}, "Options.Timeout": {Auth: anyone}} {
		if _, err := NewMux(opt); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("NewMux without %s = %v", name, err)
		}
	}
}

func TestTheAuthenticatorLearnsWhetherTheRouteIsAnonymous(t *testing.T) {
	var seen []bool
	auth := kernel.AuthenticatorFunc(func(ctx context.Context, c kernel.Call) (context.Context, error) {
		seen = append(seen, c.Anonymous)
		if c.Procedure != "/acme.S/Do" {
			t.Errorf("procedure = %q", c.Procedure)
		}
		return kernel.WithPrincipal(ctx, kernel.Principal{Kind: kernel.Anonymous}), nil
	})
	for _, anon := range []bool{true, false} {
		do(t, serve(t, Options{Auth: auth}, Route{Method: "GET", Path: "/x", Procedure: "/acme.S/Do", Anonymous: anon}, echo), "GET", "/x", "", nil)
	}
	if len(seen) != 2 || !seen[0] || seen[1] {
		t.Fatalf("Anonymous seen = %v, want [true false]", seen)
	}
}

func TestACallerTheAuthenticatorRefusesNeverReachesTheHandler(t *testing.T) {
	deny := kernel.AuthenticatorFunc(func(context.Context, kernel.Call) (context.Context, error) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("who are you"))
	})
	silent := kernel.AuthenticatorFunc(func(ctx context.Context, _ kernel.Call) (context.Context, error) { return ctx, nil })
	reached := false
	h := func(context.Context, *testpb.Thing) (*testpb.Thing, error) {
		reached = true
		return &testpb.Thing{}, nil
	}
	if code, body := do(t, serve(t, Options{Auth: deny}, Route{Method: "GET", Path: "/x"}, h), "GET", "/x", "", nil); code != 401 || !strings.Contains(body, "who are you") {
		t.Errorf("refused: %d %s", code, body)
	}
	if code, body := do(t, serve(t, Options{Auth: silent}, Route{Method: "GET", Path: "/x"}, h), "GET", "/x", "", nil); code != 500 || !strings.Contains(body, "without naming a principal") {
		t.Errorf("no principal: %d %s", code, body)
	}
	if reached {
		t.Fatal("the handler ran for a caller that was not authenticated")
	}
}

func TestErrorsKeepTheirStatusAndMessage(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		body   string
	}{
		{connect.NewError(connect.CodeNotFound, errors.New("gone")), 404, `{"code":"not_found","message":"gone"}`},
		{fault.New(fault.CodeDenied, "no"), 403, `{"code":"permission_denied","message":"no"}`},
		{connect.NewError(connect.CodeDeadlineExceeded, errors.New("slow")), 504, `{"code":"deadline_exceeded","message":"slow"}`},
		{errors.New("plain"), 500, `{"code":"unknown","message":"plain"}`},
		{fmt.Errorf("wrapped: %w", statusErr{418}), 418, `{"code":"unknown","message":"wrapped: teapot"}`},
	} {
		srv := serve(t, Options{}, Route{Method: "GET", Path: "/x"}, func(context.Context, *testpb.Thing) (*testpb.Thing, error) { return nil, c.err })
		if code, body := do(t, srv, "GET", "/x", "", nil); code != c.status || body != c.body {
			t.Errorf("%v: %d %s, want %d %s", c.err, code, body, c.status, c.body)
		}
	}
}

type statusErr struct{ status int }

func (e statusErr) Error() string   { return "teapot" }
func (e statusErr) HTTPStatus() int { return e.status }

func TestWriteErrorReplacesTheErrorBody(t *testing.T) {
	custom := func(w http.ResponseWriter, _ *http.Request, err error) {
		w.WriteHeader(Status(err))
		w.Write([]byte(`{"error":"` + Message(err) + `"}`))
	}
	srv := serve(t, Options{WriteError: custom}, Route{Method: "GET", Path: "/x"}, func(context.Context, *testpb.Thing) (*testpb.Thing, error) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("not found"))
	})
	if code, body := do(t, srv, "GET", "/x", "", nil); code != 404 || body != `{"error":"not found"}` {
		t.Fatalf("%d %s", code, body)
	}
}

func TestACallThatOutlivesTheTimeoutIsCutOff(t *testing.T) {
	srv := serve(t, Options{Timeout: 50 * time.Millisecond}, Route{Method: "GET", Path: "/x"}, func(ctx context.Context, _ *testpb.Thing) (*testpb.Thing, error) {
		<-ctx.Done()
		return nil, fmt.Errorf("query links: %w", ctx.Err())
	})
	start := time.Now()
	if code, _ := do(t, srv, "GET", "/x", "", nil); code != 504 || time.Since(start) > 2*time.Second {
		t.Fatalf("%d after %v", code, time.Since(start))
	}
}

func TestTheHandlersLogNamesTheProcedureAndThePrincipal(t *testing.T) {
	var buf bytes.Buffer
	log, _ := observe.NewLogger(&buf, "info")
	auth := kernel.AuthenticatorFunc(func(ctx context.Context, _ kernel.Call) (context.Context, error) {
		return kernel.WithPrincipal(observe.WithLogger(ctx, log), kernel.Principal{Kind: kernel.User, ID: "ana"}), nil
	})
	srv := serve(t, Options{Auth: auth}, Route{Method: "GET", Path: "/x", Procedure: "/acme.S/Do"}, func(ctx context.Context, in *testpb.Thing) (*testpb.Thing, error) {
		observe.Log(ctx).Info("handled")
		return in, nil
	})
	do(t, srv, "GET", "/x", "", nil)
	if out := buf.String(); !strings.Contains(out, `"procedure":"/acme.S/Do"`) || !strings.Contains(out, `"principal":"user:ana"`) {
		t.Fatalf("log = %s", out)
	}
}

func TestARoutesRuleChecksThePermissionAndBoundsTheCall(t *testing.T) {
	pol := kernel.PolicyFunc(func(context.Context, kernel.Principal, string) error { return errors.New("no") })
	srv := serve(t, Options{Policy: pol}, Route{Method: "GET", Path: "/x", Rule: kernel.Rule{Permission: "things.read"}}, echo)
	if code, body := do(t, srv, "GET", "/x", "", nil); code != 403 || !strings.Contains(body, "needs things.read") {
		t.Errorf("denied: %d %s", code, body)
	}
	slow := serve(t, Options{Timeout: time.Minute}, Route{Method: "GET", Path: "/x", Rule: kernel.Rule{Timeout: 50 * time.Millisecond}}, func(ctx context.Context, _ *testpb.Thing) (*testpb.Thing, error) {
		<-ctx.Done()
		return nil, connect.NewError(connect.CodeDeadlineExceeded, ctx.Err())
	})
	start := time.Now()
	if code, _ := do(t, slow, "GET", "/x", "", nil); code != 504 || time.Since(start) > 2*time.Second {
		t.Errorf("rule timeout: %d after %v", code, time.Since(start))
	}
	m, _ := NewMux(Options{Auth: anyone, Timeout: time.Second})
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "has no Policy") {
			t.Fatalf("recover = %v", r)
		}
	}()
	Handle(m, Route{Method: "GET", Path: "/y", Rule: kernel.Rule{Permission: "x"}}, echo)
}

func TestARetriedPostWithItsIdempotencyKeyGetsTheFirstReply(t *testing.T) {
	runs := 0
	count := func(_ context.Context, in *testpb.Thing) (*testpb.Thing, error) {
		runs++
		in.Views = int64(runs)
		return in, nil
	}
	route := Route{Method: "POST", Path: "/things", Procedure: "/test.Things/Make", Body: "*", Rule: kernel.Rule{Replay: true}}
	store, err := kernel.NewMemoryReplay(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv := serve(t, Options{Replay: store}, route, count)
	key := http.Header{"Idempotency-Key": {"k1"}}
	_, first := do(t, srv, "POST", "/things", `{"id":"a"}`, key)
	_, again := do(t, srv, "POST", "/things", `{"id":"a"}`, key)
	if first != again || runs != 1 || !strings.Contains(first, `"views":1`) {
		t.Fatalf("first %s, retry %s, %d runs", first, again, runs)
	}
	if status, body := do(t, srv, "POST", "/things", `{"id":"b"}`, key); status != http.StatusBadRequest || !strings.Contains(body, "different request") {
		t.Fatalf("reused key = %d %s", status, body)
	}
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "has no Replay store") {
			t.Fatalf("panic = %v", r)
		}
	}()
	m, _ := NewMux(Options{Auth: anyone, Timeout: time.Second})
	Handle(m, route, count)
}

func TestAnAuditedRouteRecordsTheCallAndItsRefusals(t *testing.T) {
	var seen []kernel.Audit
	record := func(_ context.Context, a kernel.Audit) { seen = append(seen, a) }
	route := Route{Method: "POST", Path: "/things/{id}", Procedure: "/test.Things/Save", Body: "*", Rule: kernel.Rule{Audit: true}}
	srv := serve(t, Options{Audit: record}, route, echo)
	do(t, srv, "POST", "/things/a", `{"page":2}`, nil)
	do(t, srv, "POST", "/things/a", `{"page":"many"}`, nil)
	if len(seen) != 2 {
		t.Fatalf("audited %d calls, want 2", len(seen))
	}
	thing, _ := seen[0].Request.(*testpb.Thing)
	if seen[0].Transport != "rest" || seen[0].Procedure != "/test.Things/Save" || seen[0].Principal.Kind != kernel.Anonymous || seen[0].Err != nil || thing.GetId() != "a" || thing.GetPage() != 2 {
		t.Errorf("first = %+v", seen[0])
	}
	if connect.CodeOf(seen[1].Err) != connect.CodeInvalidArgument {
		t.Errorf("a request that does not bind = %+v", seen[1])
	}
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "is audited and the Mux has no Audit") {
			t.Fatalf("panic = %v", r)
		}
	}()
	m, _ := NewMux(Options{Auth: anyone, Timeout: time.Second})
	Handle(m, route, echo)
}

func TestAPanickingAuditedRouteIsAuditedAsInternalAndStillPanics(t *testing.T) {
	var seen []kernel.Audit
	record := func(_ context.Context, a kernel.Audit) { seen = append(seen, a) }
	route := Route{Method: "POST", Path: "/things/{id}", Procedure: "/test.Things/Save", Body: "*", Rule: kernel.Rule{Audit: true}}
	srv := serve(t, Options{Audit: record}, route, func(context.Context, *testpb.Thing) (*testpb.Thing, error) { panic("boom") })
	p := func() (p any) {
		defer func() { p = recover() }()
		srv.Config.Handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/things/a", strings.NewReader(`{}`)))
		return nil
	}()
	if p != "boom" || len(seen) != 1 || connect.CodeOf(seen[0].Err) != connect.CodeInternal || seen[0].Request == nil {
		t.Fatalf("panic %v, audited %+v", p, seen)
	}
}

func TestARouteRefusedForWantOfAPermissionIsAuditedWithWhoMadeIt(t *testing.T) {
	var seen []kernel.Audit
	record := func(_ context.Context, a kernel.Audit) { seen = append(seen, a) }
	pol := kernel.PolicyFunc(func(context.Context, kernel.Principal, string) error { return fault.New(fault.CodeDenied, "no") })
	route := Route{Method: "POST", Path: "/things/{id}", Procedure: "/test.Things/Save", Body: "*", Rule: kernel.Rule{Audit: true, Permission: "things.write"}}
	do(t, serve(t, Options{Audit: record, Policy: pol}, route, echo), "POST", "/things/a", `{}`, nil)
	if len(seen) != 1 || seen[0].Err == nil || seen[0].Principal.Kind != kernel.Anonymous {
		t.Fatalf("refused call audit = %+v", seen)
	}
}

func TestAnEnumIsWrittenByNameOrByNumberWhenUnknownAndAMapAsAnObjectWithSortedStringKeys(t *testing.T) {
	counts := map[string]int64{}
	byID := map[int32]*testpb.Nested{}
	for i := range 12 {
		counts[fmt.Sprintf("k%02d", i)] = int64(i)
		byID[int32(i)] = &testpb.Nested{Count: int64(i)}
	}
	for range 5 {
		got, err := Marshal(&testpb.Thing{Color: testpb.Color_COLOR_RED, Counts: counts, ById: byID})
		if err != nil || !json.Valid(got) {
			t.Fatalf("Marshal = %s, %v", got, err)
		}
		s := string(got)
		if !strings.Contains(s, `"color":"COLOR_RED"`) || !strings.Contains(s, `"counts":{"k00":0,"k01":1,"k02":2,"k03":3,"k04":4,"k05":5,"k06":6,"k07":7,"k08":8,"k09":9,"k10":10,"k11":11}`) || !strings.Contains(s, `"byId":{"0":{"name":"","count":0,"parent":null},"1":{"name":"","count":1,"parent":null},"10":`) {
			t.Fatalf("Marshal = %s", s)
		}
	}
	got, _ := Marshal(&testpb.Thing{Color: 9})
	if !strings.Contains(string(got), `"color":9,`) {
		t.Fatalf("an unknown enum number = %s", got)
	}
}

func TestAnEnumIsReadFromTheQueryByNameOrNumber(t *testing.T) {
	var got testpb.Color
	srv := serve(t, Options{}, Route{Method: "GET", Path: "/things"}, func(_ context.Context, in *testpb.Thing) (*testpb.Thing, error) {
		got = in.Color
		return in, nil
	})
	for q, want := range map[string]int{"COLOR_RED": 200, "1": 200, "COLOR_BLUE": 400, "x1": 400} {
		got = 0
		code, body := do(t, srv, "GET", "/things?color="+q, "", nil)
		if code != want || (want == 200 && got != testpb.Color_COLOR_RED) {
			t.Errorf("color=%s: %d %s, bound %v", q, code, body, got)
		}
	}
}

func TestBytesAreReadFromTheQueryAsStandardOrURLSafeBase64(t *testing.T) {
	var got []byte
	srv := serve(t, Options{}, Route{Method: "GET", Path: "/things"}, func(_ context.Context, in *testpb.Thing) (*testpb.Thing, error) {
		got = in.Blob
		return in, nil
	})
	for _, q := range []string{"+/8=", "%2B%2F8%3D", "-_8", "-_8=", "+/8"} {
		got = nil
		code, body := do(t, srv, "GET", "/things?blob="+q, "", nil)
		if code != 200 || !bytes.Equal(got, []byte{0xfb, 0xff}) {
			t.Errorf("blob=%s: %d %s, bound %x", q, code, body, got)
		}
	}
}

func TestTheJSONNameWinsWhenAQueryNamesAFieldBothWays(t *testing.T) {
	var got string
	srv := serve(t, Options{}, Route{Method: "GET", Path: "/things"}, func(_ context.Context, in *testpb.Thing) (*testpb.Thing, error) {
		got = in.UserAgent
		return in, nil
	})
	for range 30 {
		if code, body := do(t, srv, "GET", "/things?user_agent=proto&userAgent=json", "", nil); code != 200 || got != "json" {
			t.Fatalf("%d %s, bound %q", code, body, got)
		}
	}
}

func TestAProto2OptionalFieldKeepsItsDefaultRatherThanNull(t *testing.T) {
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("legacy.proto"),
		Package: proto.String("legacy"),
		Syntax:  proto.String("proto2"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Old"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name: proto.String("nick"), JsonName: proto.String("nick"), Number: proto.Int32(1),
				Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			}},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Marshal(dynamicpb.NewMessage(fd.Messages().Get(0)))
	if err != nil || string(got) != `{"nick":""}` {
		t.Fatalf("Marshal = %s, %v", got, err)
	}
}

func TestAQueryKeyNamingAMapFieldIsIgnoredRatherThanPanicking(t *testing.T) {
	srv := serve(t, Options{}, Route{Method: "GET", Path: "/things"}, echo)
	res, err := http.Get(srv.URL + "/things?counts=1&byId=2&id=x")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), `"id":"x"`) || !strings.Contains(string(body), `"counts":{}`) {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
}
