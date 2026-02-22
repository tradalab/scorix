package invoke

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/tradalab/scorix/kernel/core/ipc"
	"github.com/tradalab/scorix/kernel/internal/logger"
	"github.com/tradalab/scorix/kernel/internal/sandbox"
)

type HandlerFunc func(context.Context, json.RawMessage) (any, error)

var (
	mu       sync.RWMutex
	handlers = make(map[string]HandlerFunc)
)

func Register(method string, fn HandlerFunc) {
	mu.Lock()
	handlers[method] = fn
	mu.Unlock()
	logger.Info("invoke registered", logger.Str("method", method))
}

func HandleSync(ctx context.Context, payload []byte) (any, error) {
	var envelope ipc.Envelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, err
	}
	if envelope.Type != "invoke" {
		return nil, fmt.Errorf("not invoke")
	}

	var req ipc.InvokePayload
	if err := json.Unmarshal(envelope.Payload, &req); err != nil {
		return nil, err
	}

	if err := sandbox.Validate(req.Method); err != nil {
		return nil, err
	}

	mu.RLock()
	fn, ok := handlers[req.Method]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("method not found: %s", req.Method)
	}

	return fn(ctx, req.Params)
}
