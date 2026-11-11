package kernel

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestLogAuditWritesOneLineNamingWhoDidWhatAndHowItEnded(t *testing.T) {
	var buf bytes.Buffer
	audit := LogAudit(slog.New(slog.NewJSONHandler(&buf, nil)))
	audit(context.Background(), Audit{Transport: "rest", Procedure: "/a.B/Make", Principal: Principal{Kind: User, ID: "u1"}, Request: wrapperspb.String("x"), Duration: 3 * time.Millisecond})
	audit(context.Background(), Audit{Transport: "connect", Procedure: "/a.B/Make", Err: errors.New("who are you")})
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %q", lines)
	}
	for _, want := range []string{`"msg":"audit"`, `"transport":"rest"`, `"procedure":"/a.B/Make"`, `"principal":"user:u1"`, `"request":"\"x\""`, `"ok":true`} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("first line lacks %s: %s", want, lines[0])
		}
	}
	for _, want := range []string{`"ok":false`, `"error":"who are you"`, `"principal":""`} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("refused line lacks %s: %s", want, lines[1])
		}
	}
}
