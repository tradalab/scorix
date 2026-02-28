package ipc

import "encoding/json"

type MessageState string

const (
	StateStart      MessageState = "start"
	StateReceived   MessageState = "received"
	StateProcessing MessageState = "processing"
	StateChunk      MessageState = "chunk"
	StateDone       MessageState = "done"
	StateError      MessageState = "error"
	StateCancel     MessageState = "cancel"
	StateDispatch   MessageState = "dispatch"
)

type Message struct {
	Id    string          `json:"id"`   // uuid
	Kind  string          `json:"kind"` // command | event | channel | ext
	Name  string          `json:"name"` // action name | event name
	State MessageState    `json:"state"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}
