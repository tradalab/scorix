package ipc

import "context"

// ClientID identifies one connected frontend endpoint (a native window or a
// WebSocket connection), used for targeted emits.
type ClientID int

type clientKey struct{}

func WithClient(ctx context.Context, id ClientID) context.Context {
	return context.WithValue(ctx, clientKey{}, id)
}

// ClientFrom reports the client id of the frontend that sent the current
// command/event; ok is false unless the dispatcher was bound to one.
func ClientFrom(ctx context.Context) (ClientID, bool) {
	id, ok := ctx.Value(clientKey{}).(ClientID)
	return id, ok
}
