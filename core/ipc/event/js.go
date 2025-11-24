package event

import (
	"encoding/json"

	"github.com/tradalab/scorix/core/ipc"
	"github.com/tradalab/scorix/internal/window"
)

func HandleJS(win window.Window, payload []byte) {
	var envelope ipc.Envelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return
	}
	if envelope.Type != "event" {
		return
	}
	var req ipc.EventPayload
	if err := json.Unmarshal(envelope.Payload, &req); err != nil {
		return
	}
	Publish(req.Name, json.RawMessage(req.Data))
}
