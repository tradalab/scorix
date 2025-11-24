package logger

import (
	"io"
	"os"

	"gopkg.in/natefinch/lumberjack.v2"
)

func fileWriter(cfg Config) io.Writer {
	if cfg.Output == "stdout" {
		return os.Stdout
	}
	if cfg.Output == "file" || cfg.Output == "both" {
		return &lumberjack.Logger{
			Filename:   cfg.File,
			MaxSize:    cfg.MaxSize, // MB
			MaxAge:     cfg.MaxAge,  // days
			MaxBackups: 3,
			LocalTime:  true,
			Compress:   true,
		}
	}
	return os.Stdout
}
