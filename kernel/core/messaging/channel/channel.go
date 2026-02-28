package channel

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tradalab/scorix/kernel/internal/ipc"
)

const Kind = "channel"

type Channel struct {
	ipc *ipc.IPC
}

func New(core *ipc.IPC) *Channel {
	return &Channel{ipc: core}
}

func (c *Channel) Handle(name string, fn func(ctx context.Context, payload json.RawMessage, send func(any) error) error) {
	c.ipc.AddHandlers([]ipc.Handler{
		{
			Kind: Kind,
			Name: name,
			Exec: func(ctx context.Context, payload json.RawMessage) (any, error) {
				// not used directly
				return nil, nil
			},
		},
	})
}

func (c *Channel) Open(ctx context.Context, id string, name string, payload any, onChunk func(json.RawMessage)) error {
	data, _ := json.Marshal(payload)
	msg := ipc.Message{
		Id:   id,
		Kind: Kind,
		Name: name,
		Data: data,
	}
	ch := make(chan ipc.Message, 8)
	c.ipc.RegisterPending(id, ch)
	if err := c.ipc.Emit(ctx, msg); err != nil {
		return err
	}
	for {
		select {
		case m := <-ch:
			switch m.State {
			case ipc.StateChunk:
				onChunk(m.Data)
			case ipc.StateDone:
				return nil
			case ipc.StateError:
				return fmt.Errorf(m.Error)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
