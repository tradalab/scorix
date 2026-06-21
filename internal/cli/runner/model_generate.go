package runner

import (
	"bytes"
	"context"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tradalab/scorix/internal/cli/runner/dialect"
	"github.com/tradalab/scorix/internal/cli/template"
	"gopkg.in/yaml.v3"
)

type ProjectConfig struct {
	Name    string         `yaml:"name"`
	Proto   string         `yaml:"proto"`
	Model   *ModelConfig   `yaml:"model"`
	Build   *BuildConfig   `yaml:"build"`
	Package *PackageConfig `yaml:"package"`
}

type ModelConfig struct {
	Schema  string `yaml:"schema"`
	Dialect string `yaml:"dialect"` // sqlite | mysql | postgres
}

type BuildConfig struct {
	Tags []string `yaml:"tags"`
}

func loadProjectConfig(path string) (*ProjectConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg ProjectConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func GenerateModel(ctx context.Context, opt GenerateModelOptions) error {
	if opt.Dir == "" {
		opt.Dir = "."
	}

	root, err := filepath.Abs(opt.Dir)
	if err != nil {
		return err
	}

	cfg, err := loadProjectConfig(filepath.Join(root, "scorix.yaml"))
	if err != nil {
		return fmt.Errorf("load scorix.yaml: %w", err)
	}

	schemaPath := opt.Schema
	if schemaPath == "etc/schema.sql" { // default
		if cfg.Model != nil && cfg.Model.Schema != "" {
			schemaPath = cfg.Model.Schema
		}
	}

	schemaAbs := schemaPath
	if !filepath.IsAbs(schemaAbs) {
		schemaAbs = filepath.Join(root, schemaPath)
	}

	// --dialect flag overrides scorix.yaml; both empty → sqlite.
	dialectName := opt.Dialect
	if dialectName == "" && cfg.Model != nil {
		dialectName = cfg.Model.Dialect
	}
	d, err := dialect.New(dialectName)
	if err != nil {
		return fmt.Errorf("resolve dialect: %w", err)
	}
	if d.Name() != "sqlite" {
		// No app validates these dialects end-to-end yet (all are SQLite).
		fmt.Printf("warning: dialect %q is EXPERIMENTAL — no app validates it end-to-end yet; review generated SQL carefully\n", d.Name())
	}

	fmt.Printf("==> Parsing schema from %s (dialect: %s)\n", schemaPath, d.Name())
	tables, err := parseSQLSchema(schemaAbs, d)
	if err != nil {
		return fmt.Errorf("parse schema: %w", err)
	}

	// schema_gen.go must live next to schema.sql — //go:embed only resolves
	// siblings/descendants.
	schemaDir := filepath.Dir(schemaAbs)
	if schemaDir == root {
		return fmt.Errorf(
			"schema.sql at project root is not supported — the root package is typically `main` which cannot be imported.\n"+
				"Move %s into a subdirectory (recommended: etc/schema.sql) and update scorix.yaml model.schema accordingly",
			schemaPath)
	}
	schemaPkgName := sanitisePackageName(filepath.Base(schemaDir))
	relSchemaDir, err := filepath.Rel(root, schemaDir)
	if err != nil {
		return fmt.Errorf("resolve schema dir relative to project root: %w", err)
	}
	schemaPkgImport := cfg.Name + "/" + filepath.ToSlash(relSchemaDir)

	for _, t := range tables {
		if err := validateTableForCodegen(t); err != nil {
			return fmt.Errorf("table %q: %w", t.Name, err)
		}
	}

	var drifted []string

	if len(tables) == 0 {
		fmt.Println("No tables found in schema. Clearing generated model entries from svc.go.")
	} else {
		modelDir := filepath.Join(root, "internal", "model")
		if err := os.MkdirAll(modelDir, 0o755); err != nil {
			return err
		}

		// Pass 1: render all; abort before any write if one fails.
		type labelled struct {
			file  generatedFile
			label string
		}
		var pending []labelled

		for _, table := range tables {
			if opt.Check {
				fmt.Printf("    Checking model for table: %s\n", table.Name)
			} else {
				fmt.Printf("    Generating model for table: %s\n", table.Name)
			}

			data := modelTemplateData{
				Module:  cfg.Name,
				Package: "model",
				Dialect: d,
				Table:   table,
				SQL:     buildSQL(table, d),
			}

			// model.go — interface + hand-edited methods; created once, opt.Force overwrites.
			modelFile := filepath.Join(modelDir, fmt.Sprintf("%s_model.go", table.TableName))
			pending = append(pending, labelled{
				file: generatedFile{
					Path:     modelFile,
					Template: mustRead(template.GoModel),
					Data:     data,
					Go:       true,
					Force:    opt.Force,
				},
				label: fmt.Sprintf("%s_model.go", table.TableName),
			})

			// model_gen.go — CRUD + struct, always regenerated.
			modelGenFile := filepath.Join(modelDir, fmt.Sprintf("%s_model_gen.go", table.TableName))
			pending = append(pending, labelled{
				file: generatedFile{
					Path:     modelGenFile,
					Template: mustRead(template.GoModelGen),
					Data:     data,
					Go:       true,
					Force:    true,
				},
				label: fmt.Sprintf("%s_model_gen.go", table.TableName),
			})
		}

		// schema_gen.go uses //go:embed so schema.sql stays as native SQL.
		schemaGenFile := filepath.Join(schemaDir, "schema_gen.go")
		pending = append(pending, labelled{
			file: generatedFile{
				Path:     schemaGenFile,
				Template: mustRead(template.GoSchemaGen),
				Data: schemaTemplateData{
					Package:    schemaPkgName,
					SchemaFile: filepath.Base(schemaAbs),
				},
				Go:    true,
				Force: true,
			},
			label: fmt.Sprintf("%s/schema_gen.go", filepath.ToSlash(relSchemaDir)),
		})

		staged := make([]stagedFile, 0, len(pending))
		labels := make([]string, 0, len(pending))
		for _, p := range pending {
			s, err := renderGeneratedFile(p.file)
			if err != nil {
				return err
			}
			staged = append(staged, s)
			labels = append(labels, p.label)
		}

		if opt.Check {
			for _, s := range staged {
				reason, err := driftOf(s)
				if err != nil {
					return err
				}
				if reason != "" {
					drifted = append(drifted, driftLabel(root, s.Path, reason))
				}
			}
		} else {
			for i, s := range staged {
				if err := commitStagedFile(s); err != nil {
					return err
				}
				if s.Action != "skipped" {
					fmt.Printf("      %s: %s\n", s.Action, labels[i])
				}
			}

			// Drop legacy internal/model/schema_gen.go to avoid duplicate SchemaSQL across packages.
			legacyPath := filepath.Join(modelDir, "schema_gen.go")
			if legacyPath != schemaGenFile {
				if err := os.Remove(legacyPath); err == nil {
					fmt.Printf("      removed legacy: internal/model/schema_gen.go\n")
				}
			}
		}
	}

	if opt.Check {
		svcPath, svcNew, err := renderServiceContext(root, cfg.Name, tables, schemaPkgImport, schemaPkgName)
		if err != nil {
			return fmt.Errorf("render service context: %w", err)
		}
		svcDisk, err := os.ReadFile(svcPath)
		if err != nil {
			return err
		}
		if !bytes.Equal(normalizeNewlines(svcDisk), normalizeNewlines(svcNew)) {
			drifted = append(drifted, driftLabel(root, svcPath, "model markers out of date"))
		}
		return reportDrift(root, "scorix generate model", drifted)
	}

	if err := patchServiceContext(root, cfg.Name, tables, schemaPkgImport, schemaPkgName); err != nil {
		return fmt.Errorf("patch service context: %w", err)
	}
	fmt.Println("==> Patched internal/svc/service_context.go")

	// dsn is NOT written here: the module reads modules.sqlx / SCORIX_MODULE_SQLX_DSN
	// at runtime — one source, no drift.

	cmd := exec.CommandContext(ctx, "go", "fmt", "./...")
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		fmt.Printf("warning: go fmt ./... failed: %v\n", err)
	}

	fmt.Println("==> Model generation complete!")
	return nil
}

