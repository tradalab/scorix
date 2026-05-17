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

	for i := range pf.Services {
		pf.Services[i].Package = lowerCamel(pf.Services[i].Name)
		for j := range pf.Services[i].RPCs {
			rpc := &pf.Services[i].RPCs[j]
			rpc.LogicName = exportedName(rpc.Name) + "Logic"
			rpc.MethodName = exportedName(rpc.Name)
			rpc.FileName = snakeName(rpc.Name) + "_logic.go"
			rpc.CommandName = pf.Services[i].Package + ":" + kebabName(rpc.Name)
			rpc.RequestGoType = typeRef(rpc.RequestType)
			rpc.ResultGoType = typeRef(rpc.ResponseType)
			rpc.RequestTSType = tsTypeRef(rpc.RequestType)
			rpc.ResultTSType = tsTypeRef(rpc.ResponseType)
			// Inherit service-level middlewares if RPC has none defined
			if len(rpc.Middlewares) == 0 {
				rpc.Middlewares = pf.Services[i].Middlewares
			}
		}
	}

	gen := protoTemplateData{
		Module:   modPath,
		Proto:    pf,
		Services: pf.Services,
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
			Path:     filepath.Join(root, "shell", "app", "page.tsx"),
			Template: mustRead(template.ShellPage),
			Data:     gen,
			Force:    opt.Force,
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
			Force:    true,
		},
		{
			Path:     filepath.Join(root, "internal", "config", "config.go"),
			Template: mustRead(template.GoConfig),
			Data:     gen,
			Go:       true,
		},
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

	fmt.Printf("==> Generating Scorix code from %s\n", protoPath)
	var created, updated, skipped int
	for _, f := range writes {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		action, err := writeGeneratedFile(f)
		if err != nil {
			return err
		}
		
		if action != "skipped" {
			fmt.Printf("      %s: %s\n", action, filepath.Base(f.Path))
		}

		switch action {
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
