package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/tradalab/scorix/observe"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/tradalab/scorix/internal/redactpb"
	"github.com/tradalab/scorix/kernel"
	"github.com/tradalab/scorix/transport/internal/gate"
	"github.com/tradalab/scorix/transport/internal/testpb"
)

type audit struct {
	mu     sync.Mutex
	events []Event
}

func (a *audit) record(_ context.Context, e Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

var ana = kernel.AuthenticatorFunc(func(ctx context.Context, c kernel.Call) (context.Context, error) {
	if c.Header.Get("Authorization") != "Bearer ana" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no token"))
	}
	return kernel.WithPrincipal(ctx, kernel.Principal{Kind: kernel.User, ID: "ana"}), nil
})

var thing = Tool{Name: "things_get", Description: "Get a thing.", Procedure: "/acme.Things/Get", InputSchema: json.RawMessage(`{"type":"object"}`)}

func serve(t *testing.T, opt Options, fn func(context.Context, *testpb.Thing) (*testpb.Thing, error)) (*httptest.Server, *audit) {
	t.Helper()
	a := &audit{}
	if opt.Auth == nil {
		opt.Auth = ana
	}
	if opt.Timeout == 0 {
		opt.Timeout = 5 * time.Second
	}
	opt.Audit = a.record
	opt.Name, opt.Version = "acme", "1.0.0"
	m, err := NewMux(opt)
	if err != nil {
		t.Fatal(err)
	}
	Register(m, thing, fn)
	Register(m, Tool{Name: "things_drop", Description: "Drop it.", Procedure: "/acme.Things/Drop", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`), Destructive: true}, fn)
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return srv, a
}

func post(t *testing.T, srv *httptest.Server, token, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%d: not JSON: %s", res.StatusCode, raw)
		}
	}
	return res.StatusCode, out
}

func call(name, args string) string {
	return `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
}

func echo(ctx context.Context, in *testpb.Thing) (*testpb.Thing, error) {
	p, _ := kernel.PrincipalFrom(ctx)
	return &testpb.Thing{Id: p.ID + ":" + in.GetId(), Views: in.GetViews() + 1}, nil
}

func TestNewMuxRefusesAMissingAuthenticatorTimeoutOrAudit(t *testing.T) {
	audit := func(context.Context, Event) {}
	for name, opt := range map[string]Options{
		"Options.Auth":    {Timeout: time.Second, Audit: audit},
		"Options.Timeout": {Auth: ana, Audit: audit},
		"Options.Audit":   {Auth: ana, Timeout: time.Second},
	} {
		if _, err := NewMux(opt); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("NewMux without %s = %v", name, err)
		}
	}
}

func TestInitializeAgreesOnAVersionTheClientSpeaks(t *testing.T) {
	srv, _ := serve(t, Options{}, echo)
	for asked, want := range map[string]string{"2025-03-26": "2025-03-26", "1999-01-01": ProtocolVersions[0]} {
		_, out := post(t, srv, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+asked+`"}}`)
		res := out["result"].(map[string]any)
		if res["protocolVersion"] != want || res["serverInfo"].(map[string]any)["name"] != "acme" || res["capabilities"].(map[string]any)["tools"] == nil {
			t.Errorf("initialize %s = %v", asked, res)
		}
	}
	if _, out := post(t, srv, "", `{"jsonrpc":"2.0","id":2,"method":"ping"}`); out["result"] == nil {
		t.Errorf("ping = %v", out)
	}
}

func TestANotificationIsAcceptedWithNoBody(t *testing.T) {
	srv, _ := serve(t, Options{}, echo)
	if code, out := post(t, srv, "", `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != http.StatusAccepted || out != nil {
		t.Fatalf("%d %v, want 202 and nothing", code, out)
	}
}

func TestOnlyWellFormedPostsAreAnswered(t *testing.T) {
	srv, _ := serve(t, Options{}, echo)
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed || res.Header.Get("Allow") != "POST" {
		t.Errorf("GET = %d, want 405 naming POST", res.StatusCode)
	}
	for body, code := range map[string]float64{`{"jsonrpc":`: codeParse, `{"jsonrpc":"2.0","id":1}`: codeInvalidRequest, `{"id":1,"method":"ping"}`: codeInvalidRequest} {
		status, out := post(t, srv, "", body)
		if status != http.StatusBadRequest || out["error"].(map[string]any)["code"] != code {
			t.Errorf("%s = %d %v, want 400 with code %v", body, status, out, code)
		}
	}
	if _, out := post(t, srv, "", `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`); out["error"].(map[string]any)["code"] != float64(codeMethodNotFound) {
		t.Errorf("unknown method = %v", out)
	}
}

func TestAnOversizedMessageIsRefusedUnread(t *testing.T) {
	srv, _ := serve(t, Options{}, echo)
	code, out := post(t, srv, "ana", `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"`+strings.Repeat("x", gate.MaxBody)+`"}}`)
	if code != http.StatusRequestEntityTooLarge || out["error"] == nil {
		t.Fatalf("%d %v, want 413", code, out)
	}
}

func TestListingToolsNeedsACaller(t *testing.T) {
	srv, _ := serve(t, Options{}, echo)
	if code, _ := post(t, srv, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); code != http.StatusUnauthorized {
		t.Errorf("tools/list without a token = %d, want 401", code)
	}
	_, out := post(t, srv, "ana", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := out["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools = %v", tools)
	}
	get, drop := tools[0].(map[string]any), tools[1].(map[string]any)
	if get["name"] != "things_get" || get["description"] != "Get a thing." || get["inputSchema"] == nil || get["outputSchema"] != nil {
		t.Errorf("things_get = %v", get)
	}
	if get["annotations"].(map[string]any)["destructiveHint"] != false || drop["annotations"].(map[string]any)["destructiveHint"] != true || drop["outputSchema"] == nil {
		t.Errorf("annotations: get %v drop %v", get["annotations"], drop["annotations"])
	}
}

func TestACallRunsAsTheCallerAndIsAudited(t *testing.T) {
	var seen []string
	auth := kernel.AuthenticatorFunc(func(ctx context.Context, c kernel.Call) (context.Context, error) {
		seen = append(seen, c.Procedure)
		return ana(ctx, c)
	})
	srv, a := serve(t, Options{Auth: auth}, echo)
	_, out := post(t, srv, "ana", call("things_get", `{"id":"x","views":"41"}`))
	res := out["result"].(map[string]any)
	sc := res["structuredContent"].(map[string]any)
	if sc["id"] != "ana:x" || sc["views"] != float64(42) || res["isError"] != nil {
		t.Fatalf("result = %v", res)
	}
	if text := res["content"].([]any)[0].(map[string]any)["text"].(string); !strings.Contains(text, `"views":42`) {
		t.Errorf("text content = %s", text)
	}
	if len(seen) != 1 || seen[0] != "/acme.Things/Get" {
		t.Errorf("authenticator saw %v, want the tool's procedure", seen)
	}
	if len(a.events) != 1 || a.events[0].Tool != "things_get" || a.events[0].Principal.ID != "ana" || a.events[0].Err != nil || a.events[0].Procedure != "/acme.Things/Get" {
		t.Fatalf("audit = %+v", a.events)
	}
}

func TestAFailingToolIsAnErrorResultAndIsAudited(t *testing.T) {
	srv, a := serve(t, Options{}, func(context.Context, *testpb.Thing) (*testpb.Thing, error) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such thing"))
	})
	_, out := post(t, srv, "ana", call("things_get", `{}`))
	res := out["result"].(map[string]any)
	if res["isError"] != true || res["content"].([]any)[0].(map[string]any)["text"] != "no such thing" {
		t.Fatalf("result = %v", res)
	}
	if len(a.events) != 1 || a.events[0].Err == nil {
		t.Fatalf("audit = %+v", a.events)
	}
}

func TestAResultThatIsNotJSONIsAnErrorResultForTheSameRequest(t *testing.T) {
	srv, _ := serve(t, Options{}, func(context.Context, *testpb.Thing) (*testpb.Thing, error) {
		return &testpb.Thing{Score: math.NaN()}, nil
	})
	code, out := post(t, srv, "ana", call("things_get", `{}`))
	res, _ := out["result"].(map[string]any)
	if code != http.StatusOK || out["id"] != float64(7) || res["isError"] != true {
		t.Fatalf("%d %v", code, out)
	}
}

func TestARefusedCallerStillGetsTheirRequestID(t *testing.T) {
	srv, _ := serve(t, Options{}, echo)
	for _, body := range []string{call("things_get", `{}`), `{"jsonrpc":"2.0","id":"q1","method":"tools/list"}`} {
		code, out := post(t, srv, "", body)
		if code != http.StatusUnauthorized || (out["id"] != float64(7) && out["id"] != "q1") {
			t.Errorf("%d %v", code, out)
		}
	}
}

func TestArgumentsThatDoNotFitAreInvalidParams(t *testing.T) {
	srv, _ := serve(t, Options{}, echo)
	for _, args := range []string{`{"nope":1}`, `{"views":"many"}`} {
		_, out := post(t, srv, "ana", call("things_get", args))
		if out["error"].(map[string]any)["code"] != float64(codeInvalidParams) {
			t.Errorf("%s = %v, want invalid params", args, out)
		}
	}
	_, out := post(t, srv, "ana", call("things_get", `null`))
	if out["result"].(map[string]any)["structuredContent"].(map[string]any)["id"] != "ana:" {
		t.Errorf("null arguments = %v, want an empty request", out)
	}
}

func TestAnUnknownToolIsInvalidParamsAndNotAudited(t *testing.T) {
	srv, a := serve(t, Options{}, echo)
	_, out := post(t, srv, "ana", call("things_burn", `{}`))
	if out["error"].(map[string]any)["code"] != float64(codeInvalidParams) || len(a.events) != 0 {
		t.Fatalf("%v, audit %+v", out, a.events)
	}
	_, out = post(t, srv, "ana", `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":"x"}`)
	if out["error"].(map[string]any)["code"] != float64(codeInvalidParams) {
		t.Fatalf("malformed params = %v", out)
	}
}

func TestARefusedCallNeverRunsAndIsAudited(t *testing.T) {
	ran := false
	srv, a := serve(t, Options{}, func(context.Context, *testpb.Thing) (*testpb.Thing, error) { ran = true; return &testpb.Thing{}, nil })
	if code, _ := post(t, srv, "mallory", call("things_get", `{}`)); code != http.StatusUnauthorized {
		t.Errorf("refused call = %d, want 401", code)
	}
	if ran || len(a.events) != 1 || a.events[0].Err == nil || a.events[0].Principal.Kind != "" {
		t.Fatalf("ran %v, audit %+v", ran, a.events)
	}
}

func TestAnAuthenticatorThatNamesNoPrincipalIsRefused(t *testing.T) {
	silent := kernel.AuthenticatorFunc(func(ctx context.Context, _ kernel.Call) (context.Context, error) { return ctx, nil })
	srv, _ := serve(t, Options{Auth: silent}, echo)
	if code, out := post(t, srv, "ana", call("things_get", `{}`)); code != http.StatusInternalServerError || !strings.Contains(out["error"].(map[string]any)["message"].(string), "principal") {
		t.Fatalf("%d %v", code, out)
	}
}

func TestACallThatOutlivesTheTimeoutIsCutOff(t *testing.T) {
	srv, _ := serve(t, Options{Timeout: 50 * time.Millisecond}, func(ctx context.Context, _ *testpb.Thing) (*testpb.Thing, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	start := time.Now()
	_, out := post(t, srv, "ana", call("things_get", `{}`))
	if out["result"].(map[string]any)["isError"] != true || time.Since(start) > 2*time.Second {
		t.Fatalf("%v after %v", out, time.Since(start))
	}
}

func TestAToolRegisteredTwicePanics(t *testing.T) {
	m, _ := NewMux(Options{Auth: ana, Timeout: time.Second, Audit: func(context.Context, Event) {}})
	Register(m, thing, echo)
	defer func() {
		if recover() == nil {
			t.Fatal("a second tool with the same name was accepted")
		}
	}()
	Register(m, thing, echo)
}

func TestTheToolsLogNamesTheProcedureAndThePrincipal(t *testing.T) {
	var buf bytes.Buffer
	log, _ := observe.NewLogger(&buf, "info")
	auth := kernel.AuthenticatorFunc(func(ctx context.Context, c kernel.Call) (context.Context, error) {
		return ana(observe.WithLogger(ctx, log), c)
	})
	srv, _ := serve(t, Options{Auth: auth}, func(ctx context.Context, in *testpb.Thing) (*testpb.Thing, error) {
		observe.Log(ctx).Info("handled")
		return in, nil
	})
	post(t, srv, "ana", call("things_get", `{}`))
	if out := buf.String(); !strings.Contains(out, `"procedure":"/acme.Things/Get"`) || !strings.Contains(out, `"principal":"user:ana"`) {
		t.Fatalf("log = %s", out)
	}
}

func TestAToolsRuleChecksThePermissionAndIsAudited(t *testing.T) {
	ran := false
	pol := kernel.PolicyFunc(func(_ context.Context, p kernel.Principal, perm string) error {
		return errors.New(p.ID + " may not " + perm)
	})
	a := &audit{}
	m, _ := NewMux(Options{Auth: ana, Timeout: time.Second, Audit: a.record, Policy: pol})
	Register(m, Tool{Name: "guarded", Description: "x", Procedure: "/acme.Things/Guarded", InputSchema: json.RawMessage(`{"type":"object"}`), Rule: kernel.Rule{Permission: "things.write"}},
		func(context.Context, *testpb.Thing) (*testpb.Thing, error) { ran = true; return &testpb.Thing{}, nil })
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	_, out := post(t, srv, "ana", call("guarded", `{}`))
	res := out["result"].(map[string]any)
	if ran || res["isError"] != true || !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "ana may not things.write") {
		t.Fatalf("ran %v, result %v", ran, res)
	}
	if len(a.events) != 1 || a.events[0].Err == nil || a.events[0].Principal.ID != "ana" {
		t.Fatalf("audit = %+v", a.events)
	}
}

func TestAToolsRuleTimeoutReplacesTheMuxes(t *testing.T) {
	m, _ := NewMux(Options{Auth: ana, Timeout: time.Minute, Audit: func(context.Context, Event) {}})
	Register(m, Tool{Name: "slow", Description: "x", Procedure: "/a/b", InputSchema: json.RawMessage(`{}`), Rule: kernel.Rule{Timeout: 50 * time.Millisecond}},
		func(ctx context.Context, _ *testpb.Thing) (*testpb.Thing, error) { <-ctx.Done(); return nil, ctx.Err() })
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	start := time.Now()
	if _, out := post(t, srv, "ana", call("slow", `{}`)); out["result"].(map[string]any)["isError"] != true || time.Since(start) > 2*time.Second {
		t.Fatalf("%v after %v", out, time.Since(start))
	}
}

func TestAToolsEffectReachesItsAnnotationsAndAPermissionNeedsAPolicy(t *testing.T) {
	m, _ := NewMux(Options{Auth: ana, Timeout: time.Second, Audit: func(context.Context, Event) {}})
	Register(m, Tool{Name: "look", Description: "x", Procedure: "/a/b", InputSchema: json.RawMessage(`{}`), ReadOnly: true, Idempotent: true}, echo)
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	_, out := post(t, srv, "ana", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	ann := out["result"].(map[string]any)["tools"].([]any)[0].(map[string]any)["annotations"].(map[string]any)
	if ann["readOnlyHint"] != true || ann["idempotentHint"] != true || ann["destructiveHint"] != false {
		t.Fatalf("annotations = %v", ann)
	}
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "has no Policy") {
			t.Fatalf("recover = %v", r)
		}
	}()
	Register(m, Tool{Name: "guarded", Rule: kernel.Rule{Permission: "x"}}, echo)
}

func TestAnAgentNeverSeesASecretAndTheAuditNeverSeesPII(t *testing.T) {
	a := &audit{}
	m, _ := NewMux(Options{Auth: ana, Timeout: time.Second, Audit: a.record})
	Register(m, Tool{Name: "account", Description: "x", Procedure: "/a/b", InputSchema: json.RawMessage(`{}`), ReadOnly: true},
		func(_ context.Context, in *redactpb.Owner) (*redactpb.Account, error) {
			return &redactpb.Account{Name: "ops", ApiKey: "sk-live", Owner: in}, nil
		})
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	_, out := post(t, srv, "ana", call("account", `{"email":"ana@example.com","team":"core"}`))
	res := out["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	sc := res["structuredContent"].(map[string]any)
	if sc["apiKey"] != "" || strings.Contains(text, "sk-live") || sc["owner"].(map[string]any)["email"] != "ana@example.com" {
		t.Fatalf("the agent got %v", sc)
	}
	if len(a.events) != 1 || !strings.Contains(string(a.events[0].Arguments), `"email":"[pii]"`) || strings.Contains(string(a.events[0].Arguments), "ana@example.com") || !strings.Contains(string(a.events[0].Arguments), `"team":"core"`) {
		t.Fatalf("audit arguments = %s", a.events[0].Arguments)
	}
	post(t, srv, "ana", call("account", `{"nope":1}`))
	if len(a.events) != 2 || a.events[1].Arguments != nil || a.events[1].Err == nil {
		t.Fatalf("undecodable arguments were audited as %+v", a.events[1])
	}
}

func TestAReplyThatCannotBeEncodedIsAnInternalErrorNotAnEmptyOK(t *testing.T) {
	w := httptest.NewRecorder()
	reply(w, http.StatusOK, response{JSONRPC: "2.0", ID: json.RawMessage("7"), Result: json.RawMessage("{")})
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusInternalServerError || out["id"] != float64(7) || out["error"].(map[string]any)["code"] != float64(-32603) {
		t.Fatalf("%d %s", w.Code, w.Body.Bytes())
	}
}
