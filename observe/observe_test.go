package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNewLoggerWritesJSONAtTheLevelAsked(t *testing.T) {
	var buf bytes.Buffer
	l, err := NewLogger(&buf, "warn")
	if err != nil {
		t.Fatal(err)
	}
	l.Info("quiet")
	l.Warn("loud", "k", "v")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil || line["msg"] != "loud" || line["k"] != "v" || strings.Contains(buf.String(), "quiet") {
		t.Fatalf("output %q", buf.String())
	}
	if _, err := NewLogger(&buf, "loud"); err == nil || !strings.Contains(err.Error(), `log level "loud"`) {
		t.Fatalf("bad level: %v", err)
	}
}

func TestTheContextCarriesItsLogger(t *testing.T) {
	if Log(context.Background()) != slog.Default() {
		t.Fatal("a bare context did not fall back to slog.Default")
	}
	var buf bytes.Buffer
	l, _ := NewLogger(&buf, "info")
	ctx := With(WithLogger(context.Background(), l), "procedure", "/a.B/C")
	Log(ctx).Info("hi")
	if !strings.Contains(buf.String(), `"procedure":"/a.B/C"`) {
		t.Fatalf("output %q", buf.String())
	}
}
