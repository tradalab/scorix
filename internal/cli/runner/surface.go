package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type SurfaceOptions struct {
	Dir     string
	Proto   string
	JSONOut io.Writer // non-nil switches the result to one JSON document on this writer
}

type SurfaceResult struct {
	Proto   string      `json:"proto"`
	Surface *IPCSurface `json:"surface"`
}

// Read-only twin of `generate proto`: same parse, same enrichment, no writes and
// no drift verdict. An agent asking what the frontend may call should not have to
// run a command whose name says it edits files.
func Surface(_ context.Context, opt SurfaceOptions) error {
	res := &SurfaceResult{}
	err := surfaceOf(opt, res)
	if opt.JSONOut != nil {
		return EmitJSON(opt.JSONOut, "surface", res, err)
	}
	if err != nil {
		return err
	}
	printSurface(res)
	return nil
}

func surfaceOf(opt SurfaceOptions, res *SurfaceResult) error {
	if opt.Proto == "" {
		opt.Proto = "idl/app.proto"
	}
	if opt.Dir == "" {
		opt.Dir = "."
	}
	root, err := filepath.Abs(opt.Dir)
	if err != nil {
		return err
	}
	cfg, _ := loadProjectConfig(filepath.Join(root, "scorix.yaml"))
	protoPath := resolveProtoPath(root, opt.Proto, cfg)

	pf, err := readProto(protoPath)
	if err != nil {
		return err
	}
	if _, _, err := enrichProto(&pf); err != nil {
		return err
	}
	if rel, relErr := filepath.Rel(root, protoPath); relErr == nil {
		protoPath = filepath.ToSlash(rel)
	}
	res.Proto, res.Surface = protoPath, ipcSurfaceOf(pf)
	return nil
}

func printSurface(res *SurfaceResult) {
	fmt.Printf("==> IPC surface from %s\n", res.Proto)
	for _, svc := range res.Surface.Services {
		fmt.Printf("  %s (%s)\n", svc.Name, svc.Wire)
		for _, c := range svc.Commands {
			fmt.Printf("    %-32s %s -> %s  %s\n", c.Command, c.Request, c.Reply, c.Arity)
		}
		for _, e := range svc.Events {
			fmt.Printf("    %-32s %s  event %s\n", e.Topic, e.Payload, e.Direction)
		}
	}
}

// Flag default yields to the manifest's proto: key; an explicit --proto wins. A
// missing or unreadable manifest keeps the default.
func resolveProtoPath(root, flag string, cfg *ProjectConfig) string {
	p := flag
	if p == "idl/app.proto" && cfg != nil && cfg.Proto != "" {
		p = cfg.Proto
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	return p
}

func readProto(path string) (protoFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return protoFile{}, fmt.Errorf("read proto: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return protoFile{}, fmt.Errorf("proto file is empty: %s", path)
	}
	pf, err := parseProto(string(data))
	if err != nil {
		return protoFile{}, err
	}
	if len(pf.Services) == 0 {
		return protoFile{}, fmt.Errorf("no service found in proto: %s", path)
	}
	return pf, nil
}

// The names every generated artifact and the surface agree on are assigned here,
// not by the parser: call this before reading CommandName, EventName or the TS
// types off an rpc.
func enrichProto(pf *protoFile) (outEvents, inEvents []protoRPC, err error) {
	for i := range pf.Services {
		svc := &pf.Services[i]
		svc.Package = lowerCamel(svc.Name)
		svcExported := exportedName(svc.Name)
		commands := svc.RPCs[:0]
		for j := range svc.RPCs {
			rpc := svc.RPCs[j]
			// A stream keyword on a one-way event would be silently discarded.
			if rpc.IsEvent && rpc.Arity != "unary" {
				return nil, nil, fmt.Errorf("rpc %s.%s is @event/@broadcast and cannot also be a %s; drop the stream keyword or the annotation", svc.Name, rpc.Name, rpc.Arity)
			}
			if !rpc.IsEvent && (rpc.Arity == "client-stream" || rpc.Arity == "bidi") {
				return nil, nil, fmt.Errorf("rpc %s.%s uses the %s arity; codegen emits only unary and server-stream - wire %s by hand with app.RegisterDuplex", svc.Name, rpc.Name, rpc.Arity, rpc.Name)
			}
			// Dropping the opt-in silently is how an app ends up believing a tool is exposed.
			if rpc.MCP && rpc.IsEvent {
				return nil, nil, fmt.Errorf("rpc %s.%s is both @mcp and @event/@broadcast; a domain tool is a request with a reply and an event is neither, so drop one", svc.Name, rpc.Name)
			}
			if rpc.MCP && rpc.Arity != "unary" {
				return nil, nil, fmt.Errorf("rpc %s.%s is @mcp with the %s arity; an MCP tool call answers once, so only a unary rpc can be exposed", svc.Name, rpc.Name, rpc.Arity)
			}
			rpc.LogicName = exportedName(rpc.Name) + "Logic"
			rpc.MethodName = exportedName(rpc.Name)
			rpc.FileName = snakeName(rpc.Name) + "_logic.go"
			rpc.CommandName = svc.Package + ":" + kebabName(rpc.Name)
			rpc.RequestGoType = typeRef(rpc.RequestType)
			rpc.ResultGoType = typeRef(rpc.ResponseType)
			rpc.RequestTSType = tsTypeRef(rpc.RequestType)
			rpc.ResultTSType = tsTypeRef(rpc.ResponseType)
			if len(rpc.Middlewares) == 0 {
				rpc.Middlewares = svc.Middlewares
			}
			// Middleware can't wrap a server-stream Sink handler; emitting one anyway
			// would silently drop an auth gate, so fail closed.
			if rpc.IsServerStream && len(rpc.Middlewares) > 0 {
				return nil, nil, fmt.Errorf("rpc %s.%s is a server-stream with @middleware %v; middleware is not supported on streaming handlers yet - remove it, or split the auth check into the handler body", svc.Name, rpc.Name, rpc.Middlewares)
			}
			if rpc.IsEvent {
				// Service-prefix the identifier so rpc names need only be unique
				// within their service (e.g. Message in both monitor and pubsub).
				rpc.EventName = rpc.CommandName
				rpc.EventGoName = rpc.MethodName
				if !strings.HasPrefix(rpc.EventGoName, svcExported) {
					rpc.EventGoName = svcExported + rpc.EventGoName
				}
				svc.Events = append(svc.Events, rpc)
				if rpc.EventDir == "in" {
					inEvents = append(inEvents, rpc)
				} else {
					outEvents = append(outEvents, rpc)
				}
				continue
			}
			commands = append(commands, rpc)
		}
		svc.RPCs = commands
	}
	return outEvents, inEvents, nil
}

// Call after enrichProto: CommandName, EventName and the TS types are assigned
// there, not by the parser, so earlier is a surface of blanks.
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
				MCP:        rpc.MCP,
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
