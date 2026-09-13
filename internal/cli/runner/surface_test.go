package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func enrichedProto() protoFile {
	return protoFile{
		Package: "app",
		Services: []protoService{{
			Name:    "Healthz",
			Package: "healthz",
			RPCs: []protoRPC{
				{CommandName: "healthz:ping", RequestTSType: "PingRequest", ResultTSType: "PingReply", Arity: "unary"},
				{CommandName: "healthz:watch", RequestTSType: "WatchRequest", ResultTSType: "Sample",
					Arity: "server-stream", IsServerStream: true, Middlewares: []string{"Auth"}},
			},
			Events: []protoRPC{
				{EventName: "healthz:tick", EventGoName: "HealthzTick", EventDir: "out", RequestTSType: "Tick", IsEvent: true},
				{EventName: "healthz:ack", EventGoName: "HealthzAck", EventDir: "in", RequestTSType: "Ack", IsEvent: true},
			},
		}},
	}
}

func TestIPCSurfaceCarriesCommands(t *testing.T) {
	s := ipcSurfaceOf(enrichedProto())
	if s.Package != "app" || len(s.Services) != 1 {
		t.Fatalf("surface = %+v", s)
	}
	svc := s.Services[0]
	if svc.Wire != "healthz" {
		t.Errorf("wire prefix = %q, want healthz: every command of the service carries it", svc.Wire)
	}
	if len(svc.Commands) != 2 {
		t.Fatalf("commands = %+v", svc.Commands)
	}
	if got := svc.Commands[1]; got.Arity != "server-stream" || len(got.Middleware) != 1 {
		t.Errorf("stream command lost its arity or middleware: %+v", got)
	}
}

func TestIPCSurfaceMirrorsAnInboundEvent(t *testing.T) {
	svc := ipcSurfaceOf(enrichedProto()).Services[0]
	if len(svc.Events) != 2 {
		t.Fatalf("events = %+v", svc.Events)
	}
	out, in := svc.Events[0], svc.Events[1]
	if out.Go != "events.EmitHealthzTick" || out.Frontend != "events.onHealthzTick" {
		t.Errorf("outbound pair = %q / %q", out.Go, out.Frontend)
	}
	if in.Go != "events.OnHealthzAck" || in.Frontend != "events.emitHealthzAck" {
		t.Errorf("inbound pair = %q / %q, so an app would wire Emit for a JS -> Go topic", in.Go, in.Frontend)
	}
}

const surfaceTestProto = `syntax = "proto3";
package testapp;

message PingRequest {}
message PingReply { string status = 1; }
message TickEvent { int64 at = 1; }

service Healthz {
  rpc Ping (PingRequest) returns (PingReply);

  // @event
  rpc Tick (TickEvent) returns (PingReply);
}
`

// The whole point of the read-only twin: parse plus enrich with no go.mod, no
// templates and nothing written, on a directory that only holds a proto.
func TestSurfaceReadsAProtoAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "idl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "idl", "app.proto"), []byte(surfaceTestProto), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := Surface(context.Background(), SurfaceOptions{Dir: dir, JSONOut: &buf}); err != nil {
		t.Fatalf("surface: %v", err)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Proto   string `json:"proto"`
			Surface struct {
				Package  string `json:"package"`
				Services []struct {
					Wire     string `json:"wire"`
					Commands []struct {
						Command string `json:"command"`
						Arity   string `json:"arity"`
					} `json:"commands"`
					Events []struct {
						Topic     string `json:"topic"`
						Direction string `json:"direction"`
						Go        string `json:"go"`
					} `json:"events"`
				} `json:"services"`
			} `json:"surface"`
		} `json:"data"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("not an envelope: %v\n%s", err, buf.String())
	}
	if !env.OK || len(env.Data.Surface.Services) != 1 {
		t.Fatalf("envelope = %s", buf.String())
	}
	svc := env.Data.Surface.Services[0]
	if svc.Wire != "healthz" || len(svc.Commands) != 1 || svc.Commands[0].Command != "healthz:ping" {
		t.Errorf("commands = %+v", svc.Commands)
	}
	if len(svc.Events) != 1 || svc.Events[0].Topic != "healthz:tick" || svc.Events[0].Direction != "out" {
		t.Errorf("events = %+v", svc.Events)
	}
	if svc.Events[0].Go != "events.EmitHealthzTick" {
		t.Errorf("event helper = %q", svc.Events[0].Go)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "idl" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("surface wrote something: %v", names)
	}
}
