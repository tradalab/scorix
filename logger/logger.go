package logger

import (
	"io"
	"os"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	logPtr      atomic.Pointer[zap.Logger]
	sugarPtr    atomic.Pointer[zap.SugaredLogger]
	defaultOnce sync.Once
)

func New(cfg Config) {
	level := zap.InfoLevel
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		level = zap.InfoLevel
	}

	var encoder zapcore.Encoder
	if cfg.Format == "json" {
		encoder = zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	} else {
		config := zap.NewDevelopmentEncoderConfig()
		config.EncodeLevel = zapcore.CapitalColorLevelEncoder
		encoder = zapcore.NewConsoleEncoder(config)
	}

	var writer io.Writer
	if cfg.Output == "both" {
		writer = io.MultiWriter(os.Stdout, fileWriter(cfg))
	} else {
		writer = fileWriter(cfg)
	}

	core := zapcore.NewCore(encoder, zapcore.AddSync(writer), level)

	l := zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))
	logPtr.Store(l)
	sugarPtr.Store(l.Sugar())
}

func ensure() {
	if logPtr.Load() != nil {
		return
	}
	defaultOnce.Do(func() {
		New(Config{
			Level:   "info",
			Format:  "console",
			Output:  "stdout",
			File:    "logs/app.log",
			MaxSize: 10,
			MaxAge:  7,
		})
	})
}

func get() *zap.SugaredLogger {
	ensure()
	return sugarPtr.Load()
}
