// Package kernel holds what every transport and host agree on about a call: who makes it
// and how that is decided. It imports nothing above the base tier.
package kernel

import (
	"context"
	"net/http"

	"github.com/tradalab/scorix/observe"
)

type Kind string

const (
	Anonymous Kind = "anonymous"
	User      Kind = "user"
	Service   Kind = "service"
	Agent     Kind = "agent"
)

// Principal is the caller a transport established; the zero value is no caller at all.
type Principal struct {
	Kind Kind
	ID   string
}

type principalKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom reports false when no Authenticator ran for this call.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Call is what an Authenticator sees of a request before its body is read.
type Call struct {
	Procedure string
	Header    http.Header
	// Anonymous is true when the contract lets this procedure run with no signed-in caller;
	// the Authenticator then names a kernel.Anonymous principal instead of refusing.
	Anonymous bool
}

// Authenticator decides who makes a call. The context it returns is the one the handler
// runs with; an error refuses the call and is sent to the caller as is.
type Authenticator interface {
	Authenticate(ctx context.Context, call Call) (context.Context, error)
}

// Enter marks ctx as running procedure for p: every line logged through observe.Log(ctx)
// names both. Transports call it once the Authenticator accepted the call.
func Enter(ctx context.Context, procedure string, p Principal) context.Context {
	return observe.With(ctx, "procedure", procedure, "principal", string(p.Kind)+":"+p.ID)
}

type AuthenticatorFunc func(ctx context.Context, call Call) (context.Context, error)

func (f AuthenticatorFunc) Authenticate(ctx context.Context, call Call) (context.Context, error) {
	return f(ctx, call)
}
