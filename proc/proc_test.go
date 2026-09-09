package proc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	case "heartbeat":
		// Counts up in a file so a test can tell alive from dead without asking
		// the OS about a pid, which has no portable answer.
		beat := os.Getenv("PROC_HELPER_BEAT")
		for i := 0; i < 1200; i++ { // 60s, so a leak cannot outlive the run by much
			_ = os.WriteFile(beat, []byte(strconv.Itoa(i)), 0o600)
			time.Sleep(50 * time.Millisecond)
		}
	case "parent":
		// Starts a supervised child and then does nothing, so the test can kill
		// this process the way an agent disappearing kills the MCP server.
		// Env built by hand rather than appended over os.Environ(): a duplicate
		// PROC_HELPER_MODE that resolved to "parent" instead of "heartbeat" would
		// spawn parents forever.
		if _, err := Start(context.Background(), Spec{
			Path: os.Args[0],
			Args: []string{"-test.run=^TestHelperProcess$"},
			Env:  helperEnv("heartbeat", "PROC_HELPER_BEAT="+os.Getenv("PROC_HELPER_BEAT")),
		}); err != nil {
			fmt.Fprintln(os.Stderr, "parent: start:", err)
			os.Exit(1)
		}
		time.Sleep(30 * time.Second) // bounded: the test kills this long before
	case "print":
		for i := 0; i < 10; i++ {
			fmt.Printf("line-%d\n", i)
		}
	}
	os.Exit(0)
}

// helperEnv drops any PROC_HELPER_* the current process carries before setting
// the mode, so a helper can never inherit its parent's role.
func helperEnv(mode string, extra ...string) []string {
	out := make([]string, 0, len(os.Environ())+len(extra)+1)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PROC_HELPER_") {
			out = append(out, kv)
		}
	}
	out = append(out, "PROC_HELPER_MODE="+mode)
	return append(out, extra...)
}

func helperSpec(t *testing.T, mode string, extraEnv ...string) Spec {
	t.Helper()
	return Spec{
		Path: os.Args[0],
		Args: []string{"-test.run=^TestHelperProcess$"},
		Env:  helperEnv(mode, extraEnv...),
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

// beat reads the counter the heartbeat helper writes; -1 means it has not
// written yet.
func beat(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return -1
	}
	return n
}

// A supervised child that outlives the process which started it is the whole
// point of the Job Object (Windows) and Pdeathsig (Linux) wiring, and nothing
// exercised it until this test. Kills the parent outright, so no deferred
// release() gets to help.
func TestChildDiesWhenItsParentIsKilled(t *testing.T) {
	if orphanNet == "" {
		t.Skip("no orphan net on this platform: a supervised child does outlive an abrupt parent here")
	}
	beatFile := filepath.Join(t.TempDir(), "beat")

	parent := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	parent.Env = helperEnv("parent", "PROC_HELPER_BEAT="+beatFile)
	if err := parent.Start(); err != nil {
		t.Fatalf("start parent: %v", err)
	}
	defer func() {
		_ = parent.Process.Kill()
		_, _ = parent.Process.Wait()
	}()

	// Precondition, without which "the counter stopped" would also be the reading
	// for a grandchild that never ran at all.
	first := waitForBeat(t, beatFile, -1, 20*time.Second)

	if err := parent.Process.Kill(); err != nil {
		t.Fatalf("kill parent: %v", err)
	}
	_, _ = parent.Process.Wait()

	deadline := time.Now().Add(20 * time.Second)
	last, stableSince := first, time.Time{}
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		now := beat(t, beatFile)
		if now != last {
			last, stableSince = now, time.Time{}
			continue
		}
		if stableSince.IsZero() {
			stableSince = time.Now()
		}
		if time.Since(stableSince) > 2*time.Second {
			return // stopped counting: the child went with its parent
		}
	}
	t.Fatalf("child kept running %ds after its parent was killed (counter %d)",
		20, beat(t, beatFile))
}

func waitForBeat(t *testing.T, path string, notThis int, within time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if n := beat(t, path); n != notThis && n >= 0 {
			return n
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no heartbeat within %s: the grandchild never started, so this test would prove nothing", within)
	return -1
}
