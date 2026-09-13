package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/tradalab/scorix/proc"
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

// A stand-in for the dev loop: it prints once so the log ring has something to
// return, then waits to be killed.
func TestDevHelperProcess(t *testing.T) {
	if os.Getenv("SCORIX_DEV_HELPER") == "" {
		return
	}
	fmt.Println("dev helper up")
	time.Sleep(2 * time.Minute)
}

// What it does NOT prove: that the OS process is gone afterwards. `stop` clears
// the handle, and on Windows os.FindProcess succeeds for a dead pid, so there is
// no portable liveness probe here. Killing is proc's contract and proc tests it.
func TestDevStartThenStopDrivesARealChild(t *testing.T) {
	var job devJob
	job.child = func(dir string, _ []string) proc.Spec {
		return proc.Spec{
			Path:     os.Args[0],
			Args:     []string{"-test.run=^TestDevHelperProcess$"},
			Dir:      dir,
			Env:      append(os.Environ(), "SCORIX_DEV_HELPER=1"),
			LogLines: 50,
		}
	}

	env := devEnvelope(t, func(out *bytes.Buffer) error { return job.start(context.Background(), args{"dir": "."}, out) })
	if env["ok"] != true {
		t.Fatalf("start failed: %v", env)
	}
	data, _ := env["data"].(map[string]any)
	if data["running"] != true {
		t.Fatalf("start reports running=%v", data["running"])
	}
	if pid, _ := data["pid"].(float64); pid <= 0 {
		t.Fatalf("start reported no pid: %v", data)
	}

	// The tail is the reason status exists: an agent reads compile errors there.
	var tail string
	for i := 0; i < 50; i++ {
		st := devEnvelope(t, func(out *bytes.Buffer) error { return job.status(args{"lines": float64(10)}, out) })
		d, _ := st["data"].(map[string]any)
		lines, _ := d["log"].([]any)
		tail = fmt.Sprint(lines...)
		if len(lines) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if tail == "" {
		t.Error("status returned no log tail, so a compile error would be invisible")
	}

	start := time.Now()
	stopped := devEnvelope(t, func(out *bytes.Buffer) error { return job.stop(context.Background(), out) })
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("stop took %s: it waited on a child that ignores termination", elapsed)
	}
	if stopped["ok"] != true {
		t.Fatalf("stop failed: %v", stopped)
	}
	sd, _ := stopped["data"].(map[string]any)
	if sd["running"] != false {
		t.Errorf("stop still reports running=%v", sd["running"])
	}

	after := devEnvelope(t, func(out *bytes.Buffer) error { return job.status(args{}, out) })
	ad, _ := after["data"].(map[string]any)
	if ad["running"] != false {
		t.Errorf("status after stop reports running=%v", ad["running"])
	}
}
