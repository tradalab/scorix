package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/tradalab/scorix/kernel/internal/sandbox"
	"github.com/tradalab/scorix/logger"
)

type Handler struct {
	Kind string
	Name string
	Exec func(context.Context, json.RawMessage) (any, error)
}

type envelope struct {
	ctx context.Context
	msg Message
}

type IPC struct {
	bridge   Bridge
	mu       sync.Mutex
	handlers map[string]Handler
	pending  map[string]chan Message

	queue chan envelope
}

func New(bridge Bridge) *IPC {
	return &IPC{
		bridge:   bridge,
		handlers: make(map[string]Handler),
		pending:  make(map[string]chan Message),
		queue:    make(chan envelope, 512),
	}
}

func (i *IPC) Bridge() Bridge {
	return i.bridge
}

func (i *IPC) AddHandlers(hs []Handler) {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, h := range hs {
		id := fmt.Sprintf("%s:%s", h.Kind, h.Name)
		i.handlers[id] = h
	}
}

func (i *IPC) Start() {
	i.bridge.OnMessage(i.On)
	go i.loop()
}

func (i *IPC) loop() {
	for env := range i.queue {
		go i.handle(env)
	}
}

func (i *IPC) handle(env envelope) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("IPC:handle - recovered", logger.Any("error", r))
			_ = i.bridge.Emit(env.ctx, Message{
				Id:    env.msg.Id,
				Kind:  env.msg.Kind,
				Name:  env.msg.Name,
				State: StateError,
				Error: fmt.Sprintf("panic: %v", r),
			})
		}
	}()

	ctx := env.ctx
	msg := env.msg

	key := fmt.Sprintf("%s:%s", msg.Kind, msg.Name)

	i.mu.Lock()
	handler, ok := i.handlers[key]
	i.mu.Unlock()

	if !ok {
		_ = i.bridge.Emit(ctx, Message{
			Id:    msg.Id,
			Kind:  msg.Kind,
			Name:  msg.Name,
			State: StateError,
			Error: "handler not found",
		})
		return
	}

	methodForSandbox := fmt.Sprintf("%s.%s", msg.Kind, msg.Name)
	if err := sandbox.Validate(methodForSandbox); err != nil {
		_ = i.bridge.Emit(ctx, Message{
			Id:    msg.Id,
			Kind:  msg.Kind,
			Name:  msg.Name,
			State: StateError,
			Error: err.Error(),
		})
		return
	}

	// processing state
	_ = i.bridge.Emit(ctx, Message{
		Id:    msg.Id,
		Kind:  msg.Kind,
		Name:  msg.Name,
		State: StateProcessing,
	})

	result, err := handler.Exec(ctx, msg.Data)

	if err != nil {
		_ = i.bridge.Emit(ctx, Message{
			Id:    msg.Id,
			Kind:  msg.Kind,
			Name:  msg.Name,
			State: StateError,
			Error: err.Error(),
		})
		return
	}

	data, _ := json.Marshal(result)

	_ = i.bridge.Emit(ctx, Message{
		Id:    msg.Id,
		Kind:  msg.Kind,
		Name:  msg.Name,
		State: StateDone,
		Data:  data,
	})
}

func (i *IPC) registerPending(id string, ch chan Message) {
	i.mu.Lock()
	i.pending[id] = ch
	i.mu.Unlock()
}

func (i *IPC) unregisterPending(id string) {
	i.mu.Lock()
	delete(i.pending, id)
	i.mu.Unlock()
}

func (i *IPC) getPending(id string) chan Message {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.pending[id]
}

func (i *IPC) RegisterPending(id string, ch chan Message) {
	i.registerPending(id, ch)
}

func (i *IPC) UnregisterPending(id string) {
	i.unregisterPending(id)
}

func (i *IPC) On(ctx context.Context, msg Message) Message {
	logger.Info("IPC:On()", logger.Any("msg", msg))
	switch msg.State {
	case StateDone, StateError, StateChunk:
		ch := i.getPending(msg.Id)
		if ch != nil {
			ch <- msg
		}
		if msg.State == StateDone || msg.State == StateError {
			i.unregisterPending(msg.Id)
		}
		return Message{}
	}

	// push to worker
	select {
	case i.queue <- envelope{ctx: ctx, msg: msg}:
	default:
		logger.Error("IPC queue full")
		return Message{
			Id:    msg.Id,
			Kind:  msg.Kind,
			Name:  msg.Name,
			State: StateError,
			Error: "IPC queue full",
		}
	}

	// ACK
	return Message{
		Id:    msg.Id,
		Kind:  msg.Kind,
		Name:  msg.Name,
		State: StateReceived,
	}
}

func (i *IPC) Invoke(ctx context.Context, msg Message) (Message, error) {
	msg.State = StateStart

	ch := make(chan Message, 1)

	i.mu.Lock()
	i.pending[msg.Id] = ch
	i.mu.Unlock()

	if err := i.bridge.Emit(ctx, msg); err != nil {
		return Message{}, err
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return Message{}, ctx.Err()
	}
}

func (i *IPC) Emit(ctx context.Context, msg Message) error {
	if msg.State == "" {
		msg.State = StateDispatch
	}
	return i.bridge.Emit(ctx, msg)
}
