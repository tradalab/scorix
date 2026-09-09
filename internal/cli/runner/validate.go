package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tradalab/scorix/internal/cli/runner/dialect"
)

// Severity is what a caller branches on: error means generate cannot run, warn
// means it will run and produce something the author probably did not intend.
type Finding struct {
	Severity string `json:"severity"` // error | warn
	Source   string `json:"source"`   // manifest | proto | schema
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
	Hint     string `json:"hint,omitempty"`
}

type ValidateResult struct {
	Proto    string    `json:"proto,omitempty"`
	Schema   string    `json:"schema,omitempty"`
	Services int       `json:"services"`
	Messages int       `json:"messages"`
	Tables   int       `json:"tables"`
	Findings []Finding `json:"findings,omitempty"`
}

type ValidateOptions struct {
	Dir     string
	JSONOut io.Writer // non-nil switches the result to one JSON document on this writer
}

// Writes nothing. Every failure it reports used to surface somewhere else: as
// drift to commit, as a compiler error inside generated code, or as a green run
// that quietly produced nothing.
func Validate(ctx context.Context, opt ValidateOptions) error {
	res := &ValidateResult{}
	err := validateProject(opt, res)
	printFindings(res)
	if opt.JSONOut != nil {
		return EmitJSON(opt.JSONOut, "validate", res, err)
	}
	return err
}

func validateProject(opt ValidateOptions, res *ValidateResult) error {
	if opt.Dir == "" {
		opt.Dir = "."
	}
	root, err := filepath.Abs(opt.Dir)
	if err != nil {
		return err
	}

	add := func(sev, source, path, msg, hint string) {
		res.Findings = append(res.Findings, Finding{
			Severity: sev, Source: source, Path: path, Message: msg, Hint: hint,
		})
	}

	cfg, err := loadProjectConfig(filepath.Join(root, "scorix.yaml"))
	if err != nil {
		add("error", "manifest", "scorix.yaml", err.Error(), "run scorix init, or fix the manifest by hand")
		return finish(res)
	}

	protoRel := cfg.Proto
	if protoRel == "" {
		protoRel = "idl/app.proto"
	}
	res.Proto = protoRel
	if src, err := os.ReadFile(filepath.Join(root, protoRel)); err != nil {
		add("error", "proto", protoRel, err.Error(), "scorix.yaml proto: names this file")
	} else if pf, err := parseProto(string(src)); err != nil {
		add("error", "proto", protoRel, err.Error(), "")
	} else {
		res.Messages, res.Services = len(pf.Messages), len(pf.Services)
		if res.Services == 0 {
			add("warn", "proto", protoRel, "no service declared, so no handler or client is generated",
				"declare a service, or drop the proto if this app has no IPC")
		}
	}

	schemaRel := "etc/schema.sql"
	if cfg.Model != nil && cfg.Model.Schema != "" {
		schemaRel = cfg.Model.Schema
	}
	res.Schema = schemaRel
	schemaAbs := filepath.Join(root, schemaRel)
	if _, err := os.Stat(schemaAbs); err != nil {
		add("error", "schema", schemaRel, err.Error(), "scorix.yaml model.schema names this file")
		return finish(res)
	}
	dialectName := ""
	if cfg.Model != nil {
		dialectName = cfg.Model.Dialect
	}
	d, err := dialect.New(dialectName)
	if err != nil {
		add("error", "manifest", "scorix.yaml", err.Error(), "model.dialect must be sqlite, mysql or postgres")
		return finish(res)
	}
	if cfg.Model != nil && cfg.Model.Migrations != "" {
		if _, _, err := resolveMigrationsPkg(root, cfg.Model.Migrations); err != nil {
			add("error", "manifest", "scorix.yaml", err.Error(),
				"model.migrations names a package exporting an embed.FS called FS and a Dir const")
		}
	}
	// The driver import lands in generated wiring, so a dialect the module does
	// not require turns a green generate into a build that cannot resolve it.
	if !moduleRequires(root, d.DriverImport()) {
		add("warn", "manifest", "go.mod",
			fmt.Sprintf("dialect %s generates an import of %s, which go.mod does not require", d.Name(), d.DriverImport()),
			"run go mod tidy after generate")
	}

	tables, err := parseSQLSchema(schemaAbs, d)
	if err != nil {
		add("error", "schema", schemaRel, err.Error(), "")
		return finish(res)
	}
	res.Tables = len(tables)
	// Silent before: the column keeps its name, loses the Insert hook that stamps
	// it, and the app writes rows with an empty timestamp nobody asked for.
	for _, t := range tables {
		for _, c := range t.Columns {
			if (c.Name == "created_at" || c.Name == "updated_at") && c.GoType != "time.Time" {
				add("warn", "schema", schemaRel,
					fmt.Sprintf("%s.%s is %s, which dialect %s maps to %s, so it is not stamped on insert",
						t.Name, c.Name, c.SQLType, d.Name(), c.GoType),
					"declare it as a timestamp type this dialect knows, or rename the column")
			}
		}
	}
	if res.Tables == 0 {
		add("warn", "schema", schemaRel, "no table declared, so generate model CLEARS the model wiring already in svc.go",
			"check the path and that the CREATE TABLE statements are not commented out")
	}
	return finish(res)
}

// The findings are the whole command, and they used to exist only inside the
// --json document: a human got "1 problem(s)" and never learned which. Printing
// on stdout puts them on stderr under --json, like every other runner.
func printFindings(res *ValidateResult) {
	for _, f := range res.Findings {
		where := f.Source
		if f.Path != "" {
			where += " " + f.Path
		}
		fmt.Printf("  %-5s %s: %s\n", f.Severity, where, f.Message)
		if f.Hint != "" {
			fmt.Printf("        %s\n", f.Hint)
		}
	}
	if countErrors(res) == 0 {
		fmt.Printf("==> Validate passed: %d table(s), %d service(s), %d warning(s)\n",
			res.Tables, res.Services, len(res.Findings))
	}
}

// moduleRequires answers from go.mod alone so validate stays offline. A missing
// go.mod is not a finding here: the compiler says it better.
func moduleRequires(root, importPath string) bool {
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return true
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "require "))
		if len(f) < 2 || !strings.HasPrefix(f[1], "v") {
			continue
		}
		if importPath == f[0] || strings.HasPrefix(importPath, f[0]+"/") {
			return true
		}
	}
	return false
}

func finish(res *ValidateResult) error {
	for _, f := range res.Findings {
		if f.Severity == "error" {
			return &ExitError{Code: ExitFailed, Kind: "invalid", Err: fmt.Errorf("%d problem(s) in the codegen inputs", countErrors(res))}
		}
	}
	return nil
}

func countErrors(res *ValidateResult) int {
	n := 0
	for _, f := range res.Findings {
		if f.Severity == "error" {
			n++
		}
	}
	return n
}
