package resolve

import (
	"encoding/json"
	"fmt"

	"github.com/tradalab/scorix/core/ipc"
	"github.com/tradalab/scorix/internal/window"
)

func CallJS(win window.Window, name string, params any) {
	payload := ipc.ResolvePayload{
		Name:   name,
		Params: json.RawMessage(toJSON(params)),
	}
	envelope := ipc.Envelope{
		Type:    "resolve",
		Payload: json.RawMessage(toJSON(payload)),
	}
	js, _ := json.Marshal(envelope)
	script := fmt.Sprintf(`window.scorix._resolve(%s)`, js)
	win.Eval(script)
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
