package ipc

import "context"

type Bridge interface {
	Name() string
	OnMessage(exec func(ctx context.Context, msg Message) Message) error
	Emit(ctx context.Context, msg Message) error
}
