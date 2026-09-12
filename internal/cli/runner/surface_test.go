package runner

import "testing"

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
