package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tradalab/scorix/internal/cli/template"
)

func GenerateProto(ctx context.Context, opt GenerateProtoOptions) error {
	res := &GenerateResult{Check: opt.Check, Regen: "scorix generate proto"}
	err := generateProto(ctx, opt, res)
	if opt.JSONOut != nil {
		return EmitJSON(opt.JSONOut, "generate proto", res, err)
	}
	return err
}

func generateProto(ctx context.Context, opt GenerateProtoOptions, res *GenerateResult) error {
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
	shellName := ""
	if cfg != nil && cfg.Shell != nil {
		shellName = cfg.Shell.Type
	}
	shell, err := resolveShellKind(shellName)
	if err != nil {
		return fmt.Errorf("scorix.yaml shell: %w", err)
	}

	protoPath := resolveProtoPath(root, opt.Proto, cfg)

	pf, err := readProto(protoPath)
	if err != nil {
		return err
	}

	modPath, err := readModulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		return err
	}

	// Shared with `scorix surface`, so the two can never describe the same proto
	// differently.
	outEvents, inEvents, err := enrichProto(&pf)
	if err != nil {
		return err
	}

	gen := protoTemplateData{
		Module:    modPath,
		Proto:     pf,
		Services:  pf.Services,
		OutEvents: outEvents,
		InEvents:  inEvents,
		HasEvents: len(outEvents)+len(inEvents) > 0,
		Shell:     shell,
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

	if len(pageGen.Services) > 0 && shell.Page != "" {
		writes = append(writes, generatedFile{
			Path:     filepath.Join(root, "shell", filepath.FromSlash(shell.Page)),
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
	if len(gen.OutEvents) > 0 && shell.React {
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

	res.Surface = ipcSurfaceOf(pf)

	// `driftOf` counts a missing file as drift, so appending this in check mode
	// would turn six consumers' CI red over a doc they have never generated yet.
	docsPath := filepath.Join(root, "docs", "ipc-surface.md")
	if _, statErr := os.Stat(docsPath); statErr == nil || !opt.Check {
		writes = append(writes, generatedFile{
			Path:     docsPath,
			Template: mustRead(template.DocsIPCSurface),
			Data:     gen,
			Force:    true,
		})
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
		var drifted []DriftItem
		for _, s := range staged {
			reason, err := driftOf(s)
			if err != nil {
				return err
			}
			if reason != "" {
				drifted = append(drifted, driftLabel(root, s.Path, reason))
			}
		}
		res.Files, res.Drift = len(staged), drifted
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

	res.Files, res.Created, res.Updated, res.Skipped = len(staged), created, updated, skipped
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
