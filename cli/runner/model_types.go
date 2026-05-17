package runner

import "github.com/tradalab/scorix/cli/runner/dialect"

type GenerateModelOptions struct {
	Schema  string
	Dir     string
	Force   bool
	Dialect string // sqlite | mysql | postgres. Empty falls back to scorix.yaml / "sqlite".
}

// sqlTable is one CREATE TABLE post-parse. Per-table CRUD only — callers
// stitch relations in internal/logic/.
type sqlTable struct {
	Name      string
	GoName    string
	TableName string

	Columns       []sqlColumn
	UniqueColumns []sqlColumn // drives FindOneByX generation

	HasTime      bool
	HasNullable  bool
	HasDeletedAt bool

	PKGoType   string // first PK column's Go type — composite PKs rejected by validateTableForCodegen
	PKGoName   string
	PKGoFields []string
	PKSqlNames []string
}

type sqlColumn struct {
	Name      string
	GoName    string
	ParamName string // unexported camelCase for func params
	SQLType   string
	GoType    string

	JSONTag string

	IsPrimary bool
	IsAuto    bool
	IsUnique  bool
	IsNotNull bool

	// DefaultValue carries the raw token after `DEFAULT` so the SQL-side
	// UUID heuristic (looksLikeUUIDDefault) can fire.
	DefaultValue string
}

type modelTemplateData struct {
	Module  string
	Package string
	Dialect dialect.Dialect
	Table   sqlTable
	SQL     tableSQL
}

// schemaTemplateData drives the schema_gen.go that //go:embeds the raw
// schema.sql. Lives in schema.sql's directory because //go:embed only
// resolves siblings/descendants.
type schemaTemplateData struct {
	Package    string
	SchemaFile string
}
