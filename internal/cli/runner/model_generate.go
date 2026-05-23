package runner

import (
	"context"
	"fmt"
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
	Name  string       `yaml:"name"`
	Model *ModelConfig `yaml:"model"`
	Build *BuildConfig `yaml:"build"`
}

type ModelConfig struct {
	Schema  string `yaml:"schema"`
	Dialect string `yaml:"dialect"` // sqlite | mysql | postgres
}

// BuildConfig carries options that `scorix dev` / `scorix build` should pass to `go run` / `go build`.
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

	if len(tables) == 0 {
		fmt.Println("No tables found in schema. Clearing generated model entries from svc.go.")
	} else {
		modelDir := filepath.Join(root, "internal", "model")
		if err := os.MkdirAll(modelDir, 0o755); err != nil {
			return err
		}

		for _, table := range tables {
			fmt.Printf("    Generating model for table: %s\n", table.Name)

			data := modelTemplateData{
				Module:  cfg.Name,
				Package: "model",
				Dialect: d,
				Table:   table,
				SQL:     buildSQL(table, d),
			}

			// model.go — interface + hand-edited methods; created once, opt.Force overwrites.
			modelFile := filepath.Join(modelDir, fmt.Sprintf("%s_model.go", table.TableName))
			action1, err := writeGeneratedFile(generatedFile{
				Path:     modelFile,
				Template: mustRead(template.GoModel),
				Data:     data,
				Go:       true,
				Force:    opt.Force,
			})
			if err != nil {
				return err
			}
			if action1 != "skipped" {
				fmt.Printf("      %s: %s_model.go\n", action1, table.TableName)
			}

			// model_gen.go — CRUD + struct, always regenerated.
			modelGenFile := filepath.Join(modelDir, fmt.Sprintf("%s_model_gen.go", table.TableName))
			action2, err := writeGeneratedFile(generatedFile{
				Path:     modelGenFile,
				Template: mustRead(template.GoModelGen),
				Data:     data,
				Go:       true,
				Force:    true,
			})
			if err != nil {
				return err
			}
			if action2 != "skipped" {
				fmt.Printf("      %s: %s_model_gen.go\n", action2, table.TableName)
			}
		}

		// schema_gen.go uses //go:embed so schema.sql stays as native SQL.
		schemaGenFile := filepath.Join(schemaDir, "schema_gen.go")
		action, err := writeGeneratedFile(generatedFile{
			Path:     schemaGenFile,
			Template: mustRead(template.GoSchemaGen),
			Data: schemaTemplateData{
				Package:    schemaPkgName,
				SchemaFile: filepath.Base(schemaAbs),
			},
			Go:    true,
			Force: true,
		})
		if err != nil {
			return err
		}
		if action != "skipped" {
			fmt.Printf("      %s: %s/schema_gen.go\n", action, filepath.ToSlash(relSchemaDir))
		}

		// Drop the legacy internal/model/schema_gen.go if present, so we don't
		// end up with duplicate SchemaSQL declarations across packages.
		legacyPath := filepath.Join(modelDir, "schema_gen.go")
		if legacyPath != schemaGenFile {
			if err := os.Remove(legacyPath); err == nil {
				fmt.Printf("      removed legacy: internal/model/schema_gen.go\n")
			}
		}
	}

	if err := patchServiceContext(root, cfg.Name, tables, schemaPkgImport, schemaPkgName); err != nil {
		return fmt.Errorf("patch service context: %w", err)
	}
	fmt.Println("==> Patched internal/svc/service_context.go")

	if len(tables) > 0 {
		if err := patchAppYaml(root, d); err != nil {
			return fmt.Errorf("patch app.yaml: %w", err)
		}
		fmt.Println("==> Ensured sqlx module enabled in etc/app.yaml")
	}

	cmd := exec.CommandContext(ctx, "go", "fmt", "./...")
	cmd.Dir = root
	_ = cmd.Run()

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
	svcPath := filepath.Join(root, "internal", "svc", "service_context.go")
	b, err := os.ReadFile(svcPath)
	if err != nil {
		return err
	}
	content := ensureMarkers(string(b))

	var imports, fields, init, assigns string
	if len(tables) > 0 {
		imports = fmt.Sprintf(
			"\tscorixsqlx \"github.com/tradalab/scorix/module/sqlx\"\n\t%q\n\t%q",
			moduleName+"/internal/model",
			schemaPkgImport,
		)

		var fieldLines, assignLines []string
		for _, t := range tables {
			fieldLines = append(fieldLines, fmt.Sprintf("\t%sModel model.%sModel", t.GoName, t.GoName))
			// sqlxMod.Conn (no parens) is a bound method value — defers
			// connection resolution until after OnLoad opens the DB.
			assignLines = append(assignLines, fmt.Sprintf("\t\t%sModel: model.New%sModel(sqlxMod.Conn),", t.GoName, t.GoName))
		}
		fields = strings.Join(fieldLines, "\n")
		assigns = strings.Join(assignLines, "\n")

		init = fmt.Sprintf(
			"\tsqlxMod := scorixsqlx.New(scorixsqlx.WithSchema(%s.SchemaSQL))\n\tapp.Modules().Register(sqlxMod)",
			schemaPkgName,
		)
	}

	content = replaceBetweenMarkers(content, markerImports, imports)
	content = replaceBetweenMarkers(content, markerFields, fields)
	content = replaceBetweenMarkers(content, markerInit, init)
	content = replaceBetweenMarkers(content, markerAssigns, assigns)

	return os.WriteFile(svcPath, []byte(content), 0o644)
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
		re := regexp.MustCompile(`(?s)(type ServiceContext struct \{[^}]*?)\n\}`)
		content = re.ReplaceAllString(content,
			"${1}\n\t// "+markerFields+":start\n\t// "+markerFields+":end\n}")
	}
	if !strings.Contains(content, markerInit+":start") {
		re := regexp.MustCompile(`(func NewServiceContext\([^)]+\) \*ServiceContext \{)`)
		content = re.ReplaceAllString(content,
			"${1}\n\t// "+markerInit+":start\n\t// "+markerInit+":end")
	}
	if !strings.Contains(content, markerAssigns+":start") {
		// Anchor on `\n\t}\n}` — literal close (one tab) + function close
		// (no indent) — so nested braces like `&config.Config{...}` don't fool us.
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

// patchAppYaml installs `modules.sqlx` in etc/app.yaml. Idempotent — re-runs
// noop when modules.sqlx already exists (preserves hand-tuned fields).
func patchAppYaml(root string, d dialect.Dialect) error {
	yamlPath := filepath.Join(root, "etc", "app.yaml")
	b, err := os.ReadFile(yamlPath)
	if err != nil {
		return err
	}

	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("parse app.yaml: %w", err)
	}
	if modules, ok := doc["modules"].(map[string]any); ok {
		if _, hasSqlx := modules["sqlx"]; hasSqlx {
			return nil
		}
	}

	content := string(b)
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	sqlxBlock := buildSqlxYamlBlock(d)

	re := regexp.MustCompile(`(?m)^modules:[ \t]*$`)
	if loc := re.FindStringIndex(content); loc != nil {
		insertion := "\n" + strings.TrimRight(sqlxBlock, "\n")
		content = content[:loc[1]] + insertion + content[loc[1]:]
	} else {
		content += "\nmodules:\n" + sqlxBlock
	}

	return os.WriteFile(yamlPath, []byte(content), 0o644)
}

func buildSqlxYamlBlock(d dialect.Dialect) string {
	return fmt.Sprintf("  sqlx:\n    enabled: true\n    driver: %s\n    dsn: %s\n",
		d.DriverName(), defaultDSN(d.Name()))
}

// defaultDSN: SQLite gets a filename (resolved against DataDir at runtime);
// MySQL/Postgres get placeholder credentials the user must replace.
func defaultDSN(dialectName string) string {
	switch dialectName {
	case "mysql":
		return "user:pass@tcp(127.0.0.1:3306)/app?parseTime=true"
	case "postgres":
		return "postgres://user:pass@127.0.0.1:5432/app?sslmode=disable"
	default:
		return "app.dat"
	}
}
