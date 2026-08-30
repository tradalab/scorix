package proc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("PROC_HELPER_MODE")
	if mode == "" {
		return // running as a normal test: nothing to do
	}
	switch mode {
	case "serve":
		fmt.Println("helper: starting")
		fmt.Fprintln(os.Stderr, "helper: stderr line")
		if f := os.Getenv("PROC_HELPER_READY_FILE"); f != "" {
			_ = os.WriteFile(f, []byte("ready"), 0o600)
		}
		time.Sleep(time.Minute) // killed by the test's Stop
	case "crash":
		fmt.Println("helper: crashing")
		os.Exit(1)
	case "print":
		for i := 0; i < 10; i++ {
			fmt.Printf("line-%d\n", i)
		}
	}
	os.Exit(0)
}

func helperSpec(t *testing.T, mode string, extraEnv ...string) Spec {
	t.Helper()
	return Spec{
		Path: os.Args[0],
		Args: []string{"-test.run=^TestHelperProcess$"},
		Env: append(append(os.Environ(),
			"PROC_HELPER_MODE="+mode), extraEnv...),
	}
}

func TestHealthyStartAndStop(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	spec := helperSpec(t, "serve", "PROC_HELPER_READY_FILE="+ready)
	spec.Health = func(context.Context) error {
		if _, err := os.Stat(ready); err != nil {
			return err
		}
		return nil
	}
	spec.ReadyTimeout = 10 * time.Second
	spec.PollInterval = 20 * time.Millisecond

	p, err := Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if p.PID() == 0 {
		t.Fatal("PID = 0 for a running child")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("Done not closed after Stop")
	}
	if p.Err() != nil {
		t.Fatalf("Err after Stop = %v, want nil", p.Err())
	}

	logs := strings.Join(p.Logs(), "\n")
	if !strings.Contains(logs, "helper: starting") || !strings.Contains(logs, "helper: stderr line") {
		t.Fatalf("logs missing stdout/stderr capture:\n%s", logs)
	}
}

func TestRestartBudgetThenGiveUp(t *testing.T) {
	var exits atomic.Int32
	spec := helperSpec(t, "crash")
	spec.Restart = RestartPolicy{MaxRestarts: 2, Backoff: 10 * time.Millisecond}
	spec.OnExit = func(err error, willRestart bool) {
		exits.Add(1)
	}

	p, err := Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err) // no Health: start succeeds, then it crashes
	}
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("supervision never ended")
	}
	if got := exits.Load(); got != 3 { // initial + 2 restarts
		t.Fatalf("OnExit calls = %d, want 3", got)
	}
	if p.Err() == nil {
		t.Fatal("Err = nil after a crash with exhausted budget")
	}
}

func TestReadyTimeoutKillsChild(t *testing.T) {
	spec := helperSpec(t, "serve") // no ready file → health never passes
	spec.Health = func(context.Context) error { return errors.New("not yet") }
	spec.ReadyTimeout = 300 * time.Millisecond
	spec.PollInterval = 20 * time.Millisecond

	if _, err := Start(context.Background(), spec); err == nil {
		t.Fatal("Start must fail when health never passes")
	} else if !strings.Contains(err.Error(), "not healthy within") {
		t.Fatalf("err = %v", err)
	}
}

func TestRingBounds(t *testing.T) {
	r := newRing(3)
	for i := 0; i < 5; i++ {
		r.add(fmt.Sprintf("l%d", i))
	}
	got := strings.Join(r.lines(), ",")
	if got != "l2,l3,l4" {
		t.Fatalf("ring = %q", got)
	}
}
