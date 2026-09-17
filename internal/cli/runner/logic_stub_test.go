package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnaryLogicStubNeverRepliesNull(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "idl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "idl", "app.proto"), []byte(eventsProto), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatalf("GenerateProto: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "internal", "logic", "healthz", "ping_logic.go"))
	if err != nil {
		t.Fatal(err)
	}
	stub := string(b)
	// A nil pointer reaches JS as null, which the generated PingRes type does not admit, and the
	// demo page of every scaffold reads reply.status on the first run.
	if strings.Contains(stub, "return nil, nil") {
		t.Errorf("unary stub replies null:\n%s", stub)
	}
	if !strings.Contains(stub, "return new(types.PingRes), nil") {
		t.Errorf("unary stub does not reply with its declared result type:\n%s", stub)
	}
}
