package template

import "embed"

//go:embed all:static/*
var StaticFS embed.FS

// Template files use the .go.tpl suffix so `go build/vet/test ./...` skips
// them (their {{ }} syntax isn't valid Go). Generator emits .go output.
const (
	GoHandler    = "static/go/handler.go.tpl"
	GoEvents     = "static/go/events.go.tpl"
	GoLogic      = "static/go/logic.go.tpl"
	GoMain       = "static/go/main.go.tpl"
	GoSvc        = "static/go/svc.go.tpl"
	GoTypes      = "static/go/types.go.tpl"
	GoConfig     = "static/go/config.go.tpl"
	GoMiddleware = "static/go/middleware.go.tpl"
	GoModel      = "static/go/model.go.tpl"
	GoModelGen   = "static/go/model_gen.go.tpl"
	GoSchemaGen  = "static/go/schema_gen.go.tpl"

	ShellTypes       = "static/codegen/shell/types.ts"
	ShellAPI         = "static/codegen/shell/api.ts"
	ShellPage        = "static/codegen/shell/page.tsx"
	ShellHooksEvents = "static/codegen/shell/hooks_events.ts"

	ProjectScorixYaml = "static/project/scorix.yaml"
	ProjectProto      = "static/project/idl/app.proto"

	InstallerWindows = "static/project/installer/windows"
	InstallerLinux   = "static/project/installer/linux"
	InstallerMac     = "static/project/installer/mac"

	// Copied once by `scorix init`, unlike codegen/ which every `generate proto` rewrites.
	ScaffoldCommon = "static/scaffold/_common"
	ScaffoldDir    = "static/scaffold"
)

func ReadFile(path string) (string, error) {
	data, err := StaticFS.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
