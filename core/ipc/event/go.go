package event

import (
	"encoding/json"
	"fmt"

	"github.com/tradalab/scorix/core/ipc"
	"github.com/tradalab/scorix/internal/window"
)

func PublishJS(win window.Window, topic string, data any) {
	envelope := ipc.Envelope{
		Type:    "event",
		Payload: json.RawMessage(fmt.Sprintf(`{"name":"%s","data":%s}`, topic, toJSON(data))),
	}
	js, _ := json.Marshal(envelope)
	script := fmt.Sprintf(`window.scorix._dispatch(%s)`, js)
	win.Eval(script)
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
