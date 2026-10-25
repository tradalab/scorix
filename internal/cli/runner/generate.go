package runner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
		opt.Proto = DefaultProtoPath
	}
	if opt.Dir == "" {
		opt.Dir = "."
	}

	root, err := filepath.Abs(opt.Dir)
	if err != nil {
		return err
	}
	cfg, err := loadOptionalProjectConfig(filepath.Join(root, "scorix.yaml"))
	if err != nil {
		return err
	}
	shellName := ""
	if cfg != nil && cfg.Shell != nil {
		shellName = cfg.Shell.Type
	}
	shell, err := resolveShellKind(shellName)
	if err != nil {
		return fmt.Errorf("scorix.yaml shell: %w", err)
	}

	protoPath := resolveProtoPath(root, opt.Proto, cfg)
	if opt.Proto == DefaultProtoPath && !hasIPC(root) {
		// Modules still go over IPC, but a hand-wired shell has no lib/scorix for the clients to import.
		var writes []generatedFile
		lib := filepath.Join(root, "shell", "lib", "scorix")
		if fileExists(lib) || fileExists(lib+".ts") {
			f, err := moduleClientFile(root, cfg, opt.Check)
			if err != nil {
				return err
			} else if f != nil {
				writes = append(writes, *f)
			}
		}
		fmt.Println("==> No proto: only the module clients apply")
		return emitGenerated(ctx, root, opt.Check, res, writes)
	}

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
		Natives:   pf.Natives,
	}
	for _, svc := range pf.Services {
		gen.HasNativeRoute = gen.HasNativeRoute || svc.Native
		for _, rpc := range svc.RPCs {
			gen.HasMCP = gen.HasMCP || rpc.MCP
			gen.HasCLI = gen.HasCLI || rpc.CLI
		}
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

	writes = append(writes, generatedFile{
		Path:     filepath.Join(root, "internal", "handler", "modules.go"),
		Template: mustRead(template.GoModules),
		Data:     map[string]any{"Modules": enabledBuiltins(cfg)},
		Go:       true,
		Force:    true,
	})

	if f, err := moduleClientFile(root, cfg, opt.Check); err != nil {
		return err
	} else if f != nil {
		writes = append(writes, *f)
	}

	nativeWrites, err := nativeFiles(root, pf, gen)
	if err != nil {
		return err
	}
	writes = append(writes, nativeWrites...)

	for _, svc := range pf.Services {
		// An exposed native service's handler calls the plugin; there is no logic to write.
		if svc.Native {
			continue
		}
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
	return emitGenerated(ctx, root, opt.Check, res, writes)
}

func emitGenerated(ctx context.Context, root string, check bool, res *GenerateResult, writes []generatedFile) error {
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

	if check {
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

	var created, updated, skipped, unchanged int
	for _, s := range staged {
		if err := commitStagedFile(s); err != nil {
			return err
		}
		switch s.Action {
		case "created":
			created++
		case "updated":
			updated++
		case "skipped":
			skipped++
		case "unchanged":
			unchanged++
		}
		if s.Action == "created" || s.Action == "updated" {
			fmt.Printf("      %s: %s\n", s.Action, filepath.Base(s.Path))
		}
	}

	res.Files, res.Created, res.Updated, res.Skipped, res.Unchanged = len(staged), created, updated, skipped, unchanged
	fmt.Printf("==> Proto generation complete! (created: %d, updated: %d, unchanged: %d, skipped: %d)\n",
		created, updated, unchanged, skipped)
	return nil
}

// Once written, these stay in step even after the last @native service goes, so nothing is left
// calling a deleted type.
func nativeFiles(root string, pf protoFile, gen protoTemplateData) ([]generatedFile, error) {
	var out []generatedFile
	hostPath := filepath.Join(root, "internal", "host", "host.go")
	if len(pf.Natives) > 0 || fileExists(hostPath) {
		out = append(out, generatedFile{Path: hostPath, Template: mustRead(template.GoHost), Data: gen, Go: true, Force: true})
	}
	ktPath := filepath.Join(root, "android", "app", "src", "scorix", "kotlin", "dev", "scorix", "app", "ScorixPlugins.kt")
	if !fileExists(filepath.Join(root, "android", "app")) || (len(pf.Natives) == 0 && !fileExists(ktPath)) {
		return out, nil
	}
	appPackage := ""
	if len(pf.Natives) > 0 {
		meta, err := loadAppMetadata(root)
		if err != nil {
			return nil, err
		}
		// The implementations live in the package `mobile init` gave the app, its application ID.
		if appPackage, err = androidAppID(meta.App.Identifier); err != nil {
			return nil, err
		}
	}
	kt, err := kotlinNatives(pf, appPackage)
	if err != nil {
		return nil, err
	}
	if len(pf.Natives) > 0 {
		warnNativeSetup(root, pf.Natives)
	}
	return append(out, generatedFile{Path: ktPath, Template: mustRead(template.AndroidPlugins), Data: kt, Force: true}), nil
}

func mustRead(path string) string {
	s, err := template.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return s
}

// The same test as validate's: no proto key and no default proto is an app without services.
func hasIPC(root string) bool {
	cfg, err := loadProjectConfig(filepath.Join(root, "scorix.yaml"))
	if err != nil || cfg.Proto != "" {
		return true
	}
	_, err = os.Stat(filepath.Join(root, DefaultProtoPath))
	return !errors.Is(err, fs.ErrNotExist)
}
