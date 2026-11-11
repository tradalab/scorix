package oidc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"

	"github.com/tradalab/scorix/kernel"
)

type registry struct {
	*httptest.Server
	mu     sync.Mutex
	issuer string
	ids    []string
	down   bool
	asks   atomic.Int32
}

func newRegistry(t *testing.T, issuer string, ids ...string) *registry {
	t.Helper()
	r := &registry{issuer: issuer, ids: ids}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		r.asks.Add(1)
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.down {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		switch q.URL.Path {
		case "/api/v1/apps/links/auth-config":
			id := ""
			if len(r.ids) > 0 {
				id = r.ids[0]
			}
			json.NewEncoder(w).Encode(map[string]any{"config": map[string]string{"issuer": r.issuer, "clientId": id}})
		case "/api/v1/apps/auth-clients":
			json.NewEncoder(w).Encode(map[string]any{"issuer": r.issuer, "clientIds": r.ids})
		default:
			http.NotFound(w, q)
		}
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *registry) set(issuer string, ids ...string) {
	r.mu.Lock()
	r.issuer, r.ids = issuer, ids
	r.mu.Unlock()
}

func sourced(t *testing.T, cfg Config, src Source, err error) *Authenticator {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Source = src
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAnAppRegistryAcceptsItsOneClientAsAudienceAndParty(t *testing.T) {
	p := newIDP(t)
	reg := newRegistry(t, p.URL, "links-app")
	src, err := AppRegistry(reg.URL+"/api/v1/launcher/", "links")
	a := sourced(t, Config{}, src, err)
	if _, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(nil))), false); err != nil {
		t.Fatalf("the registered client = %v", err)
	}
	if _, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"azp": "other-app"}))), false); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("a token another client asked for = %v", err)
	}
	if _, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"iss": "https://elsewhere.example"}))), false); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("a token of another issuer = %v", err)
	}
}

func TestASharedRegistryLearnsANewAppWithoutARestartButJunkCannotHammerIt(t *testing.T) {
	p := newIDP(t)
	reg := newRegistry(t, p.URL, "links-app", "")
	src, err := SharedRegistry(reg.URL)
	a := sourced(t, Config{}, src, err)
	now := time.Unix(10_000, 0)
	a.now = func() time.Time { return now }
	if _, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"azp": "anyone"}))), false); err != nil {
		t.Fatalf("a registered client with any azp = %v", err)
	}
	if _, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"aud": []string{""}}))), false); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("an empty aud against a registry listing a blank id = %v", err)
	}
	now = now.Add(time.Minute)
	reg.set(p.URL, "links-app", "new-app")
	if _, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"aud": []string{"new-app"}}))), false); err != nil {
		t.Fatalf("a just-registered app = %v", err)
	}
	before := reg.asks.Load()
	for range 20 {
		enter(a, bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"aud": []string{"junk"}}))), false)
	}
	if n := reg.asks.Load() - before; n != 0 {
		t.Fatalf("20 unknown audiences within the interval cost %d registry calls", n)
	}
	now = now.Add(time.Minute)
	enter(a, bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"aud": []string{"junk"}}))), false)
	if n := reg.asks.Load() - before; n != 1 {
		t.Fatalf("an unknown audience after the interval cost %d registry calls, want 1", n)
	}
}

func TestARegistryThatNamesAnotherIssuerLaterKeepsTheFirst(t *testing.T) {
	p := newIDP(t)
	reg := newRegistry(t, p.URL, "links-app")
	src, err := SharedRegistry(reg.URL)
	a := sourced(t, Config{}, src, err)
	if _, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(nil))), false); err != nil {
		t.Fatal(err)
	}
	reg.set("https://elsewhere.example", "links-app", "new-app")
	if _, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"aud": []string{"new-app"}}))), false); err != nil {
		t.Fatalf("a new app while the registry names another issuer = %v", err)
	}
	if _, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"aud": []string{"new-app"}}))), false); err != nil {
		t.Fatalf("the next call after the registry named another issuer = %v", err)
	}
}