const (
	markerImports = "scorix:model:imports"
	markerFields  = "scorix:model:fields"
	markerInit    = "scorix:model:init"
	markerAssigns = "scorix:model:assigns"
)

func patchServiceContext(root, moduleName string, tables []sqlTable, schemaPkgImport, schemaPkgName string) error {
	svcPath, content, err := renderServiceContext(root, moduleName, tables, schemaPkgImport, schemaPkgName)
	if err != nil {
		return err
	}
	return os.WriteFile(svcPath, content, 0o644)
}

// renderServiceContext does not write, so --check can diff the result against disk.
func renderServiceContext(root, moduleName string, tables []sqlTable, schemaPkgImport, schemaPkgName string) (string, []byte, error) {
	svcPath := filepath.Join(root, "internal", "svc", "service_context.go")
	b, err := os.ReadFile(svcPath)
	if err != nil {
		return "", nil, err
	}
	content := ensureMarkers(string(b))

	var imports, fields, init, assigns string
	if len(tables) > 0 {
		imports = fmt.Sprintf(
			"\t\"github.com/jmoiron/sqlx\"\n\t_ \"modernc.org/sqlite\"\n\tscorixsqlx \"github.com/tradalab/scorix/module/sqlx\"\n\t%q\n\t%q",
			moduleName+"/internal/model",
			schemaPkgImport,
		)

		var fieldLines, assignLines []string
		for _, t := range tables {
			fieldLines = append(fieldLines, fmt.Sprintf("\t%sModel model.%sModel", t.GoName, t.GoName))
			// sqlxMod.Conn (no parens): bound method value, defers connection until OnLoad opens the DB.
			assignLines = append(assignLines, fmt.Sprintf("\t\t%sModel: model.New%sModel(sqlxMod.Conn),", t.GoName, t.GoName))
		}
		fields = strings.Join(fieldLines, "\n")
		assigns = strings.Join(assignLines, "\n")

		// No SetModuleConfig: the module self-defaults to sqlite/app.dat; override the
		// DSN via modules.sqlx or SCORIX_MODULE_SQLX_DSN at runtime — no rebuild.
		init = fmt.Sprintf(
			"\tsqlxMod := scorixsqlx.New(scorixsqlx.WithSchema(%s.SchemaSQL))\n"+
				"\tsqlxMod.RegisterDriver(\"sqlite\", func(dsn string) (*sqlx.DB, error) { return sqlx.Connect(\"sqlite\", dsn) })\n"+
				"\ta.Module(sqlxMod)",
			schemaPkgName,
		)
	}

	content = replaceBetweenMarkers(content, markerImports, imports)
	content = replaceBetweenMarkers(content, markerFields, fields)
	content = replaceBetweenMarkers(content, markerInit, init)
	content = replaceBetweenMarkers(content, markerAssigns, assigns)

	// gofmt so --check diffs against the formatted bytes; raw on parse error.
	if formatted, err := format.Source([]byte(content)); err == nil {
		return svcPath, formatted, nil
	}
	return svcPath, []byte(content), nil
}

