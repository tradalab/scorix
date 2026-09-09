package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func devEnvelope(t *testing.T, run func(out *bytes.Buffer) error) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	_ = run(&buf)
	var env map[string]any
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("not an envelope: %v\n%s", err, buf.String())
	}
	return env
}

func TestDevStopWithNoJobSaysSoInTheEnvelope(t *testing.T) {
	var job devJob
	env := devEnvelope(t, func(out *bytes.Buffer) error { return job.stop(context.Background(), out) })
	if env["ok"] != false {
		t.Fatalf("stopping nothing reported success: %v", env)
	}
	if env["command"] != "dev stop" {
		t.Fatalf("envelope names %v", env["command"])
	}
}

// Status is what an agent polls, so it must answer even when nothing was ever
// started - an error there reads as "the tool is broken", not "not running".
func TestDevStatusWithNoJobIsNotAnError(t *testing.T) {
	var job devJob
	env := devEnvelope(t, func(out *bytes.Buffer) error { return job.status(args{}, out) })
	if env["ok"] != true {
		t.Fatalf("status on an idle server failed: %v", env)
	}
	data, _ := env["data"].(map[string]any)
	if data["running"] != false {
		t.Fatalf("idle server reports running=%v", data["running"])
	}
}

// Two servers in one process used to share one dev job through package state.
func TestDevJobIsPerServer(t *testing.T) {
	a, b := NewServer("a"), NewServer("b")
	if &a.dev == &b.dev {
		t.Fatal("two servers share one dev job")
	}
}
