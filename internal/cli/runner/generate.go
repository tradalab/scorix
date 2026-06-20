package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tradalab/scorix/internal/cli/template"
)

func GenerateProto(ctx context.Context, opt GenerateProtoOptions) error {
	if opt.Proto == "" {
		opt.Proto = "proto/app.proto"
	}
	if opt.Dir == "" {
		opt.Dir = "."
	}

	root, err := filepath.Abs(opt.Dir)
	if err != nil {
		return err
	}
	protoPath := opt.Proto
	if !filepath.IsAbs(protoPath) {
		protoPath = filepath.Join(root, protoPath)
	}

	data, err := os.ReadFile(protoPath)
	if err != nil {
		return fmt.Errorf("read proto: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return fmt.Errorf("proto file is empty: %s", protoPath)
	}

	modPath, err := readModulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		return err
	}

	pf, err := parseProto(string(data))
	if err != nil {
		return err
	}
	if len(pf.Services) == 0 {
		return fmt.Errorf("no service found in proto: %s", protoPath)
	}

	var outEvents, inEvents []protoRPC
	for i := range pf.Services {
		svc := &pf.Services[i]
		svc.Package = lowerCamel(svc.Name)
		svcExported := exportedName(svc.Name)
		commands := svc.RPCs[:0]
		for j := range svc.RPCs {
			rpc := svc.RPCs[j]
			// A stream keyword on a one-way event would be silently discarded.
			if rpc.IsEvent && rpc.Arity != "unary" {
				return fmt.Errorf("rpc %s.%s is @event/@broadcast and cannot also be a %s; drop the stream keyword or the annotation", svc.Name, rpc.Name, rpc.Arity)
			}
			if !rpc.IsEvent && (rpc.Arity == "client-stream" || rpc.Arity == "bidi") {
				return fmt.Errorf("rpc %s.%s uses the %s arity; codegen emits only unary and server-stream — wire %s by hand with app.RegisterDuplex", svc.Name, rpc.Name, rpc.Arity, rpc.Name)
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
				return fmt.Errorf("rpc %s.%s is a server-stream with @middleware %v; middleware is not supported on streaming handlers yet — remove it, or split the auth check into the handler body", svc.Name, rpc.Name, rpc.Middlewares)
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

	gen := protoTemplateData{
		Module:    modPath,
		Proto:     pf,
		Services:  pf.Services,
		OutEvents: outEvents,
		InEvents:  inEvents,
		HasEvents: len(outEvents)+len(inEvents) > 0,
	}

	pageGen := gen
	pageGen.Services = nil
	for _, svc := range pf.Services {
		if len(svc.RPCs) > 0 {
			pageGen.Services = append(pageGen.Services, svc)
		}
	}

	writes := []generatedFile{
		{
			Path:     filepath.Join(root, "internal", "types", "types.go"),
			Template: mustRead(template.GoTypes),
			Data:     gen,
			Go:       true,
			Force:    true,
		},
		{
			Path:     filepath.Join(root, "shell", "types", "index.ts"),
			Template: mustRead(template.ShellTypes),
			Data:     gen,
			Force:    true,
		},
		{
			Path:     filepath.Join(root, "shell", "api", "index.ts"),
			Template: mustRead(template.ShellAPI),
			Data:     gen,
			Force:    true,
		},
		{
			Path:     filepath.Join(root, "internal", "handler", "handler.go"),
			Template: mustRead(template.GoHandler),
			Data:     gen,
			Go:       true,
			Force:    true,
		},
		{
			Path:     filepath.Join(root, "main.go"),
			Template: mustRead(template.GoMain),
			Data:     gen,
			Go:       true,
			Force:    opt.Force,
		},
		{
			Path:     filepath.Join(root, "internal", "config", "config.go"),
			Template: mustRead(template.GoConfig),
			Data:     gen,
			Go:       true,
		},
	}

	if len(pageGen.Services) > 0 {
		writes = append(writes, generatedFile{
			Path:     filepath.Join(root, "shell", "app", "page.tsx"),
			Template: mustRead(template.ShellPage),
			Data:     pageGen,
			Force:    opt.Force,
		})
	}

	if gen.HasEvents {
		writes = append(writes, generatedFile{
			Path:     filepath.Join(root, "internal", "events", "events.go"),
			Template: mustRead(template.GoEvents),
			Data:     gen,
			Go:       true,
			Force:    true,
		})
	}
	if len(gen.OutEvents) > 0 {
		writes = append(writes, generatedFile{
			Path:     filepath.Join(root, "shell", "hooks", "events.ts"),
			Template: mustRead(template.ShellHooksEvents),
			Data:     gen,
			Force:    true,
		})
	}

	for _, m := range pf.Middlewares {
		writes = append(writes, generatedFile{
			Path:     filepath.Join(root, "internal", "middleware", snakeName(m)+"_middleware.go"),
			Template: mustRead(template.GoMiddleware),
			Data: middlewareTemplateData{
				Module:       modPath,
				Name:         m,
				ExportedName: exportedName(m),
			},
			Go:    true,
			Force: opt.Force,
		})
	}

	svcPath := filepath.Join(root, "internal", "svc", "service_context.go")
	writes = append(writes, generatedFile{
		Path:     svcPath,
		Template: mustRead(template.GoSvc),
		Data:     gen,
		Go:       true,
		Force:    opt.Force,
	})

	for _, svc := range pf.Services {
		for _, rpc := range svc.RPCs {
			writes = append(writes, generatedFile{
				Path:     filepath.Join(root, "internal", "logic", svc.Package, rpc.FileName),
				Template: mustRead(template.GoLogic),
				Data: logicTemplateData{
					Module:  modPath,
					Service: svc,
					RPC:     rpc,
				},
				Go:    true,
				Force: opt.Force,
			})
		}
	}

	if opt.Check {
		fmt.Printf("==> Checking generated code against %s\n", protoPath)
	} else {
		fmt.Printf("==> Generating Scorix code from %s\n", protoPath)
	}

	// Pass 1: render all; abort before any write if one fails.
	staged := make([]stagedFile, 0, len(writes))
	for _, f := range writes {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		s, err := renderGeneratedFile(f)
		if err != nil {
			return err
		}
		staged = append(staged, s)
	}

	if opt.Check {
		var drifted []string
		for _, s := range staged {
			reason, err := driftOf(s)
			if err != nil {
				return err
			}
			if reason != "" {
				drifted = append(drifted, driftLabel(root, s.Path, reason))
			}
		}
		return reportDrift(root, "scorix generate proto", drifted)
	}

	var created, updated, skipped int
	for _, s := range staged {
		if err := commitStagedFile(s); err != nil {
			return err
		}
		if s.Action != "skipped" {
			fmt.Printf("      %s: %s\n", s.Action, filepath.Base(s.Path))
		}
		switch s.Action {
		case "created":
			created++
		case "updated":
			updated++
		case "skipped":
			skipped++
		}
	}

	fmt.Printf("==> Proto generation complete! (created: %d, updated: %d, skipped: %d)\n", created, updated, skipped)
	return nil
}

func mustRead(path string) string {
	s, err := template.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return s
}
