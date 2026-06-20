package app

import (
	"context"

	ipc "github.com/tradalab/scorix/internal/ipc"
)

// Re-exports so apps can name the handler types without importing internal pkgs.
type (
	CmdFunc     = ipc.CmdFunc
	EvtFunc     = ipc.EvtFunc
	ChunkStream = ipc.Stream   // legacy v1 progress channel; v2 uses Stream[In,Out]
	ClientID    = ipc.ClientID // one connected frontend (native window or WS conn) for App.EmitTo
)

// ClientFrom reports which frontend sent the current command/event, for EmitTo targeting.
func ClientFrom(ctx context.Context) (ClientID, bool) { return ipc.ClientFrom(ctx) }
