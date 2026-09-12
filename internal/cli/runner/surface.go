package runner

// Call after generateProto's enrichment loop: CommandName, EventName and the TS
// types are assigned there, not by the parser, so earlier is a surface of blanks.
func ipcSurfaceOf(pf protoFile) *IPCSurface {
	s := &IPCSurface{Package: pf.Package}
	for _, svc := range pf.Services {
		out := IPCService{Name: svc.Name, Wire: svc.Package}
		for _, rpc := range svc.RPCs {
			out.Commands = append(out.Commands, IPCCommand{
				Command:    rpc.CommandName,
				Request:    rpc.RequestTSType,
				Reply:      rpc.ResultTSType,
				Arity:      rpc.Arity,
				Middleware: rpc.Middlewares,
			})
		}
		for _, ev := range svc.Events {
			// An in event travels JS -> Go, so the pair mirrors: Go subscribes, frontend emits.
			goHelper, feHelper := "events.Emit"+ev.EventGoName, "events.on"+ev.EventGoName
			if ev.EventDir == "in" {
				goHelper, feHelper = "events.On"+ev.EventGoName, "events.emit"+ev.EventGoName
			}
			out.Events = append(out.Events, IPCEvent{
				Topic:     ev.EventName,
				Direction: ev.EventDir,
				Payload:   ev.RequestTSType,
				Go:        goHelper,
				Frontend:  feHelper,
			})
		}
		s.Services = append(s.Services, out)
	}
	return s
}
