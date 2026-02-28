package logger

import "go.uber.org/zap"

func Info(msg string, fields ...any)  { ensure(); sugar.Infow(msg, fields...) }
func Error(msg string, fields ...any) { ensure(); sugar.Errorw(msg, fields...) }
func Debug(msg string, fields ...any) { ensure(); sugar.Debugw(msg, fields...) }
func Warn(msg string, fields ...any)  { ensure(); sugar.Warnw(msg, fields...) }
func Fatal(msg string, fields ...any) { ensure(); sugar.Fatalw(msg, fields...) }

func With(fields ...any) *zap.SugaredLogger { ensure(); return sugar.With(fields...) }
