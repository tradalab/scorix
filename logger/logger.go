package logger

import (
	"io"
	"os"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	log         *zap.Logger
	sugar       *zap.SugaredLogger
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

	log = zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))
	sugar = log.Sugar()
}

func ensure() {
	if log != nil {
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
