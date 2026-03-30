package ipc

import (
	"context"
	"encoding/json"

	"github.com/tradalab/scorix/kernel/internal/window"
)

type AppBridge struct {
	name string
	wnd  window.Window
}

func NewAppBridge(wnd window.Window) *AppBridge {
	return &AppBridge{
		name: "__scorix__",
		wnd:  wnd,
	}
}

func (b *AppBridge) Name() string {
	return b.name
}

func (b *AppBridge) OnMessage(exec func(ctx context.Context, msg Message) Message) error {
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

func (b *AppBridge) Emit(ctx context.Context, msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	js := "window." + b.name + "ipc_receive(" + string(data) + ")"
	b.wnd.Eval(js)
	return nil
}
