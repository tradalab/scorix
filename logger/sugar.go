package logger

import "go.uber.org/zap"

func Info(msg string, fields ...any)  { get().Infow(msg, fields...) }
func Error(msg string, fields ...any) { get().Errorw(msg, fields...) }
func Debug(msg string, fields ...any) { get().Debugw(msg, fields...) }
func Warn(msg string, fields ...any)  { get().Warnw(msg, fields...) }
func Fatal(msg string, fields ...any) { get().Fatalw(msg, fields...) }

func With(fields ...any) *zap.SugaredLogger { return get().With(fields...) }
