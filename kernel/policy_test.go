package kernel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tradalab/scorix/fault"
)

func TestAdmitAsksThePolicyOnlyWhenAPermissionIsNamed(t *testing.T) {
	var asked []string
	pol := PolicyFunc(func(_ context.Context, p Principal, perm string) error {
		asked = append(asked, p.ID+"/"+perm)
		if perm == "things.write" {
			return errors.New("not an editor")
		}
		return nil
	})
	ana := Principal{Kind: User, ID: "ana"}
	if err := (Rule{}).Admit(context.Background(), ana, nil); err != nil {
		t.Fatalf("a rule with no permission refused: %v", err)
	}
	if err := (Rule{Permission: "things.read"}).Admit(context.Background(), ana, pol); err != nil {
		t.Fatalf("an allowed permission refused: %v", err)
	}
	err := (Rule{Permission: "things.write"}).Admit(context.Background(), ana, pol)
	if fault.CodeOf(err) != fault.CodeDenied || !strings.Contains(err.Error(), "needs things.write: not an editor") {
		t.Fatalf("refusal = %v", err)
	}
	if strings.Join(asked, ",") != "ana/things.read,ana/things.write" {
		t.Fatalf("policy asked %v", asked)
	}
}

func TestAPolicysOwnFaultKeepsItsCode(t *testing.T) {
	pol := PolicyFunc(func(context.Context, Principal, string) error {
		return fault.New(fault.CodeUnavailable, "policy store down")
	})
	if err := (Rule{Permission: "x"}).Admit(context.Background(), Principal{}, pol); fault.CodeOf(err) != fault.CodeUnavailable {
		t.Fatalf("err = %v, want the policy's own unavailable", err)
	}
}

func TestARulesTimeoutReplacesTheFallbackOnlyWhenSet(t *testing.T) {
	if (Rule{}).TimeoutOr(time.Second) != time.Second || (Rule{Timeout: time.Millisecond}).TimeoutOr(time.Second) != time.Millisecond {
		t.Fatal("TimeoutOr picked the wrong one")
	}
}
