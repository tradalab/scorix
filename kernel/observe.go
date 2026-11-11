package kernel

import "context"

// Observer sees every call a transport serves, for traces and metrics. Start runs first, before
// authentication, with transport ("connect", "rest", "mcp") and the caller's headers; the func it
// returns runs once, with the error the call ended on.
type Observer interface {
	Start(ctx context.Context, transport string, call Call) (context.Context, func(err error))
}

// Observe is Start on o, or nothing when o is nil.
func Observe(ctx context.Context, o Observer, transport string, call Call) (context.Context, func(error)) {
	if o == nil {
		return ctx, func(error) {}
	}
	return o.Start(ctx, transport, call)
}
