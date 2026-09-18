package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// The loop polls mtime+size, so a rewrite of unchanged files costs a restart.
func TestDevLoopRebuildsOncePerRealChangeAndNeverForAQuietGenerate(t *testing.T) {
	dir := newCheckProject(t)
	ctx := context.Background()
	if err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := GenerateModel(ctx, GenerateModelOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}

	protoPath := filepath.Join(dir, "idl", "app.proto")
	schemaPath := filepath.Join(dir, "idl", "schema.sql")
	ws := newWatchSet(dir, protoPath, schemaPath)
	ws.scan() // baseline

	var builds atomic.Int32
	hooks := devHooks{
		regenerate: func(proto, schema bool) error {
			if proto {
				if err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir}); err != nil {
					return err
				}
			}
			if schema {
				return GenerateModel(ctx, GenerateModelOptions{Dir: dir})
			}
			return nil
		},
		build:    func() error { builds.Add(1); return nil },
		restart:  func() error { return nil },
		appDone:  func() <-chan struct{} { return make(chan struct{}) },
		poll:     20 * time.Millisecond,
		debounce: 20 * time.Millisecond,
		out:      io.Discard,
	}
	loopCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() { _ = devLoop(loopCtx, ws, "idl/app.proto", "idl/schema.sql", hooks) }()

	// A generate with unchanged inputs, the way a second terminal would run it.
	if err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := GenerateModel(ctx, GenerateModelOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if n := builds.Load(); n != 0 {
		t.Fatalf("a generate that changed nothing cost %d rebuild(s)", n)
	}

	proto, err := os.ReadFile(protoPath)
	if err != nil {
		t.Fatal(err)
	}
	edited := string(proto) + "\nmessage Added { string note = 1; }\n"
	if err := os.WriteFile(protoPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for builds.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if builds.Load() == 0 {
		t.Fatal("editing the proto never reached a rebuild")
	}
	// The files that regenerate touched must not come back as a second change.
	time.Sleep(400 * time.Millisecond)
	if n := builds.Load(); n != 1 {
		t.Errorf("one proto edit cost %d rebuilds", n)
	}
}
