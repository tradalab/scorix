package kernel

import (
	"context"
	"time"

	"github.com/tradalab/scorix/fault"
)

// Policy decides whether p holds permission; a nil error lets the call through.
type Policy interface {
	Allow(ctx context.Context, p Principal, permission string) error
}

type PolicyFunc func(ctx context.Context, p Principal, permission string) error

func (f PolicyFunc) Allow(ctx context.Context, p Principal, permission string) error {
	return f(ctx, p, permission)
}

// Rule is what the contract says about one procedure: a Timeout that replaces the
// service's own, the Permission a caller must hold, whether a retry carrying an
// Idempotency-Key gets the first outcome (Replay), and whether each call is audited.
type Rule struct {
	Timeout    time.Duration
	Permission string
	Replay     bool
	Audit      bool
}

// Admit asks pol whether p may make a call under r. A refusal is a fault.CodeDenied naming
// the permission, unless pol returned a *fault.Error of its own.
func (r Rule) Admit(ctx context.Context, p Principal, pol Policy) error {
	if r.Permission == "" {
		return nil
	}
	err := pol.Allow(ctx, p, r.Permission)
	if err == nil || fault.CodeOf(err) != "" {
		return err
	}
	return fault.Errorf(fault.CodeDenied, "this call needs %s: %v", r.Permission, err).With("permission", r.Permission)
}

// TimeoutOr is r.Timeout, or fallback when r sets none.
func (r Rule) TimeoutOr(fallback time.Duration) time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return fallback
}