func TestARegistryThatIsDownOrIncompleteIsUnavailableUntilItAnswers(t *testing.T) {
	p := newIDP(t)
	reg := newRegistry(t, p.URL, "", " ")
	src, err := SharedRegistry(reg.URL)
	a := sourced(t, Config{}, src, err)
	now := time.Unix(10_000, 0)
	a.now = func() time.Time { return now }
	tok := bearer(p.sign(t, "rsa1", p.claims(nil)))
	if _, _, err := enter(a, tok, false); connect.CodeOf(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "both are required") {
		t.Fatalf("a registry listing only blank ids = %v", err)
	}
	reg.set(p.URL, "links-app")
	reg.mu.Lock()
	reg.down = true
	reg.mu.Unlock()
	now = now.Add(time.Minute)
	if _, _, err := enter(a, tok, false); connect.CodeOf(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "502") {
		t.Fatalf("a registry that is down = %v", err)
	}
	reg.mu.Lock()
	reg.down = false
	reg.mu.Unlock()
	if _, _, err := enter(a, tok, false); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("before the retry interval = %v", err)
	}
	now = now.Add(time.Minute)
	if _, _, err := enter(a, tok, false); err != nil {
		t.Fatalf("after the registry came back = %v", err)
	}
}

func TestASourceWithNoIssuerUsesTheConfiguredOne(t *testing.T) {
	p := newIDP(t)
	a := sourced(t, Config{Issuer: p.URL}, func(context.Context) (Clients, error) {
		return Clients{Audiences: []string{"links-app"}}, nil
	}, nil)
	if _, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(nil))), false); err != nil {
		t.Fatalf("a client set from code = %v", err)
	}
}

func TestARegistryConfigThatNamesNothingIsRefused(t *testing.T) {
	if _, err := AppRegistry(" ", "links"); err == nil {
		t.Error("AppRegistry with no URL")
	}
	if _, err := AppRegistry("http://id", ""); err == nil {
		t.Error("AppRegistry with no slug")
	}
	if _, err := SharedRegistry("/api/v1/launcher"); err == nil {
		t.Error("SharedRegistry with only the launcher path")
	}
	src, _ := SharedRegistry("http://id")
	if _, err := New(Config{Source: src, Audiences: []string{"a"}}); err == nil || !strings.Contains(err.Error(), "one or the other") {
		t.Errorf("Source beside Audiences = %v", err)
	}
	if _, err := New(Config{Source: src, AuthorizedParties: []string{"a"}}); err == nil {
		t.Error("Source beside AuthorizedParties")
	}
}

func TestPersonalOnlyRefusesAnAccountMarkedPersonalDenied(t *testing.T) {
	p := newIDP(t)
	denied := bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"personal_denied": true})))
	if _, id, err := enter(authenticator(t, p, Config{}), denied, false); err != nil || !id.PersonalDenied {
		t.Fatalf("without PersonalOnly = %+v %v", id, err)
	}
	a := authenticator(t, p, Config{PersonalOnly: true})
	if _, _, err := enter(a, denied, false); connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), PersonalOnlyRefusal) {
		t.Fatalf("with PersonalOnly = %v", err)
	}
	if _, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(nil))), false); err != nil {
		t.Fatalf("an unmarked account with PersonalOnly = %v", err)
	}
}

func TestAForeignPartyIsReportedAndLetThroughWhenAsked(t *testing.T) {
	p := newIDP(t)
	var got []string
	a := authenticator(t, p, Config{AuthorizedParties: []string{"links-app"}, ForeignParty: func(_ context.Context, azp, procedure string) {
		got = append(got, azp+" "+procedure)
	}})
	if _, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(nil))), false); err != nil || len(got) != 0 {
		t.Fatalf("an authorized party = %v, reported %v", err, got)
	}
	if _, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"azp": "other-app"}))), false); err != nil {
		t.Fatalf("a foreign party with ForeignParty set = %v", err)
	}
	if len(got) != 1 || got[0] != "other-app /x.Y/Z" {
		t.Fatalf("reported %v", got)
	}
}

func TestLogForeignPartiesLogsEachClientOncePerInterval(t *testing.T) {
	var buf bytes.Buffer
	now := time.Unix(10_000, 0)
	report := logForeign(slog.New(slog.NewTextHandler(&buf, nil)), func() time.Time { return now })
	for range 3 {
		report(context.Background(), "a", "/x")
	}
	report(context.Background(), "b", "/x")
	now = now.Add(11 * time.Minute)
	report(context.Background(), "a", "/x")
	if n := strings.Count(buf.String(), "azp=a"); n != 2 {
		t.Fatalf("client a logged %d times, want 2:\n%s", n, buf.String())
	}
	if n := strings.Count(buf.String(), "azp=b"); n != 1 {
		t.Fatalf("client b logged %d times, want 1", n)
	}
}

