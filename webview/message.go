package webview

import "encoding/json"

type Message struct {
	ID    string          `json:"id"`
	Kind  string          `json:"kind"` // command | event | module | rpc | push
	Name  string          `json:"name"`
	State string          `json:"state"`           // see the Kind* / State* constants
	Error string          `json:"error,omitempty"` // set when State == error / StateError
	Data  json.RawMessage `json:"data,omitempty"`
}

// Legacy command/event/module kinds coexist with v2 rpc/push during migration.
const (
	KindCommand = "command"
	KindEvent   = "event"
	KindModule  = "module"
	KindRPC     = "rpc"  // unified arity call (unary, server/client/bidi stream)
	KindPush    = "push" // callerless server->client broadcast
)

// RPC wire states. A call is two frame streams keyed by ID: client opens/data/
// (end|cancel); server replies msg* then (done|error).
const (
	StateOpen   = "open"   // C->S: begin a call (Data may carry the first message)
	StateData   = "data"   // C->S: a client->server message
	StateEnd    = "end"    // C->S: client half-close (Recv observes EOF)
	StateCancel = "cancel" // C->S: abort the call (handler ctx is canceled)

	StateMsg   = "msg"   // S->C: a server->client message
	StateDone  = "done"  // S->C: call completed successfully
	StateError = "error" // S->C: call failed (Error set)
)
