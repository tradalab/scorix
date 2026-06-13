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
		maxBackups := cfg.MaxBackups
		if maxBackups <= 0 {
			maxBackups = 3 // preserve historical default when unset
		}
		return &lumberjack.Logger{
			Filename:   cfg.File,
			MaxSize:    cfg.MaxSize, // MB
			MaxAge:     cfg.MaxAge,  // days
			MaxBackups: maxBackups,
			LocalTime:  true,
			Compress:   true,
		}
	}
	return os.Stdout
}