func TestOnAuthenticatedSeesTheIdentityAndCanRefuse(t *testing.T) {
	p := newIDP(t)
	tok := bearer(p.sign(t, "rsa1", p.claims(nil)))
	var seen string
	ok := authenticator(t, p, Config{OnAuthenticated: func(ctx context.Context, id Identity) error {
		fromCtx, _ := IdentityFrom(ctx)
		seen = id.UserID + "/" + fromCtx.UserID
		return nil
	}})
	if _, _, err := enter(ok, tok, false); err != nil || seen != "u-1/u-1" {
		t.Fatalf("a hook that admits = %v, saw %q", err, seen)
	}
	plain := authenticator(t, p, Config{OnAuthenticated: func(context.Context, Identity) error { return errors.New("db down: secret dsn") }})
	if _, _, err := enter(plain, tok, false); connect.CodeOf(err) != connect.CodeUnauthenticated || strings.Contains(err.Error(), "dsn") {
		t.Fatalf("a hook that fails = %v", err)
	}
	typed := authenticator(t, p, Config{OnAuthenticated: func(context.Context, Identity) error {
		return connect.NewError(connect.CodePermissionDenied, errors.New("suspended"))
	}})
	if _, _, err := enter(typed, tok, false); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a hook that refuses with a code = %v", err)
	}
}

func TestAClientTheRegistryDropsIsRefusedAfterTheNextRefresh(t *testing.T) {
	p := newIDP(t)
	reg := newRegistry(t, p.URL, "links-app", "evil-app")
	src, err := SharedRegistry(reg.URL)
	a := sourced(t, Config{}, src, err)
	now := time.Unix(10_000, 0)
	a.now = func() time.Time { return now }
	evil := bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"aud": []string{"evil-app"}})))
	if _, _, err := enter(a, evil, false); err != nil {
		t.Fatalf("a listed client = %v", err)
	}
	reg.set(p.URL, "links-app")
	now = now.Add(refreshInterval + time.Second)
	waitFor(t, func() bool {
		_, _, err := enter(a, evil, false)
		return connect.CodeOf(err) == connect.CodeUnauthenticated
	})
	reg.mu.Lock()
	reg.down = true
	reg.mu.Unlock()
	now = now.Add(refreshInterval + time.Second)
	for range 3 {
		if _, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(nil))), false); err != nil {
			t.Fatalf("a refresh that failed dropped the list it had: %v", err)
		}
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("never happened")
}

func within(t *testing.T, limit time.Duration, a *Authenticator, h http.Header, ctxFor time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), ctxFor)
	defer cancel()
	start := time.Now()
	_, err := a.Authenticate(ctx, kernel.Call{Procedure: "/x.Y/Z", Header: h})
	if took := time.Since(start); took > limit {
		t.Fatalf("took %v, want under %v (ctx %v)", took, limit, ctxFor)
	}
	return err
}

func TestAHungSourceNeitherBlocksCallersNorOutlivesTheirDeadline(t *testing.T) {
	p := newIDP(t)
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	var calls atomic.Int32
	src := func(ctx context.Context) (Clients, error) {
		if calls.Add(1) > 1 {
			<-hang
		}
		return Clients{Issuer: p.URL, Audiences: []string{"links-app"}}, nil
	}
	a := sourced(t, Config{}, src, nil)
	now := time.Unix(10_000, 0)
	var nowMu sync.Mutex
	a.now = func() time.Time { nowMu.Lock(); defer nowMu.Unlock(); return now }
	good := bearer(p.sign(t, "rsa1", p.claims(nil)))
	if _, _, err := enter(a, good, false); err != nil {
		t.Fatal(err)
	}
	junk := bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"aud": []string{"junk"}})))
	within(t, time.Second, a, junk, 200*time.Millisecond)
	nowMu.Lock()
	now = now.Add(refreshInterval + time.Second)
	nowMu.Unlock()
	for range 3 {
		if err := within(t, 250*time.Millisecond, a, good, 2*time.Second); err != nil {
			t.Fatalf("a known client while the source hangs: %v", err)
		}
	}
}

