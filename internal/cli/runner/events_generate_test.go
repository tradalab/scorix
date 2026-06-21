package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const eventsProto = `
syntax = "proto3";
package demo;

message Empty {}
message PingReq {}
message PingRes { string status = 1; }
message TickEvent { string connection_id = 1; int64 seq = 2; }
message NoteEvent { string text = 1; }

service healthz {
  rpc Ping (PingReq) returns (PingRes);
  // @event
  rpc Tick (TickEvent) returns (Empty);
}

// A service that carries ONLY events (no commands) must still generate.
service console {
  // @event in
  rpc Note (NoteEvent) returns (Empty);
  // @event
  rpc Output (NoteEvent) returns (Empty);
}
`

func TestParseProto_EventAnnotation(t *testing.T) {
	pf, err := parseProto(eventsProto)
	if err != nil {
		t.Fatalf("parseProto: %v", err)
	}
	byName := map[string]protoRPC{}
	for _, svc := range pf.Services {
		for _, r := range svc.RPCs {
			byName[svc.Name+"."+r.Name] = r
		}
	}
	if r := byName["healthz.Ping"]; r.IsEvent {
		t.Error("Ping must stay a command")
	}
	if r := byName["healthz.Tick"]; !r.IsEvent || r.EventDir != "out" {
		t.Errorf("Tick: want out-event, got IsEvent=%v dir=%q", r.IsEvent, r.EventDir)
	}
	if r := byName["console.Note"]; !r.IsEvent || r.EventDir != "in" {
		t.Errorf("Note: want in-event, got IsEvent=%v dir=%q", r.IsEvent, r.EventDir)
	}
	if r := byName["console.Output"]; !r.IsEvent || r.EventDir != "out" {
		t.Errorf("Output: want out-event, got IsEvent=%v dir=%q", r.IsEvent, r.EventDir)
	}
}

func TestGenerateProto_Events(t *testing.T) {
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

	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(b)
	}

	// Go event layer: topics + all three out-emit variants + typed in-subscriber.
	ev := read("internal/events/events.go")
	for _, want := range []string{
		`EventHealthzTick = "healthz:tick"`,
		`EventConsoleNote = "console:note"`,
		`EventConsoleOutput = "console:output"`,
		"func EmitHealthzTick(e Emitter, p *types.TickEvent)",
		"func EmitHealthzTickTo(e Emitter, client app.ClientID, p *types.TickEvent) bool",
		"func EmitHealthzTickCtx(ctx context.Context, e Emitter, p *types.TickEvent) bool",
		"func OnConsoleNote(reg Registrar, fn func(ctx context.Context, p *types.NoteEvent))",
	} {
		if !strings.Contains(ev, want) {
			t.Errorf("events.go missing %q", want)
		}
	}

	// Events must NOT be registered as commands.
	h := read("internal/handler/handler.go")
	if !strings.Contains(h, `reg(a, "healthz:ping"`) {
		t.Error("handler.go lost the ping command")
	}
	for _, banned := range []string{"healthz:tick", "console:note", "console:output"} {
		if strings.Contains(h, banned) {
			t.Errorf("handler.go registers event %q as a command", banned)
		}
	}
	// An event-only service contributes no commands — importing its (nonexistent)
	// logic package would break the build.
	if strings.Contains(h, "internal/logic/console") {
		t.Error("handler.go imports the logic package of an event-only service")
	}

	// No logic scaffold for events; event-only service has no logic dir at all.
	if _, err := os.Stat(filepath.Join(dir, "internal", "logic", "healthz", "tick_logic.go")); !os.IsNotExist(err) {
		t.Error("tick_logic.go must not be scaffolded for an event")
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "logic", "console")); !os.IsNotExist(err) {
		t.Error("event-only service must not get a logic dir")
	}

	// TS API: typed subscribe/emit; the event-only `console` service must NOT
	// produce `export const console` (would shadow the global).
	api := read("shell/api/index.ts")
	for _, want := range []string{"export const events", "onHealthzTick", "onConsoleOutput", "emitConsoleNote", `scorix.on("healthz:tick"`} {
		if !strings.Contains(api, want) {
			t.Errorf("api/index.ts missing %q", want)
		}
	}
	if strings.Contains(api, "export const console") {
		t.Error("api/index.ts exports `console` for an event-only service")
	}

	// Generated React hooks for out-events only.
	hooks := read("shell/hooks/events.ts")
	for _, want := range []string{"useHealthzTickEvent", "useConsoleOutputEvent"} {
		if !strings.Contains(hooks, want) {
			t.Errorf("hooks/events.ts missing %q", want)
		}
	}
	if strings.Contains(hooks, "useConsoleNoteEvent") {
		t.Error("hooks generated for an in-event")
	}
}
