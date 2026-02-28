package logger

import (
	"io"
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	log   *zap.Logger
	sugar *zap.SugaredLogger
)

func New(cfg Config) {
	// Level
	level := zap.InfoLevel
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		level = zap.InfoLevel
	}

	// Encoder
	var encoder zapcore.Encoder
	if cfg.Format == "json" {
		encoder = zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	} else {
		config := zap.NewDevelopmentEncoderConfig()
		config.EncodeLevel = zapcore.CapitalColorLevelEncoder
		encoder = zapcore.NewConsoleEncoder(config)
	}

	// Writer
	var writer io.Writer
	if cfg.Output == "both" {
		writer = io.MultiWriter(os.Stdout, fileWriter(cfg))
	} else {
		writer = fileWriter(cfg)
	}

	// Core
	core := zapcore.NewCore(encoder, zapcore.AddSync(writer), level)

	log = zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))
	sugar = log.Sugar()
}

func ensure() {
	if log == nil {
		New(Config{
			Level:   "info",
			Format:  "console",
			Output:  "stdout",
			File:    "logs/app.log",
			MaxSize: 10,
			MaxAge:  7,
		})
	}
}
