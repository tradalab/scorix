package logfwd

import (
	"context"

	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
)

type LogModule struct{}

func New() *LogModule { return &LogModule{} }

func (m *LogModule) Name() string    { return "log" }
func (m *LogModule) Version() string { return "1.0.0" }

func (m *LogModule) Capability() string { return "log" }

func (m *LogModule) OnLoad(ctx *module.Context) error {
	module.Expose(m, "Write", ctx.IPC)
	return nil
}

func (m *LogModule) OnStart() error  { return nil }
func (m *LogModule) OnStop() error   { return nil }
func (m *LogModule) OnUnload() error { return nil }

type WriteRequest struct {
	Level   string         `json:"level"` // debug | info | warn | error
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields"`
}

func (m *LogModule) Write(_ context.Context, req WriteRequest) (any, error) {
	kv := make([]any, 0, len(req.Fields)*2+2)
	for k, v := range req.Fields {
		kv = append(kv, k, v)
	}
	msg := "[shell] " + req.Message
	switch req.Level {
	case "debug":
		logger.Debug(msg, kv...)
	case "warn":
		logger.Warn(msg, kv...)
	case "error":
		logger.Error(msg, kv...)
	default:
		logger.Info(msg, kv...)
	}
	return nil, nil
}
