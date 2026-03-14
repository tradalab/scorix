package logger

import "go.uber.org/zap"

var (
	Str      = zap.String
	Int      = zap.Int
	Bool     = zap.Bool
	Err      = zap.Error
	Any      = zap.Any
	Duration = zap.Duration
	Time     = zap.Time
	Float    = zap.Float64
)
