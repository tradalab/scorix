package runner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func touch(t *testing.T, path, content string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestWatchSetScan(t *testing.T) {
	root := t.TempDir()
	t0 := time.Now().Add(-time.Hour)
	touch(t, filepath.Join(root, "main.go"), "package main", t0)
	touch(t, filepath.Join(root, "internal", "logic", "a_logic.go"), "package logic", t0)
	touch(t, filepath.Join(root, "internal", "logic", "a_logic_test.go"), "package logic", t0)
	touch(t, filepath.Join(root, "shell", "x.go"), "package shell", t0)
	touch(t, filepath.Join(root, "idl", "app.proto"), "syntax", t0)
	touch(t, filepath.Join(root, "README.md"), "x", t0)

	ws := newWatchSet(root, filepath.Join(root, "idl", "app.proto"))
	first := ws.scan()
	sort.Strings(first)
	want := []string{"idl/app.proto", "internal/logic/a_logic.go", "main.go"}
	if strings.Join(first, ",") != strings.Join(want, ",") {
		t.Fatalf("baseline = %v, want %v (shell/, _test.go and .md must be ignored)", first, want)
	}
	if got := ws.scan(); len(got) != 0 {
		t.Fatalf("quiet scan reported %v", got)
	}

	touch(t, filepath.Join(root, "internal", "logic", "a_logic.go"), "package logic // edited", t0.Add(time.Minute))
	touch(t, filepath.Join(root, "idl", "app.proto"), "syntax2", t0.Add(time.Minute))
	if err := os.Remove(filepath.Join(root, "main.go")); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(root, "new.go"), "package main", t0)
	got := ws.scan()
	sort.Strings(got)
	want = []string{"idl/app.proto", "internal/logic/a_logic.go", "main.go", "new.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("changed = %v, want %v", got, want)
	}
}

type fakeHooks struct {
	mu       sync.Mutex
	regen    []string
	builds   atomic.Int32
	restart  atomic.Int32
	buildErr atomic.Pointer[error]
	done     chan struct{}
}

func (f *fakeHooks) setBuildErr(err error) { f.buildErr.Store(&err) }

func (f *fakeHooks) regens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.regen...)
}

func (f *fakeHooks) hooks(out *syncBuffer) devHooks {
	return devHooks{
		regenerate: func(proto, schema bool) error {
			f.mu.Lock()
			f.regen = append(f.regen, map[bool]string{true: "p", false: "-"}[proto]+map[bool]string{true: "s", false: "-"}[schema])
			f.mu.Unlock()
			return nil
		},
		build: func() error {
			f.builds.Add(1)
			if p := f.buildErr.Load(); p != nil {
				return *p
			}
			return nil
		},
		restart:  func() error { f.restart.Add(1); return nil },
		appDone:  func() <-chan struct{} { return f.done },
		poll:     10 * time.Millisecond,
		debounce: 10 * time.Millisecond,
		out:      out,
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestDevLoopRebuildsAndKeepsAppOnFailure(t *testing.T) {
	root := t.TempDir()
	t0 := time.Now().Add(-time.Hour)
	touch(t, filepath.Join(root, "main.go"), "package main", t0)
	touch(t, filepath.Join(root, "idl", "app.proto"), "v1", t0)
	ws := newWatchSet(root, filepath.Join(root, "idl", "app.proto"))
	ws.scan()

	f := &fakeHooks{done: make(chan struct{})}
	out := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan error, 1)
	go func() { loopDone <- devLoop(ctx, ws, "idl/app.proto", "etc/schema.sql", f.hooks(out)) }()

	waitFor := func(cond func() bool, what string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timeout waiting for %s\n%s", what, out.String())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	touch(t, filepath.Join(root, "main.go"), "package main // v2", t0.Add(time.Minute))
	waitFor(func() bool { return f.restart.Load() == 1 }, "first restart")
	if f.builds.Load() != 1 || len(f.regens()) != 0 {
		t.Fatalf("builds=%d regen=%v after a .go edit", f.builds.Load(), f.regens())
	}

	f.setBuildErr(errors.New("syntax error"))
	touch(t, filepath.Join(root, "main.go"), "package main // v3", t0.Add(2*time.Minute))
	waitFor(func() bool { return f.builds.Load() == 2 }, "second build")
	time.Sleep(30 * time.Millisecond)
	if f.restart.Load() != 1 {
		t.Fatalf("restart=%d: a failed build must keep the previous app running", f.restart.Load())
	}
	if !strings.Contains(out.String(), "keeping the previous app") {
		t.Fatalf("no keep-alive notice:\n%s", out.String())
	}

	f.setBuildErr(nil)
	touch(t, filepath.Join(root, "idl", "app.proto"), "v2", t0.Add(3*time.Minute))
	waitFor(func() bool { return f.restart.Load() == 2 }, "restart after proto change")
	if r := f.regens(); len(r) != 1 || r[0] != "p-" {
		t.Fatalf("regen=%v, want one proto-only regeneration", r)
	}

	close(f.done)
	select {
	case err := <-loopDone:
		if err != nil {
			t.Fatalf("devLoop after app exit = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("devLoop did not return after the app exited")
	}
	cancel()
}

func TestDevGoBuildAndBinarySlots(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module devsmoke\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".scorix", "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := devBinary(root, 1)
	if runtime.GOOS == "windows" && !strings.HasSuffix(out, "app-1.exe") {
		t.Fatalf("binary slot = %s", out)
	}
	if devBinary(root, 3) != out || devBinary(root, 2) == out {
		t.Fatal("slots must alternate by generation parity")
	}
	var log bytes.Buffer
	if err := devGoBuild(context.Background(), root, out, nil, &log); err != nil {
		t.Fatalf("devGoBuild: %v\n%s", err, log.String())
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("binary missing: %v", err)
	}
}
