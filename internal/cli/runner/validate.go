package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

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
	tables, err := parseSQLSchema(schemaAbs, d)
	if err != nil {
		add("error", "schema", schemaRel, err.Error(), "")
		return finish(res)
	}
	res.Tables = len(tables)
	if res.Tables == 0 {
		add("warn", "schema", schemaRel, "no table declared, so generate model CLEARS the model wiring already in svc.go",
			"check the path and that the CREATE TABLE statements are not commented out")
	}
	return finish(res)
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