func TestAStaleListIsServedWhileAnotherReadHoldsTheLine(t *testing.T) {
	p := newIDP(t)
	src := func(ctx context.Context) (Clients, error) {
		return Clients{Issuer: p.URL, Audiences: []string{"links-app"}}, nil
	}
	a := sourced(t, Config{}, src, nil)
	now := time.Unix(10_000, 0)
	a.now = func() time.Time { return now }
	good := bearer(p.sign(t, "rsa1", p.claims(nil)))
	if _, _, err := enter(a, good, false); err != nil {
		t.Fatal(err)
	}
	now = now.Add(refreshInterval + time.Second)
	a.fetching <- struct{}{}
	defer a.release()
	if err := within(t, 300*time.Millisecond, a, good, 2*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestCallersArrivingDuringTheFirstReadWaitForIt(t *testing.T) {
	p := newIDP(t)
	src := func(ctx context.Context) (Clients, error) {
		time.Sleep(200 * time.Millisecond)
		return Clients{Issuer: p.URL, Audiences: []string{"links-app"}}, nil
	}
	a := sourced(t, Config{}, src, nil)
	good := bearer(p.sign(t, "rsa1", p.claims(nil)))
	errs := make(chan error, 3)
	for range 3 {
		go func() {
			_, _, err := enter(a, good, false)
			errs <- err
		}()
	}
	for range 3 {
		if err := <-errs; err != nil {
			t.Fatalf("a caller during the first read: %v", err)
		}
	}
}

func TestAReadThatOutlivesItsTimeoutFreesTheLine(t *testing.T) {
	p := newIDP(t)
	var calls atomic.Int32
	src := func(ctx context.Context) (Clients, error) {
		switch calls.Add(1) {
		case 1:
			return Clients{Issuer: p.URL, Audiences: []string{"links-app"}}, nil
		case 2:
			<-ctx.Done()
			return Clients{}, ctx.Err()
		}
		return Clients{Issuer: p.URL, Audiences: []string{"links-app", "new-app"}}, nil
	}
	a := sourced(t, Config{}, src, nil)
	a.fetchTimeout = 100 * time.Millisecond
	now := time.Unix(10_000, 0)
	var nowMu sync.Mutex
	a.now = func() time.Time { nowMu.Lock(); defer nowMu.Unlock(); return now }
	if _, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(nil))), false); err != nil {
		t.Fatal(err)
	}
	junk := bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"aud": []string{"junk"}})))
	within(t, time.Second, a, junk, 50*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	nowMu.Lock()
	now = now.Add(time.Minute)
	nowMu.Unlock()
	if err := within(t, time.Second, a, bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"aud": []string{"new-app"}}))), 2*time.Second); err != nil {
		t.Fatalf("after the hung read timed out: %v", err)
	}
}

func TestAHungKeyFetchEndsWithTheCallersDeadline(t *testing.T) {
	p := newIDP(t)
	p.hold = make(chan struct{})
	t.Cleanup(func() { close(p.hold) })
	a := authenticator(t, p, Config{})
	err := within(t, time.Second, a, bearer(p.sign(t, "rsa1", p.claims(nil))), 200*time.Millisecond)
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestCallersArrivingDuringTheFirstKeyFetchWaitForIt(t *testing.T) {
	p := newIDP(t)
	p.delay = 200 * time.Millisecond
	a := authenticator(t, p, Config{})
	good := bearer(p.sign(t, "rsa1", p.claims(nil)))
	errs := make(chan error, 3)
	for range 3 {
		go func() {
			_, _, err := enter(a, good, false)
			errs <- err
		}()
	}
	for range 3 {
		if err := <-errs; err != nil {
			t.Fatalf("a caller during the first key fetch: %v", err)
		}
	}
}

func TestAHungFirstReadEndsWithTheCallersDeadline(t *testing.T) {
	p := newIDP(t)
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	src := func(ctx context.Context) (Clients, error) {
		<-hang
		return Clients{}, errors.New("never")
	}
	a := sourced(t, Config{}, src, nil)
	good := bearer(p.sign(t, "rsa1", p.claims(nil)))
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		start := time.Now()
		_, err := a.Authenticate(ctx, kernel.Call{Procedure: "/x.Y/Z", Header: good})
		cancel()
		if connect.CodeOf(err) != connect.CodeUnavailable && connect.CodeOf(err) != connect.CodeDeadlineExceeded {
			t.Fatalf("err = %v", err)
		}
		if took := time.Since(start); took > time.Second {
			t.Fatalf("waited %v with a 200ms deadline", took)
		}
	}
}