// validateTableForCodegen rejects table shapes the generator would emit
// broken SQL for (composite PK, empty SET clause).
func validateTableForCodegen(t sqlTable) error {
	if len(t.PKSqlNames) == 0 {
		return fmt.Errorf("no primary key detected. Declare a PRIMARY KEY column so FindOne/Update/Delete can target rows")
	}
	if len(t.PKSqlNames) > 1 {
		return fmt.Errorf(
			"composite primary key (%s) is not yet supported by codegen. "+
				"Split into a surrogate `id` PK + UNIQUE(...) constraint",
			strings.Join(t.PKSqlNames, ", "))
	}
	var updatable int
	for _, c := range t.Columns {
		if c.IsPrimary {
			continue
		}
		switch c.Name {
		case "created_at", "updated_at", "deleted_at":
			continue
		}
		updatable++
	}
	if updatable == 0 {
		return fmt.Errorf(
			"no updatable columns (table only has PK + auto-managed timestamps). " +
				"Add at least one payload column or drop the table from the schema if it's truly read-only")
	}
	return nil
}

// sanitisePackageName falls back to "schema" when the directory name yields
// nothing valid (dots, dashes, leading digits dropped).
func sanitisePackageName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9' && i > 0:
			b.WriteRune(r)
		case r == '_':
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		return "schema"
	}
	return out
}

// ensureMarkers injects scorix:model markers into legacy svc.go files
// generated before markers existed.
func ensureMarkers(content string) string {
	if !strings.Contains(content, markerImports+":start") {
		content = strings.Replace(content, "import (",
			"import (\n\t// "+markerImports+":start\n\t// "+markerImports+":end", 1)
	}
	if !strings.Contains(content, markerFields+":start") {
		// Anchor on the column-0 `}`; `.*?` (not `[^}]*?`) so an inline struct field's
		// brace doesn't truncate the match.
		re := regexp.MustCompile(`(?s)(type ServiceContext struct \{.*?)\n\}`)
		content = re.ReplaceAllString(content,
			"${1}\n\t// "+markerFields+":start\n\t// "+markerFields+":end\n}")
	}
	if !strings.Contains(content, markerInit+":start") {
		// `.*?` (not `[^)]+`) so a param type containing `)` (e.g. `fn func()`) doesn't truncate.
		re := regexp.MustCompile(`(?s)(func NewServiceContext\(.*?\) \*ServiceContext \{)`)
		content = re.ReplaceAllString(content,
			"${1}\n\t// "+markerInit+":start\n\t// "+markerInit+":end")
	}
	if !strings.Contains(content, markerAssigns+":start") {
		// Anchor on `\n\t}\n}` (literal + func close) so nested braces like
		// `&config.Config{...}` don't fool us.
		re := regexp.MustCompile(`(?s)(return &ServiceContext\{.*?)\n\t\}\n\}`)
		content = re.ReplaceAllString(content,
			"${1}\n\t\t// "+markerAssigns+":start\n\t\t// "+markerAssigns+":end\n\t}\n}")
	}
	return content
}

func replaceBetweenMarkers(content, marker, replacement string) string {
	startMarker := "// " + marker + ":start"
	endMarker := "// " + marker + ":end"
	re := regexp.MustCompile(`(?s)([ \t]*` + regexp.QuoteMeta(startMarker) + `\n)(?:.*?)([ \t]*` + regexp.QuoteMeta(endMarker) + `)`)
	if !re.MatchString(content) {
		return content
	}
	if strings.TrimSpace(replacement) == "" {
		return re.ReplaceAllString(content, "${1}${2}")
	}
	return re.ReplaceAllString(content, "${1}"+replacement+"\n${2}")
}
