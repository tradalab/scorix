package netx

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tradalab/scorix/fault"
)

func TestAnUntrustedOrChangedKeyBecomesAFaultCarryingTheKeyToPin(t *testing.T) {
	b := startBastion(t)
	for _, c := range []struct {
		name string
		hk   HostKeyPolicy
		code string
	}{
		{"unpinned", HostKeyPolicy{}, CodeHostKeyUnknown},
		{"pinned to another key", HostKeyPolicy{Pinned: startBastion(t).pin()}, CodeHostKeyChanged},
	} {
		_, err := sshRoute(t, b, c.hk).Dial(context.Background(), "tcp", echoServer(t))
		fe, ok := HostKeyFault(fmt.Errorf("open connection: %w", err))
		if !ok || fe.Code != c.code || fe.Details["key"] != b.pin() || fe.Details["fingerprint"] == "" || !IsHostKeyFault(fe) {
			t.Fatalf("%s: fault = %+v, ok = %v", c.name, fe, ok)
		}
	}
	if fe, ok := HostKeyFault(errors.New("connection refused")); ok || fe != nil || IsHostKeyFault(errors.New("x")) {
		t.Fatal("a plain error became a host key fault")
	}
}

func TestVerifyHostKeyFlagsARekeyedBastionAndNothingElse(t *testing.T) {
	b := startBastion(t)
	r := sshRoute(t, b, HostKeyPolicy{Pinned: b.pin()})
	if err := r.VerifyHostKey(context.Background()); err != nil {
		t.Fatalf("the pinned bastion was flagged: %v", err)
	}
	other := sshRoute(t, b, HostKeyPolicy{Pinned: startBastion(t).pin()})
	if err := other.VerifyHostKey(context.Background()); fault.CodeOf(err) != CodeHostKeyChanged {
		t.Fatalf("err = %v, want %s", err, CodeHostKeyChanged)
	}
	gone := sshRoute(t, b, HostKeyPolicy{Pinned: b.pin()})
	_ = gone.Close()
	if err := gone.VerifyHostKey(context.Background()); err != nil {
		t.Fatalf("a closed route is a network matter for the client, not a host key fault: %v", err)
	}
	var none *Route
	if err := none.VerifyHostKey(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAnEditKeepsThePinUnlessTheHostMovesOrANewKeyComes(t *testing.T) {
	for _, c := range []struct {
		name                    string
		stored, submitted, want string
		sameHost                bool
	}{
		{"a form that never showed the pin", "ssh-ed25519 AAA", "", "ssh-ed25519 AAA", true},
		{"a key confirmed in the form", "ssh-ed25519 AAA", "ssh-ed25519 BBB", "ssh-ed25519 BBB", true},
		{"moved to another host", "ssh-ed25519 AAA", "", "", false},
		{"moved with the new host's key", "ssh-ed25519 AAA", " ssh-ed25519 CCC ", "ssh-ed25519 CCC", false},
		{"a form that echoes the stored pin", "ssh-ed25519 AAA", "ssh-ed25519 AAA", "ssh-ed25519 AAA", true},
		{"moved to another name of the same bastion, trusted again", "ssh-ed25519 AAA", "ssh-ed25519 AAA", "ssh-ed25519 AAA", false},
	} {
		if got := PinAfterEdit(c.stored, c.submitted, c.sameHost); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
