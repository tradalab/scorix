package connect

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/internal/redactpb"
	"github.com/tradalab/scorix/kernel"
)

type ledger struct {
	mu   sync.Mutex
	seen []kernel.Audit
}

func (l *ledger) record(_ context.Context, a kernel.Audit) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, a)
}

const save = "/test.Accounts/Save"

func accounts(t *testing.T, opt Options, rule kernel.Rule) *Conn {
	t.Helper()
	m := mux(t, opt)
	Unary(m, save, rule, func(_ context.Context, in *redactpb.Account) (*redactpb.Account, error) {
		if in.GetName() == "missing" {
			return nil, fault.New(fault.CodeNotFound, "no such account")
		}
		return in, nil
	})
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return Dial(srv.URL, ConnOptions{Header: func(_ context.Context, h http.Header) { h.Set("X-Who", "u1") }})
}

func TestAnAuditedCallIsRecordedWithItsPrincipalAndARedactedRequest(t *testing.T) {
	var l ledger
	conn := accounts(t, Options{Auth: trustAll, Audit: l.record}, kernel.Rule{Audit: true})
	Call[redactpb.Account, redactpb.Account](context.Background(), conn, save, &redactpb.Account{Name: "a", ApiKey: "sk-live"})
	Call[redactpb.Account, redactpb.Account](context.Background(), conn, save, &redactpb.Account{Name: "missing"})
	if len(l.seen) != 2 {
		t.Fatalf("audited %d calls, want 2", len(l.seen))
	}
	first, second := l.seen[0], l.seen[1]
	acct, _ := first.Request.(*redactpb.Account)
	if first.Transport != "connect" || first.Procedure != save || first.Principal.ID != "u1" || first.Err != nil || acct.GetName() != "a" || acct.GetApiKey() != "" {
		t.Errorf("first = %+v", first)
	}
	if fault.CodeOf(second.Err) != fault.CodeNotFound && connect.CodeOf(second.Err) != connect.CodeNotFound {
		t.Errorf("second err = %v", second.Err)
	}
}

func TestAPanickingAuditedCallIsAuditedAsInternalAndStillPanics(t *testing.T) {
	var l ledger
	m := mux(t, Options{Auth: trustAll, Audit: l.record})
	Unary(m, save, kernel.Rule{Audit: true}, func(context.Context, *redactpb.Account) (*redactpb.Account, error) { panic("boom") })
	req := httptest.NewRequest("POST", save, strings.NewReader(`{"name":"a"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Who", "u1")
	p := func() (p any) {
		defer func() { p = recover() }()
		m.ServeHTTP(httptest.NewRecorder(), req)
		return nil
	}()
	if p != "boom" || len(l.seen) != 1 || connect.CodeOf(l.seen[0].Err) != connect.CodeInternal || l.seen[0].Principal.ID != "u1" {
		t.Fatalf("panic %v, audited %+v", p, l.seen)
	}
}

func TestARefusedCallIsAuditedTooAndAnUnauditedOneIsNot(t *testing.T) {
	var l ledger
	refuse := kernel.AuthenticatorFunc(func(context.Context, kernel.Call) (context.Context, error) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("who are you"))
	})
	Call[redactpb.Account, redactpb.Account](context.Background(), accounts(t, Options{Auth: refuse, Audit: l.record}, kernel.Rule{Audit: true}), save, &redactpb.Account{Name: "a"})
	if len(l.seen) != 1 || connect.CodeOf(l.seen[0].Err) != connect.CodeUnauthenticated || l.seen[0].Principal.Kind != "" {
		t.Fatalf("refused call audit = %+v", l.seen)
	}
	Call[redactpb.Account, redactpb.Account](context.Background(), accounts(t, Options{Auth: trustAll, Audit: l.record}, kernel.Rule{}), save, &redactpb.Account{Name: "a"})
	if len(l.seen) != 1 {
		t.Fatalf("a call whose rule does not audit was recorded: %+v", l.seen[1:])
	}
}

func TestACallRefusedForWantOfAPermissionIsAuditedWithWhoMadeIt(t *testing.T) {
	var l ledger
	deny := kernel.PolicyFunc(func(context.Context, kernel.Principal, string) error { return fault.New(fault.CodeDenied, "no") })
	conn := accounts(t, Options{Auth: trustAll, Policy: deny, Audit: l.record}, kernel.Rule{Audit: true, Permission: "accounts.save"})
	Call[redactpb.Account, redactpb.Account](context.Background(), conn, save, &redactpb.Account{Name: "a"})
	if len(l.seen) != 1 || connect.CodeOf(l.seen[0].Err) != connect.CodePermissionDenied || l.seen[0].Principal.ID != "u1" {
		t.Fatalf("refused call audit = %+v", l.seen)
	}
}

func TestAuditWithoutAnAuditorIsRefusedAtRegistration(t *testing.T) {
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "is audited and the Mux has no Audit") {
			t.Fatalf("panic = %v", r)
		}
	}()
	Unary(mux(t, Options{Auth: trustAll}), save, kernel.Rule{Audit: true}, func(context.Context, *redactpb.Account) (*redactpb.Account, error) { return nil, nil })
}
