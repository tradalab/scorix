package app

import (
	"context"

	ipc "github.com/tradalab/scorix/internal/ipc"
)

// Public re-exports so apps in other modules can name the handler types without
// importing scorix's internal packages.
type (
	// CmdFunc handles a request/reply command; it may push chunks via Stream.
	CmdFunc = ipc.CmdFunc
	// EvtFunc handles a one-way event from the frontend.
	EvtFunc = ipc.EvtFunc
	// Stream lets a command push incremental chunks before its final reply.
	Stream = ipc.Stream
	// ClientID identifies one connected frontend (a native window or one
	// WebSocket connection) for targeted emits via App.EmitTo.
	ClientID = ipc.ClientID
)

// ClientFrom reports which connected frontend sent the current command/event.
// Use the id with App.EmitTo to push follow-up events to that client only,
// instead of broadcasting with App.Emit.
func ClientFrom(ctx context.Context) (ClientID, bool) { return ipc.ClientFrom(ctx) }
