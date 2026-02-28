package ipc

import (
	"context"
	"encoding/json"

	"github.com/tradalab/scorix/kernel/internal/window"
)

type JSBridge struct {
	name string
	wnd  window.Window
}

func NewJSBridge(wnd window.Window) JSBridge {
	return JSBridge{
		name: "__scorix__",
		wnd:  wnd,
	}
}

func (b *JSBridge) Name() string {
	return b.name
}

func (b *JSBridge) OnMessage(exec func(ctx context.Context, msg Message) Message) error {
	return b.wnd.Bind(b.name+"ipc_emit", func(raw string) any {
		ctx := context.Background()

		var msg Message
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			return Message{
				Id:    msg.Id,
				Kind:  msg.Kind,
				Name:  msg.Name,
				State: StateError,
				Error: err.Error(),
			}
		}

		result := exec(ctx, msg)

		return result
	})
}

func (b *JSBridge) Emit(ctx context.Context, msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	js := "window." + b.name + "ipc_receive(" + string(data) + ")"
	b.wnd.Eval(js)
	return nil
}
