package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Audit is one call recorded for who changed what: the transport it came over, the
// Principal (zero when it was refused before anyone was named), the Request with PII
// masked and SECRET fields cleared, how long it took, and the error it ended with.
type Audit struct {
	Transport string
	Procedure string
	Principal Principal
	Request   proto.Message
	Duration  time.Duration
	Err       error
}

// Auditor receives every call whose Rule sets Audit, refused ones included, after it ends.
type Auditor func(ctx context.Context, a Audit)

// LogAudit writes each Audit as one "audit" line to l.
func LogAudit(l *slog.Logger) Auditor {
	return func(ctx context.Context, a Audit) {
		principal := ""
		if a.Principal.Kind != "" {
			principal = string(a.Principal.Kind) + ":" + a.Principal.ID
		}
		attrs := []slog.Attr{
			slog.String("transport", a.Transport), slog.String("procedure", a.Procedure), slog.String("principal", principal),
			slog.Duration("duration", a.Duration), slog.Bool("ok", a.Err == nil),
		}
		if a.Request != nil {
			if b, err := protojson.Marshal(a.Request); err == nil {
				var compact bytes.Buffer
				if json.Compact(&compact, b) == nil {
					attrs = append(attrs, slog.String("request", compact.String()))
				}
			}
		}
		if a.Err != nil {
			attrs = append(attrs, slog.String("error", a.Err.Error()))
		}
		l.LogAttrs(ctx, slog.LevelInfo, "audit", attrs...)
	}
}
